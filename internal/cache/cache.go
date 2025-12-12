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

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

var (
	ErrNotFound     = errors.New("package not found in cache")
	ErrHashMismatch = errors.New("hash mismatch")
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
	basePath    string
	maxSize     int64
	db          *sql.DB
	mu          sync.RWMutex
	logger      *zap.Logger
	currentSize int64

	// Track active readers to prevent deletion during read
	activeReaders   map[string]int
	activeReadersMu sync.Mutex
}

// New creates a new cache instance
func New(basePath string, maxSize int64, logger *zap.Logger) (*Cache, error) {
	// Create directory structure
	packagesDir := filepath.Join(basePath, "packages", "sha256")
	pendingDir := filepath.Join(basePath, "packages", "pending")
	indicesDir := filepath.Join(basePath, "indices")

	for _, dir := range []string{packagesDir, pendingDir, indicesDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Open database
	dbPath := filepath.Join(basePath, "state.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Create tables
	if err := createTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	c := &Cache{
		basePath:      basePath,
		maxSize:       maxSize,
		db:            db,
		logger:        logger,
		activeReaders: make(map[string]int),
	}

	// Calculate current size
	if err := c.calculateSize(); err != nil {
		logger.Warn("Failed to calculate cache size", zap.Error(err))
	}

	return c, nil
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

		CREATE INDEX IF NOT EXISTS idx_packages_last_accessed 
		ON packages(last_accessed);
		
		CREATE INDEX IF NOT EXISTS idx_packages_announced 
		ON packages(announced);
	`)
	return err
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
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}

	// Track active reader to prevent deletion during read
	c.activeReadersMu.Lock()
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
		f.Close()
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
		f.Close()
		os.Remove(pendingPath)
		return fmt.Errorf("failed to write data: %w", err)
	}
	f.Close()

	// Verify hash
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != expectedHash {
		os.Remove(pendingPath)
		return fmt.Errorf("%w: expected %s, got %s", ErrHashMismatch, expectedHash, actualHash)
	}

	// Ensure we have space
	if err := c.ensureSpace(size); err != nil {
		os.Remove(pendingPath)
		return err
	}

	// Move to final location
	finalPath := c.packagePath(expectedHash)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0755); err != nil {
		os.Remove(pendingPath)
		return err
	}

	if err := os.Rename(pendingPath, finalPath); err != nil {
		os.Remove(pendingPath)
		return err
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
		zap.String("filename", filename))

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
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
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

	// Check if we freed enough space
	if c.currentSize+needed > c.maxSize {
		return ErrCacheFull
	}

	return nil
}
