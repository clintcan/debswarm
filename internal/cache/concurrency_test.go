package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
	"time"
)

func putTestContent(t *testing.T, c *Cache, content []byte, filename string) string {
	t.Helper()
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	if err := c.Put(bytes.NewReader(content), hash, filename); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return hash
}

// TestAccessBatching_PersistsAcrossReopen verifies that batched access records
// survive Close (final flush) and are visible after reopening the cache.
func TestAccessBatching_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	logger := testLogger()

	c, err := New(dir, 1<<20, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hash := putTestContent(t, c, []byte("access batching payload"), "batch_1.0_amd64.deb")

	for range 3 {
		reader, _, err := c.Get(hash)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		_, _ = io.Copy(io.Discard, reader)
		_ = reader.Close()
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := New(dir, 1<<20, logger)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	pkgs, err := reopened.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("List returned %d packages, want 1", len(pkgs))
	}
	// 1 access from Put + 3 batched Gets, flushed on Close.
	if pkgs[0].AccessCount < 4 {
		t.Errorf("AccessCount = %d, want >= 4 (batched accesses must be flushed on Close)", pkgs[0].AccessCount)
	}
}

// TestStats_SeesUnflushedAccesses verifies read-your-writes on the
// observability path: Stats folds in batched access records immediately,
// without waiting for the periodic flush.
func TestStats_SeesUnflushedAccesses(t *testing.T) {
	c, err := New(t.TempDir(), 1<<20, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	hash := putTestContent(t, c, []byte("stats visibility payload"), "statspkg_1.0_amd64.deb")

	before, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	reader, _, err := c.Get(hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = reader.Close()

	after, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if after.TotalAccesses != before.TotalAccesses+1 {
		t.Errorf("TotalAccesses = %d, want %d (Stats must flush pending accesses)",
			after.TotalAccesses, before.TotalAccesses+1)
	}
}

// slowReader trickles its payload so a Put stays in its copy phase long enough
// for the test to probe concurrent reads.
type slowReader struct {
	data  []byte
	pos   int
	delay time.Duration
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n := copy(p, r.data[r.pos:r.pos+min(1024, len(r.data)-r.pos)])
	r.pos += n
	return n, nil
}

// TestGet_NotBlockedByInFlightPut verifies the lock split: a slow Put (copying
// data to disk) must not stall concurrent cache hits. Before the split, Put
// held the exclusive lock across the whole copy and every Get queued behind it.
func TestGet_NotBlockedByInFlightPut(t *testing.T) {
	c, err := New(t.TempDir(), 1<<20, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	cachedHash := putTestContent(t, c, []byte("already cached payload"), "cached_1.0_amd64.deb")

	// Start a Put whose copy phase takes ~2.5s (50 reads x 50ms).
	slowContent := make([]byte, 50*1024)
	sum := sha256.Sum256(slowContent)
	slowHash := hex.EncodeToString(sum[:])
	putDone := make(chan error, 1)
	go func() {
		putDone <- c.Put(&slowReader{data: slowContent, delay: 50 * time.Millisecond}, slowHash, "slow_1.0_amd64.deb")
	}()

	// Give the Put time to enter its copy phase, then hit the cache.
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	reader, _, err := c.Get(cachedHash)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Get during in-flight Put: %v", err)
	}
	_ = reader.Close()

	// Generous bound: the Get must complete while the Put is still copying
	// (~2s remaining), not queue behind it.
	if elapsed > time.Second {
		t.Errorf("Get took %v during an in-flight Put; reads must not queue behind the copy phase", elapsed)
	}

	if err := <-putDone; err != nil {
		t.Fatalf("slow Put failed: %v", err)
	}
}
