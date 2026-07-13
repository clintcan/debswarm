package cache

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

	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/hashutil"
)

// MetadataEntry describes a cached repository metadata file (a Release,
// InRelease, Packages, Sources, Translation, Contents, or DEP-11 file). Unlike
// packages these are addressed by URL and revalidated against the mirror with
// their stored HTTP validators; the SHA256 content cache is untouched.
type MetadataEntry struct {
	URL           string
	ETag          string
	LastModified  string
	Size          int64
	ContentType   string
	FetchedAt     time.Time
	LastValidated time.Time
}

// SetMetadataMaxSize sets the disk budget (bytes) for the metadata cache. A
// value <= 0 disables metadata caching entirely: Get/Put become no-ops. It is
// called once at startup (like SetOnEvict) before the cache serves traffic.
func (c *Cache) SetMetadataMaxSize(n int64) {
	c.mu.Lock()
	c.metadataMaxSize = n
	c.mu.Unlock()
}

// SetOnMetadataEvict registers a callback invoked once per evicted metadata
// entry (mirrors SetOnEvict). Called with the cache lock held.
func (c *Cache) SetOnMetadataEvict(fn func()) {
	c.mu.Lock()
	c.onMetadataEvict = fn
	c.mu.Unlock()
}

// MetadataEnabled reports whether metadata caching is on (budget > 0).
func (c *Cache) MetadataEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metadataMaxSize > 0
}

// MetadataSize returns the current on-disk metadata cache size in bytes.
func (c *Cache) MetadataSize() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metadataSize
}

// MetadataCount returns the number of cached metadata files.
func (c *Cache) MetadataCount() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var n int64
	_ = c.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM indices").Scan(&n)
	return n
}

// metadataPath maps a URL to its on-disk location, sharded by the first two
// hex chars of sha256(url) so a directory never holds the whole cache.
func (c *Cache) metadataPath(url string) string {
	sum := sha256.Sum256([]byte(url))
	key := hex.EncodeToString(sum[:])
	return filepath.Join(c.basePath, "indices", key[:2], key)
}

// byHashSHA256 extracts the content hash from an APT by-hash URL of the form
// .../by-hash/SHA256/<64-hex>. by-hash files are immutable and self-describing,
// so the hash lets us verify on store and skip revalidation on read. Only
// SHA256 is recognized; other digests (SHA512/MD5Sum) return ("", false) and
// take the normal revalidated path.
func byHashSHA256(rawURL string) (string, bool) {
	const marker = "/by-hash/sha256/"
	lower := strings.ToLower(rawURL)
	i := strings.Index(lower, marker)
	if i < 0 {
		return "", false
	}
	rest := rawURL[i+len(marker):]
	// Stop at the next path or query separator.
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	if len(rest) != 64 {
		return "", false
	}
	rest = strings.ToLower(rest)
	for _, r := range rest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", false
		}
	}
	return rest, true
}

// IsImmutableMetadataURL reports whether a metadata URL is content-addressed
// (an APT by-hash/SHA256 URL) and therefore never needs upstream revalidation
// once cached. Callers use this to serve directly from cache without a
// conditional GET.
func IsImmutableMetadataURL(rawURL string) bool {
	_, ok := byHashSHA256(rawURL)
	return ok
}

// MetadataValidators returns the stored ETag and Last-Modified for a cached URL
// so the caller can issue a conditional GET upstream. ok is false when the URL
// is not cached (or caching is disabled).
func (c *Cache) MetadataValidators(url string) (etag, lastModified string, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.metadataMaxSize <= 0 {
		return "", "", false
	}
	var e, lm string
	err := c.db.QueryRowContext(context.Background(), "SELECT COALESCE(etag,''), COALESCE(last_modified,'') FROM indices WHERE url = ?", url).Scan(&e, &lm)
	if err != nil {
		return "", "", false
	}
	return e, lm, true
}

