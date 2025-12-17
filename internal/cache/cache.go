// Package cache provides content-addressed storage for .deb packages
package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	_ "modernc.org/sqlite"

	"github.com/debswarm/debswarm/internal/sanitize"
)

var (
	ErrNotFound              = errors.New("package not found in cache")
	ErrHashMismatch          = errors.New("hash mismatch")
	ErrInsufficientDiskSpace = errors.New("insufficient disk space")
	ErrDatabaseCorrupted     = errors.New("database corrupted")
)

// Package represents a cached package entry
type Package struct {
	SHA256       string
	Size         int64
	Filename     string
	AddedAt      time.Time
	LastAccessed time.Time
	AccessCount  int64
	Announced    time.Time
}

// Cache manages local package storage
type Cache struct {
	basePath     string
	maxSize      int64
	minFreeSpace int64 // Minimum free disk space to maintain
	db           *sql.DB
	mu           sync.RWMutex
	logger       *zap.Logger
	currentSize  int64

	// Track active readers to prevent deletion during read
	activeReaders   map[string]int
	activeReadersMu sync.Mutex
}

// New creates a new cache instance
func New(basePath string, maxSize int64, logger *zap.Logger) (*Cache, error) {
	return NewWithMinFreeSpace(basePath, maxSize, 0, logger)
}

// NewWithMinFreeSpace creates a new cache instance with minimum free space enforcement
func NewWithMinFreeSpace(basePath string, maxSize int64, minFreeSpace int64, logger *zap.Logger) (*Cache, error) {
	// Create directory structure
	packagesDir := filepath.Join(basePath, "packages", "sha256")
	pendingDir := filepath.Join(basePath, "packages", "pending")
	indicesDir := filepath.Join(basePath, "indices")

	for _, dir := range []string{packagesDir, pendingDir, indicesDir} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Open database with corruption detection
	dbPath := filepath.Join(basePath, "state.db")
	db, err := openDatabaseWithRecovery(dbPath, logger)
	if err != nil {
		return nil, err
	}

	// Create tables
	if err := createTables(db); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("failed to create tables: %w (also failed to close db: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	c := &Cache{
		basePath:      basePath,
		maxSize:       maxSize,
		minFreeSpace:  minFreeSpace,
		db:            db,
		logger:        logger,
		activeReaders: make(map[string]int),
	}

	// Calculate current size
	if err := c.calculateSize(); err != nil {
		logger.Warn("Failed to calculate cache size", zap.Error(err))
	}

	if minFreeSpace > 0 {
		logger.Info("Cache minimum free space enforcement enabled",
			zap.String("minFreeSpace", formatBytes(minFreeSpace)))
	}

	return c, nil
}

// openDatabaseWithRecovery opens the SQLite database with corruption detection and recovery.
// If the database is corrupted, it attempts to back it up and create a fresh database.
func openDatabaseWithRecovery(dbPath string, logger *zap.Logger) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Run integrity check
	corrupted, checkErr := isDatabaseCorrupted(db)
	if checkErr != nil {
		// Can't determine integrity - try to proceed but warn
		logger.Warn("Could not verify database integrity", zap.Error(checkErr))
	} else if corrupted {
		logger.Error("Database corruption detected, attempting recovery",
			zap.String("path", dbPath))

		// Close the corrupted database
		if closeErr := db.Close(); closeErr != nil {
			logger.Warn("Failed to close corrupted database", zap.Error(closeErr))
		}

		// Attempt recovery
		db, err = recoverDatabase(dbPath, logger)
		if err != nil {
			return nil, fmt.Errorf("%w: recovery failed: %v", ErrDatabaseCorrupted, err)
		}
	}

	return db, nil
}

