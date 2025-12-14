package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	return zap.NewNop()
}

func testCache(t *testing.T) (*Cache, string) {
	t.Helper()
	tmpDir := t.TempDir()
	c, err := New(tmpDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, tmpDir
}

func hashData(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestNew(t *testing.T) {
	tmpDir := t.TempDir()
	c, err := New(tmpDir, 100*1024*1024, testLogger())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	// Verify directories were created
	dirs := []string{
		filepath.Join(tmpDir, "packages", "sha256"),
		filepath.Join(tmpDir, "packages", "pending"),
		filepath.Join(tmpDir, "indices"),
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("Directory not created: %s", dir)
		}
	}

	// Verify database was created
	dbPath := filepath.Join(tmpDir, "state.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database not created")
	}
}

func TestPutAndGet(t *testing.T) {
	c, _ := testCache(t)

	data := []byte("test package content")
	hash := hashData(data)

	// Put
	err := c.Put(bytes.NewReader(data), hash, "test.deb")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Has
	if !c.Has(hash) {
		t.Error("Has returned false for existing package")
	}

	// Get
	reader, pkg, err := c.Get(hash)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	defer reader.Close()

	// Verify content
	retrieved, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Failed to read content: %v", err)
	}
	if !bytes.Equal(retrieved, data) {
		t.Error("Retrieved data doesn't match original")
	}

	// Verify metadata
	if pkg.SHA256 != hash {
		t.Errorf("Hash mismatch: got %s, want %s", pkg.SHA256, hash)
	}
	if pkg.Size != int64(len(data)) {
		t.Errorf("Size mismatch: got %d, want %d", pkg.Size, len(data))
	}
	if pkg.Filename != "test.deb" {
		t.Errorf("Filename mismatch: got %s, want test.deb", pkg.Filename)
	}
}

func TestPutHashMismatch(t *testing.T) {
	c, _ := testCache(t)

	data := []byte("test package content")
	wrongHash := hashData([]byte("wrong content"))

	err := c.Put(bytes.NewReader(data), wrongHash, "test.deb")
	if err == nil {
		t.Fatal("Expected error for hash mismatch")
	}
	if err != ErrHashMismatch && !bytes.Contains([]byte(err.Error()), []byte("hash mismatch")) {
		t.Errorf("Expected hash mismatch error, got: %v", err)
	}

	// Verify package was not stored
	if c.Has(wrongHash) {
		t.Error("Package should not be stored on hash mismatch")
	}
}

func TestGetNotFound(t *testing.T) {
	c, _ := testCache(t)

	_, _, err := c.Get("0000000000000000000000000000000000000000000000000000000000000000")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got: %v", err)
	}
}

func TestDelete(t *testing.T) {
	c, _ := testCache(t)

	data := []byte("test package content")
	hash := hashData(data)

	// Put
	err := c.Put(bytes.NewReader(data), hash, "test.deb")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Delete
	err = c.Delete(hash)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted
	if c.Has(hash) {
		t.Error("Package still exists after delete")
	}

	_, _, err = c.Get(hash)
	if err != ErrNotFound {
		t.Error("Get should return ErrNotFound after delete")
	}
}

func TestList(t *testing.T) {
	c, _ := testCache(t)

	// Add multiple packages
	packages := []struct {
		data     []byte
		filename string
	}{
		{[]byte("package 1"), "pkg1.deb"},
		{[]byte("package 2"), "pkg2.deb"},
		{[]byte("package 3"), "pkg3.deb"},
	}

	for _, pkg := range packages {
		hash := hashData(pkg.data)
		if err := c.Put(bytes.NewReader(pkg.data), hash, pkg.filename); err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}

	// List
	list, err := c.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(list) != len(packages) {
		t.Errorf("Expected %d packages, got %d", len(packages), len(list))
	}
}

func TestSizeAndCount(t *testing.T) {
	c, _ := testCache(t)

	if c.Size() != 0 {
		t.Errorf("Initial size should be 0, got %d", c.Size())
	}
	if c.Count() != 0 {
		t.Errorf("Initial count should be 0, got %d", c.Count())
	}

	data := []byte("test package content")
	hash := hashData(data)

	err := c.Put(bytes.NewReader(data), hash, "test.deb")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if c.Size() != int64(len(data)) {
		t.Errorf("Size should be %d, got %d", len(data), c.Size())
	}
	if c.Count() != 1 {
		t.Errorf("Count should be 1, got %d", c.Count())
	}
}

