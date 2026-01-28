package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/p2p"
)

func rollbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Manage package rollbacks",
		Long: `List and fetch old package versions from local cache and P2P peers.

Use 'debswarm rollback list <package>' to see available versions.
Use 'debswarm rollback fetch <package> <version>' to download a specific version.`,
	}

	cmd.AddCommand(rollbackListCmd())
	cmd.AddCommand(rollbackFetchCmd())
	cmd.AddCommand(rollbackMigrateCmd())

	return cmd
}

func rollbackListCmd() *cobra.Command {
	var allArch bool

	cmd := &cobra.Command{
		Use:   "list <package-name>",
		Short: "List available versions of a package",
		Long: `List all cached versions of a package that are available for rollback.

Shows version, architecture, size, last accessed time, and SHA256 hash.
By default, only shows versions for the current system architecture.
Use --all to show all architectures.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			packageName := args[0]

			logger, _ := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			maxSize := cfg.Cache.MaxSizeBytes()
			c, err := cache.New(cfg.Cache.Path, maxSize, logger)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			packages, err := c.ListByPackageName(packageName)
			if err != nil {
				return fmt.Errorf("failed to query cache: %w", err)
			}

			if len(packages) == 0 {
				fmt.Printf("No cached versions found for package '%s'\n", packageName)
				fmt.Println("\nHint: Package metadata may need migration. Try:")
				fmt.Println("  debswarm rollback migrate")
				return nil
			}

			// Filter by architecture if not --all
			currentArch := getDebianArch()
			if !allArch {
				filtered := make([]*cache.Package, 0, len(packages))
				for _, pkg := range packages {
					if pkg.Architecture == currentArch || pkg.Architecture == "all" {
						filtered = append(filtered, pkg)
					}
				}
				packages = filtered
			}

			if len(packages) == 0 {
				fmt.Printf("No cached versions found for package '%s' (arch: %s)\n", packageName, currentArch)
				fmt.Println("\nUse --all to show all architectures")
				return nil
			}

			// Sort by version (newest first is tricky, so sort by last_accessed)
			sort.Slice(packages, func(i, j int) bool {
				return packages[i].LastAccessed.After(packages[j].LastAccessed)
			})

			fmt.Printf("Available versions of '%s':\n\n", packageName)
			fmt.Printf("%-20s  %-10s  %10s  %-20s  %s\n",
				"VERSION", "ARCH", "SIZE", "LAST ACCESSED", "HASH")
			fmt.Println(strings.Repeat("-", 90))

			for _, pkg := range packages {
				hash := pkg.SHA256
				if len(hash) > 12 {
					hash = hash[:12] + "..."
				}
				fmt.Printf("%-20s  %-10s  %10s  %-20s  %s\n",
					truncateString(pkg.PackageVersion, 20),
					pkg.Architecture,
					formatBytes(pkg.Size),
					pkg.LastAccessed.Format("2006-01-02 15:04"),
					hash)
			}

			fmt.Printf("\nTotal: %d version(s)\n", len(packages))
			if !allArch {
				fmt.Println("\nUse --all to show all architectures")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&allArch, "all", false, "Show all architectures")

	return cmd
}

func rollbackFetchCmd() *cobra.Command {
	var outputPath string
	var arch string

	cmd := &cobra.Command{
		Use:   "fetch <package-name> <version>",
		Short: "Fetch a specific package version",
		Long: `Fetch a specific package version from local cache or P2P peers.

First checks the local cache. If the package is not cached, it will attempt
to find it via DHT and download from peers.

Examples:
  debswarm rollback fetch curl 7.88.1-10+deb12u5
  debswarm rollback fetch curl 7.88.1-10+deb12u5 --output /tmp/curl.deb
  debswarm rollback fetch curl 7.88.1-10+deb12u5 --arch arm64`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			packageName := args[0]
			version := args[1]

			// Default to current architecture
			if arch == "" {
				arch = getDebianArch()
			}

			logger, err := setupLogger()
			if err != nil {
				return err
			}

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			maxSize := cfg.Cache.MaxSizeBytes()
			c, err := cache.New(cfg.Cache.Path, maxSize, logger)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			// Try local cache first
			pkg, err := c.GetByNameVersionArch(packageName, version, arch)
			if err == nil {
				// Package found in cache
				return copyFromCache(c, pkg, outputPath)
			}

			if !errors.Is(err, cache.ErrNotFound) {
				return fmt.Errorf("cache query failed: %w", err)
			}

			// Not in cache - try P2P
			fmt.Printf("Package %s %s (%s) not in local cache\n", packageName, version, arch)
			fmt.Println("Searching P2P network...")

			// We need to find the hash for this package. The user might have the index loaded.
			// First try to construct the filename and search the index
			idx := index.New(cfg.Cache.Path, logger)
			expectedFilename := fmt.Sprintf("%s_%s_%s.deb", packageName, version, arch)

			// Search index for this package
			pkgInfo := idx.GetByPath(expectedFilename)
			if pkgInfo == nil {
				// Try searching by path suffix (in case it's in a pool directory)
				pkgInfo = idx.GetByPathSuffix(expectedFilename)
			}

			if pkgInfo == nil {
				fmt.Println("Package not found in index.")
				fmt.Println("\nThe package hash is required to fetch from P2P network.")
				fmt.Println("Try running 'apt update' through the debswarm proxy first to populate the index,")
				fmt.Println("or provide the package hash directly with 'debswarm cache get <hash>'.")
				return fmt.Errorf("package not found: %s %s (%s)", packageName, version, arch)
			}

			fmt.Printf("Found package in index: %s (hash: %s...)\n", pkgInfo.Filename, pkgInfo.SHA256[:16])

			// Initialize P2P node to search for providers
			return fetchFromP2P(cmd.Context(), cfg, logger, c, pkgInfo, outputPath)
		},
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output file path (default: ./<name>_<version>_<arch>.deb)")
	cmd.Flags().StringVar(&arch, "arch", "", "Architecture (default: current system arch)")

	return cmd
}

func rollbackMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Migrate existing cache entries to include package metadata",
		Long: `Scan cached packages that are missing metadata (name, version, architecture)
and populate the fields by parsing the filename.

This is useful after upgrading debswarm to support the rollback feature
if you have existing cached packages.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, _ := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			maxSize := cfg.Cache.MaxSizeBytes()
			c, err := cache.New(cfg.Cache.Path, maxSize, logger)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			fmt.Println("Scanning cache for packages with missing metadata...")

			updated, err := c.PopulateMissingMetadata()
			if err != nil {
				return fmt.Errorf("migration failed: %w", err)
			}

			if updated == 0 {
				fmt.Println("No packages needed migration.")
			} else {
				fmt.Printf("Successfully migrated %d package(s).\n", updated)
			}

			return nil
		},
	}
}