// isDatabaseCorrupted runs SQLite integrity check and returns true if database is corrupted
func isDatabaseCorrupted(db *sql.DB) (bool, error) {
	rows, err := db.Query("PRAGMA integrity_check")
	if err != nil {
		return false, fmt.Errorf("integrity check failed: %w", err)
	}
	defer rows.Close()

	var result string
	if rows.Next() {
		if err := rows.Scan(&result); err != nil {
			return false, fmt.Errorf("failed to read integrity check result: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("integrity check iteration error: %w", err)
	}

	// "ok" means no corruption found
	return result != "ok", nil
}

// recoverDatabase backs up the corrupted database and creates a fresh one.
// Cached package files on disk are preserved; only metadata is lost.
func recoverDatabase(dbPath string, logger *zap.Logger) (*sql.DB, error) {
	// Create backup with timestamp
	backupPath := dbPath + fmt.Sprintf(".corrupted.%d", time.Now().Unix())
	if err := os.Rename(dbPath, backupPath); err != nil {
		return nil, fmt.Errorf("failed to backup corrupted database: %w", err)
	}
	logger.Info("Backed up corrupted database",
		zap.String("backup", backupPath))

	// Also backup WAL and SHM files if they exist
	for _, suffix := range []string{"-wal", "-shm"} {
		walPath := dbPath + suffix
		if _, err := os.Stat(walPath); err == nil {
			if err := os.Rename(walPath, backupPath+suffix); err != nil {
				logger.Warn("Failed to backup WAL/SHM file", zap.String("file", walPath), zap.Error(err))
			}
		}
	}

	// Create fresh database
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to create new database: %w", err)
	}

	logger.Warn("Created fresh database after corruption recovery",
		zap.String("note", "Package files preserved on disk; run 'debswarm cache rebuild' to restore metadata"))

	return db, nil
}

// CheckIntegrity verifies database integrity and returns any errors found.
// This can be called periodically or on-demand to detect issues early.
func (c *Cache) CheckIntegrity() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	corrupted, err := isDatabaseCorrupted(c.db)
	if err != nil {
		return fmt.Errorf("integrity check failed: %w", err)
	}
	if corrupted {
		return ErrDatabaseCorrupted
	}
	return nil
}

