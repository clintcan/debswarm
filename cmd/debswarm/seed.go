package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/p2p"
)

// syncState tracks the last sync time for incremental syncs
type syncState struct {
	LastSync time.Time `json:"last_sync"`
	Path     string    `json:"path"`
}

func seedCmd() *cobra.Command {
	var recursive bool
	var announce bool
	var syncMode bool
	var cachePath string
	var parallel int
	var dryRun bool
	var incremental bool
	var watch bool
	var showProgress bool

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
  debswarm seed import --recursive --parallel 8 /mirror/pool/
  debswarm seed import --recursive --sync --incremental /mirror/pool/
  debswarm seed import --recursive --sync --dry-run /mirror/pool/
  debswarm seed import --recursive --watch /mirror/pool/`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := &seedImportOptions{
				recursive:    recursive,
				announce:     announce,
				syncMode:     syncMode,
				cachePath:    cachePath,
				parallel:     parallel,
				dryRun:       dryRun,
				incremental:  incremental,
				watch:        watch,
				showProgress: showProgress,
			}
			return runSeedImport(args, opts)
		},
	}

	importCmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Recursively scan directories")
	importCmd.Flags().BoolVarP(&announce, "announce", "a", true, "Announce packages to DHT")
	importCmd.Flags().BoolVar(&syncMode, "sync", false, "Remove cached packages not in source (mirror sync mode)")
	importCmd.Flags().IntVarP(&parallel, "parallel", "p", 1, "Number of parallel import workers (default 1)")
	importCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without making them")
	importCmd.Flags().BoolVar(&incremental, "incremental", false, "Only process files modified since last sync")
	importCmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch for changes and import automatically")
	importCmd.Flags().BoolVar(&showProgress, "progress", false, "Show progress bar instead of per-file output")

	// Add cache-path as persistent flag so it's available to all subcommands
	cmd.PersistentFlags().StringVar(&cachePath, "cache-path", "", "Override cache path from config")

	cmd.AddCommand(importCmd)
	cmd.AddCommand(seedListCmd(&cachePath))

	return cmd
}

type seedImportOptions struct {
	recursive    bool
	announce     bool
	syncMode     bool
	cachePath    string
	parallel     int
	dryRun       bool
	incremental  bool
	watch        bool
	showProgress bool
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
			defer func() { _ = c.Close() }()

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

func runSeedImport(args []string, opts *seedImportOptions) error {
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
	if opts.cachePath != "" {
		cacheDir = opts.cachePath
	}

	// Validate parallel count
	if opts.parallel < 1 {
		opts.parallel = 1
	}
	if opts.parallel > 32 {
		opts.parallel = 32 // Cap at reasonable limit
	}

	// Initialize cache (unless dry-run)
	var pkgCache *cache.Cache
	if !opts.dryRun {
		maxSize := cfg.Cache.MaxSizeBytes()
		pkgCache, err = cache.New(cacheDir, maxSize, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize cache: %w", err)
		}
		defer func() { _ = pkgCache.Close() }()
	}

	// Initialize P2P node if announcing (and not dry-run)
	var p2pNode *p2p.Node
	if opts.announce && !opts.dryRun {
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
		defer func() { _ = p2pNode.Close() }()

		fmt.Println("Waiting for DHT bootstrap...")
		p2pNode.WaitForBootstrap()
		fmt.Printf("Connected to %d peers\n\n", p2pNode.ConnectedPeers())
	}

	if opts.dryRun {
		fmt.Println("DRY-RUN MODE: No changes will be made")
		fmt.Println()
	}

	// Watch mode: continuous monitoring
	if opts.watch {
		return runWatchMode(args, opts, pkgCache, p2pNode, cacheDir)
	}

	// Single import run
	return runSingleImport(args, opts, pkgCache, p2pNode, cacheDir)
}

func runSingleImport(args []string, opts *seedImportOptions, pkgCache *cache.Cache, p2pNode *p2p.Node, cacheDir string) error {
	// Load last sync time for incremental mode
	var lastSync time.Time
	var stateFile string
	if opts.incremental {
		stateFile = filepath.Join(cacheDir, ".sync-state.json")
		lastSync = loadSyncState(stateFile, args[0])
		if !lastSync.IsZero() {
			fmt.Printf("Incremental mode: only files modified after %s\n\n", lastSync.Format(time.RFC3339))
		}
	}

	// Collect all .deb files
	var debFiles []string
	for _, arg := range args {
		files, err := collectDebFiles(arg, opts.recursive, lastSync)
		if err != nil {
			fmt.Printf("Warning: %s: %v\n", arg, err)
			continue
		}
		debFiles = append(debFiles, files...)
	}

	if len(debFiles) == 0 {
		if opts.incremental && !lastSync.IsZero() {
			fmt.Println("No new or modified .deb files found since last sync.")
			return nil
		}
		return fmt.Errorf("no .deb files found")
	}

	fmt.Printf("Found %d .deb files to process\n", len(debFiles))
	if opts.parallel > 1 {
		fmt.Printf("Using %d parallel workers\n", opts.parallel)
	}
	fmt.Println()

	// Track all source hashes for sync mode
	sourceHashes := sync.Map{}

	// Import statistics
	var imported, skipped, failed int64
	var totalBytes int64

	// Progress tracking
	var processed int64
	total := int64(len(debFiles))

	// Results channel for collecting import results
	type importResult struct {
		path    string
		hash    string
		size    int64
		err     error
		skipped bool
	}
	results := make(chan importResult, opts.parallel)

	// Start worker pool
	var wg sync.WaitGroup
	fileChan := make(chan string, opts.parallel)

	for i := 0; i < opts.parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range fileChan {
				hash, size, err := processDebFile(pkgCache, path, opts.dryRun)
				results <- importResult{
					path:    path,
					hash:    hash,
					size:    size,
					err:     err,
					skipped: err != nil && err.Error() == "already cached",
				}
			}
		}()
	}

	// Start result collector
	done := make(chan struct{})
	go func() {
		for result := range results {
			current := atomic.AddInt64(&processed, 1)

			if result.skipped {
				atomic.AddInt64(&skipped, 1)
				sourceHashes.Store(result.hash, struct{}{})
				if !opts.showProgress {
					fmt.Printf("  [SKIP] %s (already cached)\n", filepath.Base(result.path))
				}
			} else if result.err != nil {
				atomic.AddInt64(&failed, 1)
				if !opts.showProgress {
					fmt.Printf("  [FAIL] %s: %v\n", filepath.Base(result.path), result.err)
				}
			} else {
				atomic.AddInt64(&imported, 1)
				atomic.AddInt64(&totalBytes, result.size)
				sourceHashes.Store(result.hash, struct{}{})
				if !opts.showProgress {
					if opts.dryRun {
						fmt.Printf("  [WOULD IMPORT] %s (%s, %s)\n", filepath.Base(result.path), formatBytes(result.size), result.hash[:12]+"...")
					} else {
						fmt.Printf("  [OK]   %s (%s, %s)\n", filepath.Base(result.path), formatBytes(result.size), result.hash[:12]+"...")
					}
				}

				// Announce to DHT (not in dry-run)
				if opts.announce && p2pNode != nil && !opts.dryRun {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					if err := p2pNode.Provide(ctx, result.hash); err != nil {
						if !opts.showProgress {
							fmt.Printf("         Warning: failed to announce: %v\n", err)
						}
					}
					cancel()
				}
			}

			// Progress bar
			if opts.showProgress {
				printProgress(current, total, imported, skipped, failed)
			}
		}
		close(done)
	}()

	// Feed files to workers
	for _, path := range debFiles {
		fileChan <- path
	}
	close(fileChan)

	// Wait for workers to finish
	wg.Wait()
	close(results)
	<-done

	// Clear progress line if used
	if opts.showProgress {
		fmt.Println()
	}

	// Summary
	fmt.Printf("\n")
	if opts.dryRun {
		fmt.Printf("DRY-RUN Summary:\n")
		fmt.Printf("  Would import: %d packages (%s)\n", imported, formatBytes(totalBytes))
		fmt.Printf("  Already cached: %d\n", skipped)
		fmt.Printf("  Would fail: %d\n", failed)
	} else {
		fmt.Printf("Summary: %d imported (%s), %d skipped, %d failed\n", imported, formatBytes(totalBytes), skipped, failed)
	}

	// Sync mode: remove packages not in source
	if opts.syncMode {
		removed, wouldRemove := runSyncRemoval(pkgCache, &sourceHashes, opts.dryRun)
		if opts.dryRun {
			fmt.Printf("  Would remove: %d packages\n", wouldRemove)
		} else {
			fmt.Printf("Removed %d old packages\n", removed)
		}
	}

	// Show cache stats (not in dry-run)
	if !opts.dryRun {
		fmt.Printf("Cache size: %s (%d packages)\n", formatBytes(pkgCache.Size()), pkgCache.Count())
	}

	// Save sync state for incremental mode
	if opts.incremental && !opts.dryRun {
		saveSyncState(stateFile, args[0])
	}

	return nil
}

func runSyncRemoval(pkgCache *cache.Cache, sourceHashes *sync.Map, dryRun bool) (removed, wouldRemove int) {
	if dryRun {
		fmt.Println("\nSync mode: checking for packages that would be removed...")
	} else {
		fmt.Println("\nSync mode: checking for packages to remove...")
	}

	cachedPkgs, err := pkgCache.List()
	if err != nil {
		fmt.Printf("  Failed to list cache: %v\n", err)
		return 0, 0
	}

	for _, pkg := range cachedPkgs {
		if _, exists := sourceHashes.Load(pkg.SHA256); !exists {
			if dryRun {
				fmt.Printf("  [WOULD DEL] %s (%s)\n", pkg.Filename, pkg.SHA256[:12]+"...")
				wouldRemove++
			} else {
				if err := pkgCache.Delete(pkg.SHA256); err != nil {
					fmt.Printf("  [FAIL] Could not remove %s: %v\n", pkg.Filename, err)
				} else {
					removed++
					fmt.Printf("  [DEL]  %s (%s)\n", pkg.Filename, pkg.SHA256[:12]+"...")
				}
			}
		}
	}
	return removed, wouldRemove
}

func runWatchMode(args []string, opts *seedImportOptions, pkgCache *cache.Cache, p2pNode *p2p.Node, cacheDir string) error {
	fmt.Println("Watch mode: monitoring for changes (Ctrl+C to stop)")
	fmt.Println()

	// Do initial import
	if err := runSingleImport(args, opts, pkgCache, p2pNode, cacheDir); err != nil {
		// Don't fail on initial import errors in watch mode
		fmt.Printf("Initial import warning: %v\n", err)
	}

	// Set up file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// Add directories to watch
	for _, arg := range args {
		if err := addWatchPaths(watcher, arg, opts.recursive); err != nil {
			fmt.Printf("Warning: could not watch %s: %v\n", arg, err)
		}
	}

	fmt.Printf("\nWatching %d directories for changes...\n", len(watcher.WatchList()))

	// Debounce timer for batching rapid changes
	var debounceTimer *time.Timer
	pendingFiles := make(map[string]struct{})
	var pendingMu sync.Mutex

	processPending := func() {
		pendingMu.Lock()
		files := make([]string, 0, len(pendingFiles))
		for f := range pendingFiles {
			files = append(files, f)
		}
		pendingFiles = make(map[string]struct{})
		pendingMu.Unlock()

		if len(files) == 0 {
			return
		}

		fmt.Printf("\n[%s] Processing %d changed files...\n", time.Now().Format("15:04:05"), len(files))
		for _, path := range files {
			hash, size, err := processDebFile(pkgCache, path, opts.dryRun)
			if err != nil {
				if err.Error() == "already cached" {
					fmt.Printf("  [SKIP] %s\n", filepath.Base(path))
				} else {
					fmt.Printf("  [FAIL] %s: %v\n", filepath.Base(path), err)
				}
				continue
			}

			if opts.dryRun {
				fmt.Printf("  [WOULD IMPORT] %s (%s)\n", filepath.Base(path), formatBytes(size))
			} else {
				fmt.Printf("  [OK]   %s (%s)\n", filepath.Base(path), formatBytes(size))

				// Announce to DHT
				if opts.announce && p2pNode != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					if err := p2pNode.Provide(ctx, hash); err != nil {
						fmt.Printf("         Warning: failed to announce: %v\n", err)
					}
					cancel()
				}
			}
		}
	}

	// Watch for events
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			// Only care about .deb files being created or modified
			if !strings.HasSuffix(strings.ToLower(event.Name), ".deb") {
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}

			// Add to pending files
			pendingMu.Lock()
			pendingFiles[event.Name] = struct{}{}
			pendingMu.Unlock()

			// Reset debounce timer
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(2*time.Second, processPending)

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Printf("Watch error: %v\n", err)
		}
	}
}

func addWatchPaths(watcher *fsnotify.Watcher, path string, recursive bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return watcher.Add(filepath.Dir(path))
	}

	if !recursive {
		return watcher.Add(path)
	}

	//nolint:nilerr // intentionally skip inaccessible paths during watch setup
	return filepath.Walk(path, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			_ = watcher.Add(p)
		}
		return nil
	})
}

func collectDebFiles(path string, recursive bool, modifiedAfter time.Time) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		if strings.HasSuffix(strings.ToLower(path), ".deb") {
			if !modifiedAfter.IsZero() && info.ModTime().Before(modifiedAfter) {
				return nil, nil // Skip files older than last sync
			}
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
			if !modifiedAfter.IsZero() && info.ModTime().Before(modifiedAfter) {
				return nil // Skip files older than last sync
			}
			files = append(files, p)
		}
		return nil
	})

	return files, err
}

func processDebFile(c *cache.Cache, path string, dryRun bool) (string, int64, error) {
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

	// In dry-run mode, just return the hash/size
	if dryRun {
		return hash, info.Size(), nil
	}

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

func printProgress(current, total, imported, skipped, failed int64) {
	width := 40
	pct := float64(current) / float64(total)
	filled := int(pct * float64(width))

	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	fmt.Printf("\r[%s] %3.0f%% (%d/%d) | ✓%d ○%d ✗%d",
		bar, pct*100, current, total, imported, skipped, failed)
}

func loadSyncState(stateFile, sourcePath string) time.Time {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return time.Time{}
	}

	var state syncState
	if err := json.Unmarshal(data, &state); err != nil {
		return time.Time{}
	}

	// Only use state if it's for the same source path
	if state.Path != sourcePath {
		return time.Time{}
	}

	return state.LastSync
}

func saveSyncState(stateFile, sourcePath string) {
	state := syncState{
		LastSync: time.Now(),
		Path:     sourcePath,
	}

	data, err := json.Marshal(state)
	if err != nil {
		return
	}

	_ = os.WriteFile(stateFile, data, 0600)
}
