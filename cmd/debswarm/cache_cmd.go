package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/debswarm/debswarm/internal/cache"
)

func cacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the package cache",
	}

	cmd.AddCommand(cacheListCmd())
	cmd.AddCommand(cacheClearCmd())
	cmd.AddCommand(cacheStatsCmd())
	cmd.AddCommand(cacheVerifyCmd())
	cmd.AddCommand(cachePopularCmd())
	cmd.AddCommand(cacheRecentCmd())

	return cmd
}

func cacheListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List cached packages",
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

			packages, err := c.List()
			if err != nil {
				return err
			}

			fmt.Printf("Cached Packages: %d\n", len(packages))
			fmt.Printf("Total Size:      %s\n", formatBytes(c.Size()))
			fmt.Println()

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

func cacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Clear the cache",
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

			packages, err := c.List()
			if err != nil {
				return err
			}

			for _, pkg := range packages {
				if err := c.Delete(pkg.SHA256); err != nil {
					fmt.Printf("Failed to delete %s: %v\n", pkg.SHA256[:16], err)
				}
			}

			fmt.Printf("Cleared %d packages\n", len(packages))
			return nil
		},
	}
}

func cacheStatsCmd() *cobra.Command {
	var showPopular int

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show cache statistics",
		Long:  "Show comprehensive cache statistics including usage, bandwidth savings, and optionally top packages.",
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

			stats, err := c.Stats()
			if err != nil {
				return fmt.Errorf("failed to get cache stats: %w", err)
			}
			unannounced, err := c.GetUnannounced()
			if err != nil {
				return fmt.Errorf("failed to get unannounced packages: %w", err)
			}

			fmt.Printf("Cache Statistics\n")
			fmt.Printf("══════════════════════════════════════\n")
			fmt.Printf("Total Packages:    %d\n", stats.TotalPackages)
			fmt.Printf("With Metadata:     %d\n", stats.UniquePackages)
			fmt.Printf("Total Size:        %s\n", formatBytes(stats.TotalSize))
			fmt.Printf("Max Size:          %s\n", cfg.Cache.MaxSize)
			fmt.Printf("Usage:             %.1f%%\n", float64(stats.TotalSize)/float64(maxSize)*100)
			fmt.Printf("Unannounced:       %d\n", len(unannounced))
			fmt.Println()
			fmt.Printf("Access Statistics\n")
			fmt.Printf("──────────────────────────────────────\n")
			fmt.Printf("Total Accesses:    %d\n", stats.TotalAccesses)
			fmt.Printf("Bandwidth Saved:   %s\n", formatBytes(stats.BandwidthSaved))
			if stats.TotalPackages > 0 {
				avgAccesses := float64(stats.TotalAccesses) / float64(stats.TotalPackages)
				fmt.Printf("Avg Accesses/Pkg:  %.1f\n", avgAccesses)
			}

			if showPopular > 0 {
				popular, err := c.PopularPackages(showPopular)
				if err != nil {
					return fmt.Errorf("failed to get popular packages: %w", err)
				}
				if len(popular) > 0 {
					fmt.Println()
					fmt.Printf("Top %d Packages by Access Count\n", len(popular))
					fmt.Printf("──────────────────────────────────────\n")
					for i, pkg := range popular {
						name := pkg.Filename
						if pkg.PackageName != "" {
							name = pkg.PackageName
							if pkg.PackageVersion != "" {
								name += " " + pkg.PackageVersion
							}
						}
						fmt.Printf("  %2d. %-30s  %6d accesses  %s\n",
							i+1, truncateString(name, 30), pkg.AccessCount, formatBytes(pkg.Size))
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&showPopular, "popular", "p", 0, "Show top N popular packages")
	return cmd
}

func cacheVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Verify integrity of all cached packages",
		Long:  "Verify that all cached packages match their expected SHA256 hashes. Reports any corrupted or missing files.",
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

			packages, err := c.List()
			if err != nil {
				return fmt.Errorf("failed to list packages: %w", err)
			}

			if len(packages) == 0 {
				fmt.Println("Cache is empty, nothing to verify.")
				return nil
			}

			fmt.Printf("Verifying %d cached packages...\n\n", len(packages))

			var verified, corrupted, missing int
			var corruptedList []string

			for _, pkg := range packages {
				// Build file path (same logic as cache.packagePath)
				filePath := filepath.Join(cfg.Cache.Path, "packages", "sha256", pkg.SHA256[:2], pkg.SHA256)

				f, err := os.Open(filePath)
				if err != nil {
					if os.IsNotExist(err) {
						fmt.Printf("  MISSING  %s  %s\n", pkg.SHA256[:16], pkg.Filename)
						missing++
						corruptedList = append(corruptedList, pkg.SHA256)
						continue
					}
					return fmt.Errorf("failed to open %s: %w", pkg.SHA256[:16], err)
				}

				hasher := sha256.New()
				if _, err := io.Copy(hasher, f); err != nil {
					_ = f.Close()
					return fmt.Errorf("failed to read %s: %w", pkg.SHA256[:16], err)
				}
				_ = f.Close()

				actualHash := hex.EncodeToString(hasher.Sum(nil))
				if actualHash != pkg.SHA256 {
					fmt.Printf("  CORRUPT  %s  %s\n", pkg.SHA256[:16], pkg.Filename)
					fmt.Printf("           Expected: %s\n", pkg.SHA256)
					fmt.Printf("           Got:      %s\n", actualHash)
					corrupted++
					corruptedList = append(corruptedList, pkg.SHA256)
				} else {
					verified++
				}
			}

			fmt.Println()
			fmt.Printf("Verification complete:\n")
			fmt.Printf("  Verified:  %d\n", verified)
			fmt.Printf("  Corrupted: %d\n", corrupted)
			fmt.Printf("  Missing:   %d\n", missing)

			if len(corruptedList) > 0 {
				fmt.Println()
				fmt.Println("To remove corrupted/missing entries, run:")
				for _, hash := range corruptedList {
					fmt.Printf("  debswarm cache delete %s\n", hash[:16])
				}
				return fmt.Errorf("verification failed: %d issues found", len(corruptedList))
			}

			fmt.Println("\nAll packages verified successfully.")
			return nil
		},
	}
}

func cachePopularCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "popular",
		Short: "Show most frequently accessed packages",
		Long:  "List cached packages sorted by access count, showing the most popular packages first.",
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

			packages, err := c.PopularPackages(limit)
			if err != nil {
				return fmt.Errorf("failed to get popular packages: %w", err)
			}

			if len(packages) == 0 {
				fmt.Println("No packages in cache.")
				return nil
			}

			fmt.Printf("Top %d Packages by Access Count\n", len(packages))
			fmt.Printf("══════════════════════════════════════════════════════════════════════\n")
			fmt.Printf("  %-4s  %-35s  %-10s  %-8s  %s\n", "Rank", "Package", "Accesses", "Size", "Last Accessed")
			fmt.Printf("──────────────────────────────────────────────────────────────────────\n")

			for i, pkg := range packages {
				name := pkg.Filename
				if pkg.PackageName != "" {
					name = pkg.PackageName
					if pkg.PackageVersion != "" {
						name += " " + pkg.PackageVersion
					}
				}
				fmt.Printf("  %-4d  %-35s  %-10d  %-8s  %s\n",
					i+1,
					truncateString(name, 35),
					pkg.AccessCount,
					formatBytes(pkg.Size),
					pkg.LastAccessed.Format("2006-01-02 15:04"))
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Number of packages to show")
	return cmd
}

func cacheRecentCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "recent",
		Short: "Show most recently accessed packages",
		Long:  "List cached packages sorted by last access time, showing the most recently used packages first.",
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

			packages, err := c.RecentPackages(limit)
			if err != nil {
				return fmt.Errorf("failed to get recent packages: %w", err)
			}

			if len(packages) == 0 {
				fmt.Println("No packages in cache.")
				return nil
			}

			fmt.Printf("Top %d Recently Accessed Packages\n", len(packages))
			fmt.Printf("══════════════════════════════════════════════════════════════════════\n")
			fmt.Printf("  %-35s  %-10s  %-8s  %s\n", "Package", "Accesses", "Size", "Last Accessed")
			fmt.Printf("──────────────────────────────────────────────────────────────────────\n")

			for _, pkg := range packages {
				name := pkg.Filename
				if pkg.PackageName != "" {
					name = pkg.PackageName
					if pkg.PackageVersion != "" {
						name += " " + pkg.PackageVersion
					}
				}
				fmt.Printf("  %-35s  %-10d  %-8s  %s\n",
					truncateString(name, 35),
					pkg.AccessCount,
					formatBytes(pkg.Size),
					pkg.LastAccessed.Format("2006-01-02 15:04"))
			}

			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Number of packages to show")
	return cmd
}
