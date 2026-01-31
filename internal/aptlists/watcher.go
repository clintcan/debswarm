// Package aptlists provides functionality to parse and watch APT's local package lists.
// This enables debswarm to populate its package index from APT's cached Packages files,
// allowing P2P downloads even when apt update doesn't go through the proxy.
package aptlists

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/index"
)

// DefaultAPTListsPath is the standard location for APT's package lists
const DefaultAPTListsPath = "/var/lib/apt/lists"

// Watcher watches APT's package lists directory and updates the index when files change
type Watcher struct {
	listsPath string
	index     *index.Index
	logger    *zap.Logger

	watcher *fsnotify.Watcher
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// Debounce rapid changes (APT writes multiple files during update)
	debounceTimer *time.Timer
	debounceMu    sync.Mutex
	pendingFiles  map[string]struct{}
}

// Config holds configuration for the APT lists watcher
type Config struct {
	// Path to APT lists directory (default: /var/lib/apt/lists)
	ListsPath string
	// Whether to watch for changes (default: true)
	WatchEnabled bool
}

// New creates a new APT lists watcher
func New(idx *index.Index, logger *zap.Logger, cfg *Config) *Watcher {
	path := DefaultAPTListsPath
	if cfg != nil && cfg.ListsPath != "" {
		path = cfg.ListsPath
	}

	return &Watcher{
		listsPath:    path,
		index:        idx,
		logger:       logger.Named("aptlists"),
		pendingFiles: make(map[string]struct{}),
	}
}

// Start begins watching the APT lists directory
// It performs an initial scan and then watches for changes
func (w *Watcher) Start(ctx context.Context) error {
	// Check if directory exists
	info, err := os.Stat(w.listsPath)
	if err != nil {
		if os.IsNotExist(err) {
			w.logger.Info("APT lists directory does not exist, skipping",
				zap.String("path", w.listsPath))
			return nil
		}
		return err
	}
	if !info.IsDir() {
		w.logger.Warn("APT lists path is not a directory",
			zap.String("path", w.listsPath))
		return nil
	}

	// Perform initial scan
	count, err := w.scanAll()
	if err != nil {
		w.logger.Warn("Failed to scan APT lists", zap.Error(err))
		// Continue anyway - watching might still work
	} else {
		w.logger.Info("Loaded APT package lists",
			zap.String("path", w.listsPath),
			zap.Int("filesScanned", count),
			zap.Int("totalPackages", w.index.Count()))
	}

	// Set up file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		w.logger.Warn("Failed to create file watcher, changes won't be detected",
			zap.Error(err))
		return nil // Not fatal - initial scan worked
	}
	w.watcher = watcher

	if err := watcher.Add(w.listsPath); err != nil {
		w.logger.Warn("Failed to watch APT lists directory",
			zap.String("path", w.listsPath),
			zap.Error(err))
		_ = watcher.Close()
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel

	w.wg.Add(1)
	go w.watchLoop(ctx)

	w.logger.Info("Watching APT lists for changes",
		zap.String("path", w.listsPath))

	return nil
}

// Stop stops the watcher
func (w *Watcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	if w.watcher != nil {
		_ = w.watcher.Close()
	}
	w.wg.Wait()
}

// scanAll scans all Packages files in the APT lists directory
func (w *Watcher) scanAll() (int, error) {
	entries, err := os.ReadDir(w.listsPath)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if w.isPackagesFile(entry.Name()) {
			if err := w.parseFile(filepath.Join(w.listsPath, entry.Name())); err != nil {
				w.logger.Debug("Failed to parse packages file",
					zap.String("file", entry.Name()),
					zap.Error(err))
				continue
			}
			count++
		}
	}

	return count, nil
}

// isPackagesFile returns true if the filename looks like a Packages file
// APT stores them as: {origin}_{path}_Packages or {origin}_{path}_Packages.{compression}
func (w *Watcher) isPackagesFile(name string) bool {
	// Skip partial downloads
	if strings.HasSuffix(name, ".partial") {
		return false
	}

	// Match *_Packages, *_Packages.gz, *_Packages.xz, *_Packages.lz4
	base := name
	for _, ext := range []string{".gz", ".xz", ".lz4", ".bz2"} {
		base = strings.TrimSuffix(base, ext)
	}

	return strings.HasSuffix(base, "_Packages") || strings.Contains(base, "_binary-")
}

// parseFile parses a single Packages file and adds it to the index
func (w *Watcher) parseFile(path string) error {
	// Extract repo identifier from filename
	// Format: archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages.gz
	// -> archive.ubuntu.com/ubuntu
	repo := w.extractRepoFromFilename(filepath.Base(path))

	return w.index.LoadFromFileWithRepo(path, repo)
}

// extractRepoFromFilename extracts the repository base from an APT list filename
func (w *Watcher) extractRepoFromFilename(filename string) string {
	// Remove compression extension
	name := filename
	for _, ext := range []string{".gz", ".xz", ".lz4", ".bz2"} {
		name = strings.TrimSuffix(name, ext)
	}

	// Find _dists_ or _pool_ to locate the boundary
	for _, marker := range []string{"_dists_", "_pool_"} {
		if idx := strings.Index(name, marker); idx != -1 {
			// Convert underscores back to slashes for the repo part
			repo := strings.ReplaceAll(name[:idx], "_", "/")
			return repo
		}
	}

	// Fallback: take everything before first underscore as host
	if idx := strings.Index(name, "_"); idx != -1 {
		return strings.ReplaceAll(name[:idx], "_", "/")
	}

	return filename
}

// watchLoop handles file system events
func (w *Watcher) watchLoop(ctx context.Context) {
	defer w.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// Only care about writes and creates for Packages files
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			name := filepath.Base(event.Name)
			if !w.isPackagesFile(name) {
				continue
			}

			// Debounce: APT writes many files during update
			w.scheduleReparse(event.Name)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.logger.Warn("File watcher error", zap.Error(err))
		}
	}
}

// scheduleReparse schedules a file to be reparsed after a debounce delay
func (w *Watcher) scheduleReparse(path string) {
	w.debounceMu.Lock()
	defer w.debounceMu.Unlock()

	w.pendingFiles[path] = struct{}{}

	// Reset or create debounce timer
	if w.debounceTimer != nil {
		w.debounceTimer.Stop()
	}

	w.debounceTimer = time.AfterFunc(2*time.Second, func() {
		w.processPendingFiles()
	})
}

// processPendingFiles processes all pending file changes
func (w *Watcher) processPendingFiles() {
	w.debounceMu.Lock()
	files := make([]string, 0, len(w.pendingFiles))
	for path := range w.pendingFiles {
		files = append(files, path)
	}
	w.pendingFiles = make(map[string]struct{})
	w.debounceMu.Unlock()

	if len(files) == 0 {
		return
	}

	w.logger.Debug("Processing APT list updates", zap.Int("files", len(files)))

	for _, path := range files {
		if err := w.parseFile(path); err != nil {
			w.logger.Debug("Failed to parse updated packages file",
				zap.String("file", filepath.Base(path)),
				zap.Error(err))
			continue
		}
	}

	w.logger.Info("Updated package index from APT lists",
		zap.Int("filesUpdated", len(files)),
		zap.Int("totalPackages", w.index.Count()))
}