// ListMetadataURLs returns the URLs of every cached metadata file. It lets the
// proxy warm its in-memory package index from cached Packages indices after a
// restart (so a cached .deb resolves offline without a fresh apt-get update).
// Returns an empty slice when metadata caching is disabled.
func (c *Cache) ListMetadataURLs() ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.metadataMaxSize <= 0 {
		return nil, nil
	}
	rows, err := c.db.QueryContext(context.Background(), "SELECT url FROM indices")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var urls []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, rows.Err()
}

// HasMetadata reports whether a URL has a cached body on disk.
func (c *Cache) HasMetadata(url string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.metadataMaxSize <= 0 {
		return false
	}
	_, err := os.Stat(c.metadataPath(url))
	return err == nil
}

// GetMetadata returns a cached metadata entry and an open reader for its body.
// It returns ErrNotFound on a miss, when caching is disabled, or when the row
// exists but the file is gone (a self-healing miss: the stale row is dropped so
// the caller re-fetches). The caller must Close the returned reader.
func (c *Cache) GetMetadata(url string) (*MetadataEntry, io.ReadCloser, error) {
	c.mu.RLock()
	if c.metadataMaxSize <= 0 {
		c.mu.RUnlock()
		return nil, nil, ErrNotFound
	}

	entry := &MetadataEntry{URL: url}
	var fetchedAt, lastValidated int64
	err := c.db.QueryRowContext(context.Background(), `
		SELECT COALESCE(etag,''), COALESCE(last_modified,''), size,
		       COALESCE(content_type,''), fetched_at, last_validated
		FROM indices WHERE url = ?`, url).Scan(
		&entry.ETag, &entry.LastModified, &entry.Size,
		&entry.ContentType, &fetchedAt, &lastValidated)
	if err != nil {
		c.mu.RUnlock()
		return nil, nil, ErrNotFound
	}

	path := c.metadataPath(url)
	f, openErr := os.Open(path) //nolint:gosec // path derived from sha256(url), not user input
	c.mu.RUnlock()

	if openErr != nil {
		// Row without a file (interrupted write, manual deletion, or corruption
		// recovery). Treat as a miss and drop the row so the next fetch re-stores.
		c.dropMetadataRow(url)
		return nil, nil, ErrNotFound
	}

	entry.FetchedAt = time.Unix(fetchedAt, 0)
	entry.LastValidated = time.Unix(lastValidated, 0)
	c.touchMetadata(url)
	return entry, f, nil
}

// touchMetadata records an access for LRU ranking. Best-effort; a failed update
// only means slightly staler eviction ordering.
func (c *Cache) touchMetadata(url string) {
	_, _ = c.db.ExecContext(context.Background(),
		"UPDATE indices SET last_accessed = ?, access_count = access_count + 1 WHERE url = ?",
		time.Now().Unix(), url)
}

// RevalidateMetadata refreshes the stored validators and last_validated time for
// a URL after an upstream 304 confirmed the cached body is still current. It is
// a no-op if the URL is not cached.
func (c *Cache) RevalidateMetadata(url, etag, lastModified string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.metadataMaxSize <= 0 {
		return
	}
	now := time.Now().Unix()
	// Only overwrite a validator when upstream actually sent one, so a 304
	// without headers doesn't blank a previously-known ETag/Last-Modified.
	_, _ = c.db.ExecContext(context.Background(), `
		UPDATE indices
		SET etag = CASE WHEN ? <> '' THEN ? ELSE etag END,
		    last_modified = CASE WHEN ? <> '' THEN ? ELSE last_modified END,
		    last_validated = ?, last_accessed = ?
		WHERE url = ?`,
		etag, etag, lastModified, lastModified, now, now, url)
}

// dropMetadataRow removes just the DB row (used on self-healing misses).
func (c *Cache) dropMetadataRow(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var size int64
	if err := c.db.QueryRowContext(context.Background(), "SELECT size FROM indices WHERE url = ?", url).Scan(&size); err == nil {
		if _, err := c.db.ExecContext(context.Background(), "DELETE FROM indices WHERE url = ?", url); err == nil {
			c.metadataSize -= size
			if c.metadataSize < 0 {
				c.metadataSize = 0
			}
		}
	}
}

