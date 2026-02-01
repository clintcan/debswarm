// Package aptarchives provides functionality to import packages from APT's local cache.
// This enables debswarm to pre-populate its cache with packages the user already has,
// making them immediately available for P2P sharing.
package aptarchives

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/hashutil"
	"github.com/debswarm/debswarm/internal/index"
)

// DefaultAPTArchivesPath is the standard location for APT's package cache
const DefaultAPTArchivesPath = "/var/cache/apt/archives"

// Importer imports packages from APT's local cache into debswarm's cache
type Importer struct {
	archivesPath string
	cache        *cache.Cache
	index        *index.Index
	logger       *zap.Logger
}

// Config holds configuration for the APT archives importer
type Config struct {
	// Path to APT archives directory (default: /var/cache/apt/archives)
	ArchivesPath string
}

// ImportResult contains statistics from an import operation
type ImportResult struct {
	Scanned    int // Total .deb files found
	Imported   int // Successfully imported to cache
	Skipped    int // Already in cache
	Unverified int // Not in index (hash unknown)
	Errors     int // Failed to import
}

// New creates a new APT archives importer
func New(c *cache.Cache, idx *index.Index, logger *zap.Logger, cfg *Config) *Importer {
	path := DefaultAPTArchivesPath
	if cfg != nil && cfg.ArchivesPath != "" {
		path = cfg.ArchivesPath
	}

	return &Importer{
		archivesPath: path,
		cache:        c,
		index:        idx,
		logger:       logger.Named("aptarchives"),
	}
}

// Import scans the APT archives directory and imports packages into debswarm's cache.
// It only imports packages that:
// - Are not already in the cache
// - Have a known hash in the index (for verification)
func (i *Importer) Import(ctx context.Context) (*ImportResult, error) {
	result := &ImportResult{}

	// Check if directory exists
	info, err := os.Stat(i.archivesPath)
	if err != nil {
		if os.IsNotExist(err) {
			i.logger.Info("APT archives directory does not exist, skipping",
				zap.String("path", i.archivesPath))
			return result, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		i.logger.Warn("APT archives path is not a directory",
			zap.String("path", i.archivesPath))
		return result, nil
	}

	// Scan directory
	entries, err := os.ReadDir(i.archivesPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// Skip directories (especially "partial")
		if entry.IsDir() {
			continue
		}

		// Only process .deb files
		name := entry.Name()
		if !strings.HasSuffix(name, ".deb") {
			continue
		}

		result.Scanned++

		// Import the package
		status := i.importPackage(filepath.Join(i.archivesPath, name))
		switch status {
		case statusImported:
			result.Imported++
		case statusSkipped:
			result.Skipped++
		case statusUnverified:
			result.Unverified++
		case statusError:
			result.Errors++
		}
	}

	i.logger.Info("Imported APT archives",
		zap.String("path", i.archivesPath),
		zap.Int("scanned", result.Scanned),
		zap.Int("imported", result.Imported),
		zap.Int("skipped", result.Skipped),
		zap.Int("unverified", result.Unverified),
		zap.Int("errors", result.Errors))

	return result, nil
}

type importStatus int

const (
	statusImported importStatus = iota
	statusSkipped
	statusUnverified
	statusError
)

// importPackage attempts to import a single .deb file
func (i *Importer) importPackage(path string) importStatus {
	filename := filepath.Base(path)

	// Get file info for size
	info, err := os.Stat(path)
	if err != nil {
		i.logger.Debug("Failed to stat file",
			zap.String("file", filename),
			zap.Error(err))
		return statusError
	}

	// Compute hash
	hash, err := i.computeHash(path)
	if err != nil {
		i.logger.Debug("Failed to compute hash",
			zap.String("file", filename),
			zap.Error(err))
		return statusError
	}

	// Check if already in cache
	if i.cache.Has(hash) {
		i.logger.Debug("Package already in cache",
			zap.String("file", filename),
			zap.String("hash", hash[:16]+"..."))
		return statusSkipped
	}

	// Look up in index to verify this is a known package
	pkg := i.index.GetBySHA256(hash)
	if pkg == nil {
		// Also try looking up by basename (may have different hash in index)
		pkg = i.index.GetByPath(filename)
		if pkg == nil || pkg.SHA256 != hash {
			i.logger.Debug("Package not in index or hash mismatch, skipping",
				zap.String("file", filename),
				zap.String("hash", hash[:16]+"..."))
			return statusUnverified
		}
	}

	// Import by opening and passing to cache.Put
	// #nosec G304 -- path is constructed from configured directory + filename from os.ReadDir, not user input
	f, err := os.Open(path)
	if err != nil {
		i.logger.Debug("Failed to open file",
			zap.String("file", filename),
			zap.Error(err))
		return statusError
	}
	defer f.Close()

	// Use the filename from the index if available, otherwise use local filename
	cacheFilename := filename
	if pkg.Filename != "" {
		cacheFilename = pkg.Filename
	}

	if err := i.cache.Put(f, hash, cacheFilename); err != nil {
		i.logger.Debug("Failed to import package",
			zap.String("file", filename),
			zap.Error(err))
		return statusError
	}

	i.logger.Debug("Imported package from APT archives",
		zap.String("file", filename),
		zap.String("hash", hash[:16]+"..."),
		zap.Int64("size", info.Size()))

	return statusImported
}

// computeHash computes the SHA256 hash of a file
func (i *Importer) computeHash(path string) (string, error) {
	// #nosec G304 -- path is constructed from configured directory + filename from os.ReadDir, not user input
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	return hashutil.HashReader(f)
}
