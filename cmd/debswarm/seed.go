package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/p2p"
)

func seedCmd() *cobra.Command {
	var recursive bool
	var announce bool
	var sync bool
	var cachePath string

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed packages to the P2P network",
		Long: `Import and seed .deb packages to make them available to other peers.

This allows you to pre-populate the cache with packages from local files,
making them discoverable via the DHT without waiting for APT requests.`,
	}

	importCmd := &cobra.Command{
		Use:   "import [files or directories...]",
		Short: "Import .deb files into cache and announce to network",
		Long: `Import .deb packages from local files or directories.

Examples:
  debswarm seed import /var/cache/apt/archives/*.deb
  debswarm seed import --recursive /mirror/ubuntu/pool/
  debswarm seed import --recursive --sync /var/www/mirror/pool/
  debswarm seed import --cache-path /var/cache/debswarm /mirror/pool/
  debswarm seed import package1.deb package2.deb`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSeedImport(args, recursive, announce, sync, cachePath)
		},
	}

	importCmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Recursively scan directories")
	importCmd.Flags().BoolVarP(&announce, "announce", "a", true, "Announce packages to DHT")
	importCmd.Flags().BoolVar(&sync, "sync", false, "Remove cached packages not in source (mirror sync mode)")

	// Add cache-path as persistent flag so it's available to all subcommands
	cmd.PersistentFlags().StringVar(&cachePath, "cache-path", "", "Override cache path from config")

	cmd.AddCommand(importCmd)
	cmd.AddCommand(seedListCmd(&cachePath))

	return cmd
}

func seedListCmd(cachePath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List seeded packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger, _ := setupLogger()
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Override cache path if specified
			cacheDir := cfg.Cache.Path
			if cachePath != nil && *cachePath != "" {
				cacheDir = *cachePath
			}

			maxSize := cfg.Cache.MaxSizeBytes()
			c, err := cache.New(cacheDir, maxSize, logger)
			if err != nil {
				return err
			}
			defer c.Close()

			packages, err := c.List()
			if err != nil {
				return err
			}

			fmt.Printf("Seeded Packages: %d\n", len(packages))
			fmt.Printf("Total Size:      %s\n\n", formatBytes(c.Size()))

			for _, pkg := range packages {
				fmt.Printf("  %s  %10s  %s\n",
					pkg.SHA256[:16],
					formatBytes(pkg.Size),
					pkg.Filename)
			}

			return nil
		},
	}
}

func runSeedImport(args []string, recursive, announce, syncMode bool, cachePath string) error {
	logger, err := setupLogger()
	if err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Override cache path if specified
	cacheDir := cfg.Cache.Path
	if cachePath != "" {
		cacheDir = cachePath
	}

	// Initialize cache
	maxSize := cfg.Cache.MaxSizeBytes()
	pkgCache, err := cache.New(cacheDir, maxSize, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}
	defer pkgCache.Close()

	// Initialize P2P node if announcing
	var p2pNode *p2p.Node
	if announce {
		ctx := context.Background()
		p2pCfg := &p2p.Config{
			ListenPort:     cfg.Network.ListenPort,
			BootstrapPeers: cfg.Network.BootstrapPeers,
			EnableMDNS:     cfg.Privacy.EnableMDNS,
			PreferQUIC:     true,
		}

		p2pNode, err = p2p.New(ctx, p2pCfg, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize P2P: %w", err)
		}
		defer p2pNode.Close()

		fmt.Println("Waiting for DHT bootstrap...")
		p2pNode.WaitForBootstrap()
		fmt.Printf("Connected to %d peers\n\n", p2pNode.ConnectedPeers())
	}

	// Collect all .deb files
	var debFiles []string
	for _, arg := range args {
		files, err := collectDebFiles(arg, recursive)
		if err != nil {
			fmt.Printf("Warning: %s: %v\n", arg, err)
			continue
		}
		debFiles = append(debFiles, files...)
	}

	if len(debFiles) == 0 {
		return fmt.Errorf("no .deb files found")
	}

	fmt.Printf("Found %d .deb files to import\n\n", len(debFiles))

	// Track all source hashes for sync mode
	sourceHashes := make(map[string]struct{})

	// Import each file
	var imported, skipped, failed int
	for _, path := range debFiles {
		hash, size, err := importDebFile(pkgCache, path)
		if err != nil {
			if err.Error() == "already cached" {
				skipped++
				// Still track the hash for sync mode
				if syncMode {
					sourceHashes[hash] = struct{}{}
				}
				fmt.Printf("  [SKIP] %s (already cached)\n", filepath.Base(path))
			} else {
				failed++
				fmt.Printf("  [FAIL] %s: %v\n", filepath.Base(path), err)
			}
			continue
		}

		imported++
		if syncMode {
			sourceHashes[hash] = struct{}{}
		}
		fmt.Printf("  [OK]   %s (%s, %s)\n", filepath.Base(path), formatBytes(size), hash[:12]+"...")

		// Announce to DHT
		if announce && p2pNode != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := p2pNode.Provide(ctx, hash); err != nil {
				fmt.Printf("         Warning: failed to announce: %v\n", err)
			}
			cancel()
		}
	}

	fmt.Printf("\nSummary: %d imported, %d skipped, %d failed\n", imported, skipped, failed)

	// Sync mode: remove packages not in source
	if syncMode {
		fmt.Println("\nSync mode: checking for packages to remove...")
		cachedPkgs, err := pkgCache.List()
		if err != nil {
			return fmt.Errorf("failed to list cache: %w", err)
		}

		var removed int
		for _, pkg := range cachedPkgs {
			if _, exists := sourceHashes[pkg.SHA256]; !exists {
				if err := pkgCache.Delete(pkg.SHA256); err != nil {
					fmt.Printf("  [FAIL] Could not remove %s: %v\n", pkg.Filename, err)
				} else {
					removed++
					fmt.Printf("  [DEL]  %s (%s)\n", pkg.Filename, pkg.SHA256[:12]+"...")
				}
			}
		}
		fmt.Printf("\nRemoved %d old packages\n", removed)
	}

	fmt.Printf("Cache size: %s (%d packages)\n", formatBytes(pkgCache.Size()), pkgCache.Count())

	return nil
}

func collectDebFiles(path string, recursive bool) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		if strings.HasSuffix(strings.ToLower(path), ".deb") {
			return []string{path}, nil
		}
		return nil, nil
	}

	// Directory
	var files []string
	err = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && p != path && !recursive {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(p), ".deb") {
			files = append(files, p)
		}
		return nil
	})

	return files, err
}

func importDebFile(c *cache.Cache, path string) (string, int64, error) {
	// Open file
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	// Get file size
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}

	// Calculate SHA256
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", 0, err
	}
	hash := hex.EncodeToString(hasher.Sum(nil))

	// Check if already cached
	if c.Has(hash) {
		return hash, info.Size(), fmt.Errorf("already cached")
	}

	// Seek back to start
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}

	// Store in cache
	filename := filepath.Base(path)
	if err := c.Put(f, hash, filename); err != nil {
		return "", 0, err
	}

	return hash, info.Size(), nil
}