// MetadataWriter streams a metadata body to a pending file while the caller also
// writes it to the client (via io.MultiWriter), so large Contents/Packages files
// are never buffered in memory. Commit verifies (for by-hash URLs), evicts to
// fit the budget, and atomically installs the entry; Abort discards it.
type MetadataWriter struct {
	cache        *Cache
	url          string
	etag         string
	lastModified string
	contentType  string
	expectedHash string // from a by-hash URL; "" otherwise
	tmp          *os.File
	tmpPath      string
	hw           *hashutil.HashingWriter
	dst          io.Writer
	size         int64
	done         bool
}

// NewMetadataWriter begins a cached write for url. It returns (nil, nil) — a
// no-op writer sentinel is *not* used; callers must check MetadataEnabled first.
// The returned writer must be finished with exactly one of Commit or Abort.
func (c *Cache) NewMetadataWriter(url, etag, lastModified, contentType string) (*MetadataWriter, error) {
	pendingDir := filepath.Join(c.basePath, "indices", "pending")
	f, err := os.CreateTemp(pendingDir, "meta.*")
	if err != nil {
		return nil, fmt.Errorf("failed to create metadata temp file: %w", err)
	}
	mw := &MetadataWriter{
		cache:        c,
		url:          url,
		etag:         etag,
		lastModified: lastModified,
		contentType:  contentType,
		tmp:          f,
		tmpPath:      f.Name(),
		dst:          f,
	}
	if h, ok := byHashSHA256(url); ok {
		mw.expectedHash = h
		mw.hw = hashutil.NewHashingWriter(f)
		mw.dst = mw.hw
	}
	return mw, nil
}

// Write appends body bytes to the pending file.
func (mw *MetadataWriter) Write(p []byte) (int, error) {
	n, err := mw.dst.Write(p)
	mw.size += int64(n)
	return n, err
}

// Abort discards the pending write. Safe to call after Commit (no-op).
func (mw *MetadataWriter) Abort() {
	if mw.done {
		return
	}
	mw.done = true
	_ = mw.tmp.Close()
	if err := os.Remove(mw.tmpPath); err != nil && !os.IsNotExist(err) {
		mw.cache.logger.Warn("Failed to remove metadata temp file", zap.Error(err))
	}
}