// copyFromCache copies a cached package to the output path
func copyFromCache(c *cache.Cache, pkg *cache.Package, outputPath string) error {
	if outputPath == "" {
		// Default filename
		if pkg.PackageName != "" && pkg.PackageVersion != "" && pkg.Architecture != "" {
			outputPath = fmt.Sprintf("%s_%s_%s.deb", pkg.PackageName, pkg.PackageVersion, pkg.Architecture)
		} else {
			outputPath = filepath.Base(pkg.Filename)
		}
	}

	// Expand to absolute path
	if !filepath.IsAbs(outputPath) {
		wd, _ := os.Getwd()
		outputPath = filepath.Join(wd, outputPath)
	}

	fmt.Printf("Copying from cache to %s...\n", outputPath)

	reader, _, err := c.Get(pkg.SHA256)
	if err != nil {
		return fmt.Errorf("failed to read from cache: %w", err)
	}
	defer reader.Close()

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	written, err := io.Copy(outFile, reader)
	if err != nil {
		return fmt.Errorf("failed to write output file: %w", err)
	}

	fmt.Printf("Successfully fetched %s (%s)\n", filepath.Base(outputPath), formatBytes(written))
	fmt.Printf("SHA256: %s\n", pkg.SHA256)

	return nil
}

// fetchFromP2P attempts to download a package from P2P peers
func fetchFromP2P(ctx context.Context, cfg interface{}, logger interface{}, c *cache.Cache, pkgInfo *index.PackageInfo, outputPath string) error {
	// For a full implementation, we would need to initialize the P2P node
	// and search for providers. This is a simplified version that shows
	// what would be needed.

	fmt.Println("\nP2P fetch not fully implemented in standalone CLI mode.")
	fmt.Println("To fetch packages from P2P, ensure the debswarm daemon is running")
	fmt.Println("and use the APT proxy to download the package:")
	fmt.Printf("\n  apt download %s=%s\n", pkgInfo.Package, pkgInfo.Version)

	return fmt.Errorf("P2P fetch requires running daemon")
}

// getDebianArch returns the Debian architecture name for the current system
func getDebianArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "386":
		return "i386"
	case "arm64":
		return "arm64"
	case "arm":
		return "armhf"
	default:
		return runtime.GOARCH
	}
}

// truncateString truncates a string to maxLen, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// fetchFromP2PFull is a more complete implementation that would be used
// when the full P2P stack is available. This is kept as a reference.
func fetchFromP2PFull(ctx context.Context, logger interface{}, c *cache.Cache, node *p2p.Node, pkgInfo *index.PackageInfo, outputPath string) error {
	// This would be the full implementation:
	// 1. Search DHT for providers of pkgInfo.SHA256
	// 2. Download from best peer
	// 3. Verify hash
	// 4. Save to outputPath

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	fmt.Printf("Searching DHT for %s...\n", pkgInfo.SHA256[:16])

	providers, err := node.FindProviders(ctx, pkgInfo.SHA256, 10)
	if err != nil {
		return fmt.Errorf("DHT lookup failed: %w", err)
	}

	if len(providers) == 0 {
		return fmt.Errorf("no P2P providers found for %s", pkgInfo.SHA256[:16])
	}

	fmt.Printf("Found %d provider(s), downloading...\n", len(providers))

	// Try each provider
	for _, provider := range providers {
		data, err := node.Download(ctx, provider, pkgInfo.SHA256)
		if err != nil {
			continue
		}

		// Determine output path
		if outputPath == "" {
			outputPath = fmt.Sprintf("%s_%s_%s.deb", pkgInfo.Package, pkgInfo.Version, pkgInfo.Architecture)
		}

		// Write to file
		if err := os.WriteFile(outputPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}

		fmt.Printf("Successfully downloaded %s (%s)\n", outputPath, formatBytes(int64(len(data))))
		return nil
	}

	return fmt.Errorf("all providers failed")
}