// formatBytes formats bytes as human-readable string
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func createTables(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS packages (
			sha256 TEXT PRIMARY KEY,
			size INTEGER NOT NULL,
			filename TEXT NOT NULL,
			added_at INTEGER NOT NULL,
			last_accessed INTEGER NOT NULL,
			access_count INTEGER DEFAULT 1,
			announced INTEGER DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS indices (
			url TEXT PRIMARY KEY,
			etag TEXT,
			last_modified TEXT,
			fetched_at INTEGER NOT NULL,
			path TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS downloads (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			expected_size INTEGER NOT NULL,
			completed_size INTEGER DEFAULT 0,
			chunk_size INTEGER NOT NULL,
			total_chunks INTEGER NOT NULL,
			status TEXT DEFAULT 'pending',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			error TEXT,
			retry_count INTEGER DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS download_chunks (
			download_id TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			start_offset INTEGER NOT NULL,
			end_offset INTEGER NOT NULL,
			status TEXT DEFAULT 'pending',
			completed_at INTEGER,
			PRIMARY KEY (download_id, chunk_index),
			FOREIGN KEY (download_id) REFERENCES downloads(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_packages_last_accessed
		ON packages(last_accessed);

		CREATE INDEX IF NOT EXISTS idx_packages_announced
		ON packages(announced);

		CREATE INDEX IF NOT EXISTS idx_downloads_status
		ON downloads(status);

		CREATE INDEX IF NOT EXISTS idx_download_chunks_status
		ON download_chunks(download_id, status);
	`)
	if err != nil {
		return err
	}

	// Migration: Add retry_count column if it doesn't exist (for existing databases)
	_, _ = db.Exec(`ALTER TABLE downloads ADD COLUMN retry_count INTEGER DEFAULT 0`)

	return nil
}

// Has checks if a package with the given hash exists in the cache
func (c *Cache) Has(sha256Hash string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	path := c.packagePath(sha256Hash)
	_, err := os.Stat(path)
	return err == nil
}

// trackedReader wraps a file and decrements reader count on close
type trackedReader struct {
	file   *os.File
	hash   string
	cache  *Cache
	closed bool
}

func (tr *trackedReader) Read(p []byte) (n int, err error) {
	return tr.file.Read(p)
}

func (tr *trackedReader) Close() error {
	if tr.closed {
		return nil
	}
	tr.closed = true
	err := tr.file.Close()

	tr.cache.activeReadersMu.Lock()
	tr.cache.activeReaders[tr.hash]--
	if tr.cache.activeReaders[tr.hash] <= 0 {
		delete(tr.cache.activeReaders, tr.hash)
	}
	tr.cache.activeReadersMu.Unlock()

	return err
}

// Get retrieves a package from the cache
func (c *Cache) Get(sha256Hash string) (io.ReadCloser, *Package, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	path := c.packagePath(sha256Hash)

	// Use a single critical section for file open and reader tracking
	// to prevent TOCTOU race conditions
	c.activeReadersMu.Lock()
	f, err := os.Open(path)
	if err != nil {
		c.activeReadersMu.Unlock()
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	c.activeReaders[sha256Hash]++
	c.activeReadersMu.Unlock()

	// Update access time and count
	now := time.Now().Unix()
	_, err = c.db.Exec(`
		UPDATE packages
		SET last_accessed = ?, access_count = access_count + 1
		WHERE sha256 = ?`,
		now, sha256Hash)
	if err != nil {
		c.logger.Warn("Failed to update access time", zap.Error(err))
	}

	// Get package info
	pkg, err := c.getPackageInfo(sha256Hash)
	if err != nil {
		if closeErr := f.Close(); closeErr != nil {
			c.logger.Warn("Failed to close file during cleanup", zap.Error(closeErr))
		}
		c.activeReadersMu.Lock()
		c.activeReaders[sha256Hash]--
		if c.activeReaders[sha256Hash] <= 0 {
			delete(c.activeReaders, sha256Hash)
		}
		c.activeReadersMu.Unlock()
		return nil, nil, err
	}

	return &trackedReader{file: f, hash: sha256Hash, cache: c}, pkg, nil
}

// Put stores a package in the cache
func (c *Cache) Put(data io.Reader, expectedHash string, filename string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Write to temporary file while computing hash
	pendingPath := filepath.Join(c.basePath, "packages", "pending", expectedHash)
	f, err := os.Create(pendingPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)

	size, err := io.Copy(writer, data)
	if err != nil {
		if closeErr := f.Close(); closeErr != nil {
			c.logger.Warn("Failed to close file during cleanup", zap.Error(closeErr))
		}
		if removeErr := os.Remove(pendingPath); removeErr != nil {
			c.logger.Warn("Failed to remove pending file during cleanup", zap.Error(removeErr))
		}
		return fmt.Errorf("failed to write data: %w", err)
	}
	if closeErr := f.Close(); closeErr != nil {
		if removeErr := os.Remove(pendingPath); removeErr != nil {
			c.logger.Warn("Failed to remove pending file during cleanup", zap.Error(removeErr))
		}
		return fmt.Errorf("failed to close file: %w", closeErr)
	}

	// Verify hash
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != expectedHash {
		if removeErr := os.Remove(pendingPath); removeErr != nil {
			c.logger.Warn("Failed to remove pending file during cleanup", zap.Error(removeErr))
		}
		return fmt.Errorf("%w: expected %s, got %s", ErrHashMismatch, expectedHash, actualHash)
	}

	// Ensure we have space
	if spaceErr := c.ensureSpace(size); spaceErr != nil {
		if removeErr := os.Remove(pendingPath); removeErr != nil {
			c.logger.Warn("Failed to remove pending file during cleanup", zap.Error(removeErr))
		}
		return spaceErr
	}

	// Move to final location
	finalPath := c.packagePath(expectedHash)
	if mkdirErr := os.MkdirAll(filepath.Dir(finalPath), 0750); mkdirErr != nil {
		if removeErr := os.Remove(pendingPath); removeErr != nil {
			c.logger.Warn("Failed to remove pending file during cleanup", zap.Error(removeErr))
		}
		return mkdirErr
	}

	if renameErr := os.Rename(pendingPath, finalPath); renameErr != nil {
		if removeErr := os.Remove(pendingPath); removeErr != nil {
			c.logger.Warn("Failed to remove pending file during cleanup", zap.Error(removeErr))
		}
		return renameErr
	}

	// Record in database - use ON CONFLICT to preserve access_count if re-adding
	now := time.Now().Unix()
	_, err = c.db.Exec(`
		INSERT INTO packages
		(sha256, size, filename, added_at, last_accessed, access_count, announced)
		VALUES (?, ?, ?, ?, ?, 1, 0)
		ON CONFLICT(sha256) DO UPDATE SET
			size = excluded.size,
			filename = excluded.filename,
			last_accessed = excluded.last_accessed,
			access_count = access_count + 1`,
		expectedHash, size, filename, now, now)
	if err != nil {
		return fmt.Errorf("failed to record package: %w", err)
	}

	c.currentSize += size
	c.logger.Debug("Cached package",
		zap.String("hash", expectedHash[:16]+"..."),
		zap.Int64("size", size),
		zap.String("filename", sanitize.Filename(filename)))

	return nil
}

// PutFile stores a pre-verified file in the cache by moving it.
// The file at filePath must already have been verified (correct hash).
// This is more efficient than Put() for large files as it avoids copying.
func (c *Cache) PutFile(filePath string, hash string, filename string, size int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Ensure we have space
	if err := c.ensureSpace(size); err != nil {
		return err
	}

	// Move to final location
	finalPath := c.packagePath(hash)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0750); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	if err := os.Rename(filePath, finalPath); err != nil {
		return fmt.Errorf("failed to move file to cache: %w", err)
	}

	// Record in database - use ON CONFLICT to preserve access_count if re-adding
	now := time.Now().Unix()
	_, err := c.db.Exec(`
		INSERT INTO packages
		(sha256, size, filename, added_at, last_accessed, access_count, announced)
		VALUES (?, ?, ?, ?, ?, 1, 0)
		ON CONFLICT(sha256) DO UPDATE SET
			size = excluded.size,
			filename = excluded.filename,
			last_accessed = excluded.last_accessed,
			access_count = access_count + 1`,
		hash, size, filename, now, now)
	if err != nil {
		return fmt.Errorf("failed to record package: %w", err)
	}

	c.currentSize += size
	c.logger.Debug("Cached package (file move)",
		zap.String("hash", hash[:16]+"..."),
		zap.Int64("size", size),
		zap.String("filename", sanitize.Filename(filename)))

	return nil
}

// Delete removes a package from the cache
func (c *Cache) Delete(sha256Hash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.deleteUnlocked(sha256Hash)
}

var ErrFileInUse = errors.New("file is currently being read")

func (c *Cache) deleteUnlocked(sha256Hash string) error {
	// Check if file is currently being read
	c.activeReadersMu.Lock()
	readers := c.activeReaders[sha256Hash]
	c.activeReadersMu.Unlock()

	if readers > 0 {
		return ErrFileInUse
	}

	// Get size before deleting
	var size int64
	err := c.db.QueryRow("SELECT size FROM packages WHERE sha256 = ?", sha256Hash).Scan(&size)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	// Delete file
	path := c.packagePath(sha256Hash)
	if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
		return removeErr
	}

	// Delete from database
	_, err = c.db.Exec("DELETE FROM packages WHERE sha256 = ?", sha256Hash)
	if err != nil {
		return err
	}

	c.currentSize -= size
	return nil
}

// List returns all cached packages
func (c *Cache) List() ([]*Package, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	rows, err := c.db.Query(`
		SELECT sha256, size, filename, added_at, last_accessed, access_count, announced 
		FROM packages 
		ORDER BY last_accessed DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var packages []*Package
	for rows.Next() {
		pkg := &Package{}
		var addedAt, lastAccessed, announced int64
		err := rows.Scan(
			&pkg.SHA256, &pkg.Size, &pkg.Filename,
			&addedAt, &lastAccessed, &pkg.AccessCount, &announced)
		if err != nil {
			return nil, err
		}
		pkg.AddedAt = time.Unix(addedAt, 0)
		pkg.LastAccessed = time.Unix(lastAccessed, 0)
		pkg.Announced = time.Unix(announced, 0)
		packages = append(packages, pkg)
	}

	return packages, rows.Err()
}

// GetUnannounced returns packages that need to be announced to the DHT
func (c *Cache) GetUnannounced() ([]*Package, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	threshold := time.Now().Add(-12 * time.Hour).Unix()
	rows, err := c.db.Query(`
		SELECT sha256, size, filename, added_at, last_accessed, access_count, announced 
		FROM packages 
		WHERE announced < ?`, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var packages []*Package
	for rows.Next() {
		pkg := &Package{}
		var addedAt, lastAccessed, announced int64
		err := rows.Scan(
			&pkg.SHA256, &pkg.Size, &pkg.Filename,
			&addedAt, &lastAccessed, &pkg.AccessCount, &announced)
		if err != nil {
			return nil, err
		}
		pkg.AddedAt = time.Unix(addedAt, 0)
		pkg.LastAccessed = time.Unix(lastAccessed, 0)
		pkg.Announced = time.Unix(announced, 0)
		packages = append(packages, pkg)
	}

	return packages, rows.Err()
}

// MarkAnnounced updates the announced timestamp for a package
func (c *Cache) MarkAnnounced(sha256Hash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.db.Exec(
		"UPDATE packages SET announced = ? WHERE sha256 = ?",
		time.Now().Unix(), sha256Hash)
	return err
}

// Size returns the current cache size in bytes
func (c *Cache) Size() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.currentSize
}

// Count returns the number of cached packages
func (c *Cache) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var count int
	_ = c.db.QueryRow("SELECT COUNT(*) FROM packages").Scan(&count)
	return count
}

// Close closes the cache database
func (c *Cache) Close() error {
	return c.db.Close()
}

func (c *Cache) packagePath(sha256Hash string) string {
	// Use first 2 chars as subdirectory for better filesystem performance
	return filepath.Join(c.basePath, "packages", "sha256", sha256Hash[:2], sha256Hash)
}

func (c *Cache) getPackageInfo(sha256Hash string) (*Package, error) {
	pkg := &Package{}
	var addedAt, lastAccessed, announced int64

	err := c.db.QueryRow(`
		SELECT sha256, size, filename, added_at, last_accessed, access_count, announced 
		FROM packages WHERE sha256 = ?`, sha256Hash).Scan(
		&pkg.SHA256, &pkg.Size, &pkg.Filename,
		&addedAt, &lastAccessed, &pkg.AccessCount, &announced)
	if err != nil {
		return nil, err
	}

	pkg.AddedAt = time.Unix(addedAt, 0)
	pkg.LastAccessed = time.Unix(lastAccessed, 0)
	pkg.Announced = time.Unix(announced, 0)
	return pkg, nil
}

func (c *Cache) calculateSize() error {
	var total int64
	err := c.db.QueryRow("SELECT COALESCE(SUM(size), 0) FROM packages").Scan(&total)
	if err != nil {
		return err
	}
	c.currentSize = total
	return nil
}

var ErrCacheFull = errors.New("cache full: unable to free enough space")

func (c *Cache) ensureSpace(needed int64) error {
	// Check minimum free space constraint first
	if c.minFreeSpace > 0 {
		freeSpace, err := c.getDiskFreeSpace()
		if err != nil {
			c.logger.Warn("Failed to check disk free space", zap.Error(err))
		} else if freeSpace-needed < c.minFreeSpace {
			return fmt.Errorf("%w: need %s but only %s available (min free: %s)",
				ErrInsufficientDiskSpace,
				formatBytes(needed),
				formatBytes(freeSpace),
				formatBytes(c.minFreeSpace))
		}
	}

	if c.currentSize+needed <= c.maxSize {
		return nil
	}

	// Get packages sorted by eviction score (oldest, least accessed first)
	// Uses last_accessed adjusted by access_count - more accesses = higher score = evicted later
	// SQLite doesn't support LOG, so we use a simpler linear formula
	rows, err := c.db.Query(`
		SELECT sha256, size
		FROM packages
		WHERE last_accessed < ?
		ORDER BY (last_accessed + access_count * 3600) ASC`,
		time.Now().Add(-7*24*time.Hour).Unix()) // Don't evict recently accessed
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() && c.currentSize+needed > c.maxSize {
		var hash string
		var size int64
		if err := rows.Scan(&hash, &size); err != nil {
			continue
		}

		c.logger.Debug("Evicting package",
			zap.String("hash", hash[:16]+"..."),
			zap.Int64("size", size))

		if err := c.deleteUnlocked(hash); err != nil {
			// Log but continue - file might be in use, try next candidate
			c.logger.Warn("Failed to evict package", zap.Error(err))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating eviction candidates: %w", err)
	}

	// Check if we freed enough space
	if c.currentSize+needed > c.maxSize {
		return ErrCacheFull
	}

	return nil
}

// GetDB returns the underlying database connection
// Used by downloader for state persistence
func (c *Cache) GetDB() *sql.DB {
	return c.db
}

// PartialDir returns the directory for partial downloads
func (c *Cache) PartialDir(hash string) string {
	return filepath.Join(c.basePath, "packages", "partial", hash)
}

// EnsurePartialDir creates the partial download directory for a hash
func (c *Cache) EnsurePartialDir(hash string) error {
	dir := c.PartialDir(hash)
	return os.MkdirAll(dir, 0750)
}

// CleanPartialDir removes the partial download directory for a hash
func (c *Cache) CleanPartialDir(hash string) error {
	dir := c.PartialDir(hash)
	return os.RemoveAll(dir)
}

// BasePath returns the cache base path
func (c *Cache) BasePath() string {
	return c.basePath
}