// Commit verifies the by-hash digest (if any), makes room within the metadata
// budget, and atomically installs the entry. On any failure the pending file is
// cleaned up and the previously cached copy (if any) is left untouched.
func (mw *MetadataWriter) Commit() error {
	if mw.done {
		return fmt.Errorf("metadata writer already finished")
	}
	mw.done = true
	c := mw.cache

	if err := mw.tmp.Close(); err != nil {
		_ = os.Remove(mw.tmpPath)
		return fmt.Errorf("failed to close metadata temp file: %w", err)
	}

	if mw.expectedHash != "" {
		if got := mw.hw.Sum(); got != mw.expectedHash {
			_ = os.Remove(mw.tmpPath)
			return fmt.Errorf("%w: by-hash metadata %s != %s", ErrHashMismatch, got, mw.expectedHash)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.metadataMaxSize <= 0 {
		_ = os.Remove(mw.tmpPath)
		return nil // disabled between construction and commit; nothing to store
	}

	// A single file larger than the whole budget can never fit — don't thrash
	// eviction trying.
	if mw.size > c.metadataMaxSize {
		_ = os.Remove(mw.tmpPath)
		return ErrCacheFull
	}

	// Account for replacing an existing entry (its bytes free up).
	var oldSize int64
	haveOld := c.db.QueryRowContext(context.Background(), "SELECT size FROM indices WHERE url = ?", mw.url).Scan(&oldSize) == nil

	if err := c.ensureMetadataSpace(mw.size - oldSizeIf(haveOld, oldSize)); err != nil {
		_ = os.Remove(mw.tmpPath)
		return err
	}

	finalPath := c.metadataPath(mw.url)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0750); err != nil {
		_ = os.Remove(mw.tmpPath)
		return fmt.Errorf("failed to create metadata dir: %w", err)
	}
	// Rename onto an existing destination fails on Windows; remove first.
	if _, statErr := os.Stat(finalPath); statErr == nil {
		if err := os.Remove(finalPath); err != nil {
			_ = os.Remove(mw.tmpPath)
			return fmt.Errorf("failed to replace metadata file: %w", err)
		}
	}
	if err := os.Rename(mw.tmpPath, finalPath); err != nil {
		_ = os.Remove(mw.tmpPath)
		return fmt.Errorf("failed to install metadata file: %w", err)
	}

	now := time.Now().Unix()
	_, err := c.db.ExecContext(context.Background(), `
		INSERT INTO indices (url, etag, last_modified, fetched_at, path, size, content_type, last_accessed, access_count, last_validated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT(url) DO UPDATE SET
			etag = excluded.etag, last_modified = excluded.last_modified,
			fetched_at = excluded.fetched_at, path = excluded.path, size = excluded.size,
			content_type = excluded.content_type, last_accessed = excluded.last_accessed,
			access_count = indices.access_count + 1, last_validated = excluded.last_validated`,
		mw.url, mw.etag, mw.lastModified, now, finalPath, mw.size, mw.contentType, now, now)
	if err != nil {
		// The row failed but the file is installed; remove it to avoid an orphan.
		_ = os.Remove(finalPath)
		return fmt.Errorf("failed to record metadata: %w", err)
	}

	if haveOld {
		c.metadataSize -= oldSize
	}
	c.metadataSize += mw.size
	if c.metadataSize < 0 {
		c.metadataSize = 0
	}
	return nil
}

func oldSizeIf(have bool, n int64) int64 {
	if have {
		return n
	}
	return 0
}

// ensureMetadataSpace evicts least-recently-accessed metadata until needed bytes
// fit within the budget. Must be called with c.mu held.
func (c *Cache) ensureMetadataSpace(needed int64) error {
	if c.metadataSize+needed <= c.metadataMaxSize {
		return nil
	}

	type victim struct {
		url  string
		size int64
	}
	// Read all candidates up front (deferred Close) so the delete loop below is
	// not iterating an open cursor.
	victims, err := func() ([]victim, error) {
		rows, err := c.db.QueryContext(context.Background(),
			`SELECT url, size FROM indices ORDER BY last_accessed ASC, access_count ASC`)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var vs []victim
		for rows.Next() {
			var v victim
			if err := rows.Scan(&v.url, &v.size); err != nil {
				continue
			}
			vs = append(vs, v)
		}
		return vs, rows.Err()
	}()
	if err != nil {
		return fmt.Errorf("error iterating metadata eviction candidates: %w", err)
	}

	for _, v := range victims {
		if c.metadataSize+needed <= c.metadataMaxSize {
			break
		}
		if err := c.deleteMetadataUnlocked(v.url, v.size); err != nil {
			c.logger.Warn("Failed to evict metadata", zap.Error(err))
			continue
		}
		if c.onMetadataEvict != nil {
			c.onMetadataEvict()
		}
	}

	if c.metadataSize+needed > c.metadataMaxSize {
		return ErrCacheFull
	}
	return nil
}

// deleteMetadataUnlocked removes a metadata file then its row (file first, so a
// failed removal never leaves a row promising a missing file). Must hold c.mu.
func (c *Cache) deleteMetadataUnlocked(url string, size int64) error {
	path := c.metadataPath(url)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := c.db.ExecContext(context.Background(), "DELETE FROM indices WHERE url = ?", url); err != nil {
		return err
	}
	c.metadataSize -= size
	if c.metadataSize < 0 {
		c.metadataSize = 0
	}
	return nil
}