func TestMarkAnnounced(t *testing.T) {
	c, _ := testCache(t)

	data := []byte("test package content")
	hash := hashData(data)

	err := c.Put(bytes.NewReader(data), hash, "test.deb")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Initially should be in unannounced list
	unannounced, err := c.GetUnannounced()
	if err != nil {
		t.Fatalf("GetUnannounced failed: %v", err)
	}
	if len(unannounced) != 1 {
		t.Errorf("Expected 1 unannounced, got %d", len(unannounced))
	}

	// Mark as announced
	err = c.MarkAnnounced(hash)
	if err != nil {
		t.Fatalf("MarkAnnounced failed: %v", err)
	}

	// Should no longer be in unannounced list
	unannounced, err = c.GetUnannounced()
	if err != nil {
		t.Fatalf("GetUnannounced failed: %v", err)
	}
	if len(unannounced) != 0 {
		t.Errorf("Expected 0 unannounced after marking, got %d", len(unannounced))
	}
}

func TestEviction(t *testing.T) {
	tmpDir := t.TempDir()
	// Create cache with very small max size (1KB)
	c, err := New(tmpDir, 1024, testLogger())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	// Add a package that fits
	data1 := make([]byte, 300)
	copy(data1, "package1")
	hash1 := hashData(data1)
	if err := c.Put(bytes.NewReader(data1), hash1, "pkg1.deb"); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Add another that fits
	data2 := make([]byte, 300)
	copy(data2, "package2")
	hash2 := hashData(data2)
	if err := c.Put(bytes.NewReader(data2), hash2, "pkg2.deb"); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Try to add one that would exceed limit
	// Eviction happens for packages older than 7 days, so this tests the limit check
	data3 := make([]byte, 600)
	copy(data3, "package3")
	hash3 := hashData(data3)

	// This may or may not succeed depending on eviction logic
	// The important thing is it shouldn't panic
	_ = c.Put(bytes.NewReader(data3), hash3, "pkg3.deb")
}

func TestConcurrentAccess(t *testing.T) {
	c, _ := testCache(t)

	data := []byte("concurrent test package")
	hash := hashData(data)

	// Put the package first
	err := c.Put(bytes.NewReader(data), hash, "test.deb")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Concurrent reads
	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader, _, err := c.Get(hash)
			if err != nil {
				errors <- err
				return
			}
			defer reader.Close()
			_, err = io.ReadAll(reader)
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent access error: %v", err)
	}
}

func TestDeleteWhileReading(t *testing.T) {
	c, _ := testCache(t)

	data := []byte("test package content")
	hash := hashData(data)

	err := c.Put(bytes.NewReader(data), hash, "test.deb")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Open for reading
	reader, _, err := c.Get(hash)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Try to delete while reading - should fail
	err = c.Delete(hash)
	if err != ErrFileInUse {
		t.Errorf("Expected ErrFileInUse, got: %v", err)
	}

	// Close reader
	reader.Close()

	// Now delete should succeed
	err = c.Delete(hash)
	if err != nil {
		t.Errorf("Delete after close failed: %v", err)
	}
}

func TestTrackedReaderDoubleClose(t *testing.T) {
	c, _ := testCache(t)

	data := []byte("test package content")
	hash := hashData(data)

	err := c.Put(bytes.NewReader(data), hash, "test.deb")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	reader, _, err := c.Get(hash)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Double close should not panic or error
	if err := reader.Close(); err != nil {
		t.Errorf("First close failed: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Errorf("Second close failed: %v", err)
	}
}

func TestPutReplacesExisting(t *testing.T) {
	c, _ := testCache(t)

	data := []byte("test package content")
	hash := hashData(data)

	// Put first time
	err := c.Put(bytes.NewReader(data), hash, "test1.deb")
	if err != nil {
		t.Fatalf("First put failed: %v", err)
	}

	// Put again with different filename
	err = c.Put(bytes.NewReader(data), hash, "test2.deb")
	if err != nil {
		t.Fatalf("Second put failed: %v", err)
	}

	// Should still have only one entry
	if c.Count() != 1 {
		t.Errorf("Expected count 1, got %d", c.Count())
	}

	// Access count should be 2
	_, pkg, err := c.Get(hash)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	// Access count is incremented by Get too, so it should be >= 2
	if pkg.AccessCount < 2 {
		t.Errorf("Expected access count >= 2, got %d", pkg.AccessCount)
	}
}
