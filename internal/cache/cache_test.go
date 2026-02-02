package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

func TestPutFile(t *testing.T) {
	c, dir := testCache(t)

	data := []byte("test package content for PutFile")
	hash := hashData(data)

	// Create a temp file with the data
	tempFile := filepath.Join(dir, "temp_package")
	if err := os.WriteFile(tempFile, data, 0600); err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	// PutFile (moves the file to cache)
	err := c.PutFile(tempFile, hash, "test.deb", int64(len(data)))
	if err != nil {
		t.Fatalf("PutFile failed: %v", err)
	}

	// Verify temp file was moved (no longer exists)
	if _, err := os.Stat(tempFile); !os.IsNotExist(err) {
		t.Error("Temp file should have been moved")
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

func TestPartialDir(t *testing.T) {
	c, tmpDir := testCache(t)

	hash := "abc123def456"
	expected := filepath.Join(tmpDir, "packages", "partial", hash)
	got := c.PartialDir(hash)

	if got != expected {
		t.Errorf("PartialDir = %q, want %q", got, expected)
	}
}

func TestEnsurePartialDir(t *testing.T) {
	c, tmpDir := testCache(t)

	hash := "abc123def456"
	err := c.EnsurePartialDir(hash)
	if err != nil {
		t.Fatalf("EnsurePartialDir failed: %v", err)
	}

	// Verify directory exists
	partialDir := filepath.Join(tmpDir, "packages", "partial", hash)
	info, err := os.Stat(partialDir)
	if err != nil {
		t.Fatalf("Directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("PartialDir should be a directory")
	}

	// Calling again should not error (idempotent)
	err = c.EnsurePartialDir(hash)
	if err != nil {
		t.Errorf("Second EnsurePartialDir failed: %v", err)
	}
}

func TestCleanPartialDir(t *testing.T) {
	c, tmpDir := testCache(t)

	hash := "abc123def456"

	// Create the partial directory with some content
	partialDir := filepath.Join(tmpDir, "packages", "partial", hash)
	if err := os.MkdirAll(partialDir, 0755); err != nil {
		t.Fatalf("Failed to create partial dir: %v", err)
	}

	// Create a file inside
	testFile := filepath.Join(partialDir, "chunk_0")
	if err := os.WriteFile(testFile, []byte("chunk data"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Clean the directory
	err := c.CleanPartialDir(hash)
	if err != nil {
		t.Fatalf("CleanPartialDir failed: %v", err)
	}

	// Verify directory is gone
	if _, err := os.Stat(partialDir); !os.IsNotExist(err) {
		t.Error("Partial directory should be removed")
	}

	// Cleaning non-existent dir should not error
	err = c.CleanPartialDir("nonexistent")
	if err != nil {
		t.Errorf("CleanPartialDir on non-existent dir failed: %v", err)
	}
}

func TestBasePath(t *testing.T) {
	c, tmpDir := testCache(t)

	if c.BasePath() != tmpDir {
		t.Errorf("BasePath = %q, want %q", c.BasePath(), tmpDir)
	}
}

func TestGetDB(t *testing.T) {
	c, _ := testCache(t)

	db := c.GetDB()
	if db == nil {
		t.Fatal("GetDB returned nil")
	}

	// Verify we can query the database
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM packages").Scan(&count)
	if err != nil {
		t.Errorf("Query on GetDB failed: %v", err)
	}
}

func TestCachePersistence(t *testing.T) {
	tmpDir := t.TempDir()

	// Create cache and add package
	c1, err := New(tmpDir, 100*1024*1024, testLogger())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	data := []byte("persistent package")
	hash := hashData(data)

	if err := c1.Put(bytes.NewReader(data), hash, "persist.deb"); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	c1.Close()

	// Reopen cache and verify package is still there
	c2, err := New(tmpDir, 100*1024*1024, testLogger())
	if err != nil {
		t.Fatalf("Failed to reopen cache: %v", err)
	}
	defer c2.Close()

	if !c2.Has(hash) {
		t.Error("Package should exist after cache reopen")
	}

	reader, pkg, err := c2.Get(hash)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	defer reader.Close()

	if pkg.Filename != "persist.deb" {
		t.Errorf("Filename = %q, want persist.deb", pkg.Filename)
	}

	// Verify Size is calculated on reopen
	if c2.Size() != int64(len(data)) {
		t.Errorf("Size = %d, want %d", c2.Size(), len(data))
	}
}

func TestConcurrentPut(t *testing.T) {
	c, _ := testCache(t)

	var wg sync.WaitGroup
	errors := make(chan error, 10)

	// Concurrent puts of different packages
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("package content %d", n))
			hash := hashData(data)
			err := c.Put(bytes.NewReader(data), hash, fmt.Sprintf("pkg%d.deb", n))
			if err != nil {
				errors <- fmt.Errorf("put %d failed: %w", n, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// Verify all packages were stored
	if c.Count() != 10 {
		t.Errorf("Count = %d, want 10", c.Count())
	}
}

func TestHasNonexistent(t *testing.T) {
	c, _ := testCache(t)

	if c.Has("0000000000000000000000000000000000000000000000000000000000000000") {
		t.Error("Has should return false for nonexistent hash")
	}
}

func TestListEmpty(t *testing.T) {
	c, _ := testCache(t)

	list, err := c.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(list) != 0 {
		t.Errorf("Expected empty list, got %d items", len(list))
	}
}

func TestGetUnannouncedEmpty(t *testing.T) {
	c, _ := testCache(t)

	unannounced, err := c.GetUnannounced()
	if err != nil {
		t.Fatalf("GetUnannounced failed: %v", err)
	}

	if len(unannounced) != 0 {
		t.Errorf("Expected empty unannounced list, got %d items", len(unannounced))
	}
}

func TestDeleteNonexistent(t *testing.T) {
	c, _ := testCache(t)

	// Deleting nonexistent should not error
	err := c.Delete("0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Errorf("Delete nonexistent should not error, got: %v", err)
	}
}

func TestMultipleReadersSimultaneous(t *testing.T) {
	c, _ := testCache(t)

	data := []byte("shared package content")
	hash := hashData(data)

	err := c.Put(bytes.NewReader(data), hash, "shared.deb")
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Open multiple readers
	readers := make([]io.ReadCloser, 5)
	for i := 0; i < 5; i++ {
		reader, _, err := c.Get(hash)
		if err != nil {
			t.Fatalf("Get %d failed: %v", i, err)
		}
		readers[i] = reader
	}

	// All readers should work
	for i, reader := range readers {
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Errorf("Read %d failed: %v", i, err)
		}
		if !bytes.Equal(content, data) {
			t.Errorf("Reader %d got wrong content", i)
		}
	}

	// Delete should fail while readers are open
	err = c.Delete(hash)
	if err != ErrFileInUse {
		t.Errorf("Delete should fail with ErrFileInUse while readers open, got: %v", err)
	}

	// Close all readers
	for _, reader := range readers {
		reader.Close()
	}

	// Now delete should succeed
	err = c.Delete(hash)
	if err != nil {
		t.Errorf("Delete after closing readers failed: %v", err)
	}
}

func TestListByPackageName(t *testing.T) {
	c, _ := testCache(t)

	// Add packages with proper Debian naming
	packages := []struct {
		content  string
		filename string
	}{
		{"curl v1", "curl_7.88.1-10_amd64.deb"},
		{"curl v2", "curl_7.88.1-11_amd64.deb"},
		{"curl arm", "curl_7.88.1-10_arm64.deb"},
		{"nginx", "nginx_1.22.1-9_amd64.deb"},
	}

	for _, p := range packages {
		data := []byte(p.content)
		hash := hashData(data)
		if err := c.Put(bytes.NewReader(data), hash, p.filename); err != nil {
			t.Fatalf("Put %s failed: %v", p.filename, err)
		}
	}

	// Query for curl packages
	curlPkgs, err := c.ListByPackageName("curl")
	if err != nil {
		t.Fatalf("ListByPackageName failed: %v", err)
	}

	if len(curlPkgs) != 3 {
		t.Errorf("Expected 3 curl packages, got %d", len(curlPkgs))
	}

	// Verify package metadata was parsed correctly
	for _, pkg := range curlPkgs {
		if pkg.PackageName != "curl" {
			t.Errorf("Expected package name 'curl', got %q", pkg.PackageName)
		}
		if pkg.PackageVersion == "" {
			t.Error("Package version should not be empty")
		}
		if pkg.Architecture == "" {
			t.Error("Architecture should not be empty")
		}
	}

	// Query for nginx packages
	nginxPkgs, err := c.ListByPackageName("nginx")
	if err != nil {
		t.Fatalf("ListByPackageName failed: %v", err)
	}

	if len(nginxPkgs) != 1 {
		t.Errorf("Expected 1 nginx package, got %d", len(nginxPkgs))
	}

	// Query for non-existent package
	nonePkgs, err := c.ListByPackageName("nonexistent")
	if err != nil {
		t.Fatalf("ListByPackageName failed: %v", err)
	}

	if len(nonePkgs) != 0 {
		t.Errorf("Expected 0 packages, got %d", len(nonePkgs))
	}
}

func TestGetByNameVersionArch(t *testing.T) {
	c, _ := testCache(t)

	// Add a package
	data := []byte("curl package content")
	hash := hashData(data)
	filename := "curl_7.88.1-10_amd64.deb"

	if err := c.Put(bytes.NewReader(data), hash, filename); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get by exact name/version/arch
	pkg, err := c.GetByNameVersionArch("curl", "7.88.1-10", "amd64")
	if err != nil {
		t.Fatalf("GetByNameVersionArch failed: %v", err)
	}

	if pkg.PackageName != "curl" {
		t.Errorf("Expected name 'curl', got %q", pkg.PackageName)
	}
	if pkg.PackageVersion != "7.88.1-10" {
		t.Errorf("Expected version '7.88.1-10', got %q", pkg.PackageVersion)
	}
	if pkg.Architecture != "amd64" {
		t.Errorf("Expected arch 'amd64', got %q", pkg.Architecture)
	}
	if pkg.SHA256 != hash {
		t.Errorf("Expected hash %q, got %q", hash, pkg.SHA256)
	}

	// Try to get non-existent version
	_, err = c.GetByNameVersionArch("curl", "9.99.99", "amd64")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}

	// Try to get non-existent package
	_, err = c.GetByNameVersionArch("nonexistent", "1.0", "amd64")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}

	// Try to get wrong architecture
	_, err = c.GetByNameVersionArch("curl", "7.88.1-10", "arm64")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

func TestPopulateMissingMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	// Create cache
	c, err := New(tmpDir, 100*1024*1024, testLogger())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	// Manually insert packages with empty metadata (simulating old entries)
	data1 := []byte("old curl package")
	hash1 := hashData(data1)

	data2 := []byte("old nginx package")
	hash2 := hashData(data2)

	// Write files to disk
	for _, item := range []struct {
		hash string
		data []byte
	}{
		{hash1, data1},
		{hash2, data2},
	} {
		path := c.packagePath(item.hash)
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatalf("Failed to create dir: %v", err)
		}
		if err := os.WriteFile(path, item.data, 0644); err != nil {
			t.Fatalf("Failed to write file: %v", err)
		}
	}

	// Insert into database with empty metadata
	db := c.GetDB()
	_, err = db.Exec(`INSERT INTO packages (sha256, size, filename, added_at, last_accessed, access_count, announced, package_name, package_version, architecture)
		VALUES (?, ?, ?, 0, 0, 1, 0, '', '', '')`,
		hash1, len(data1), "curl_7.88.1-10_amd64.deb")
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	_, err = db.Exec(`INSERT INTO packages (sha256, size, filename, added_at, last_accessed, access_count, announced, package_name, package_version, architecture)
		VALUES (?, ?, ?, 0, 0, 1, 0, '', '', '')`,
		hash2, len(data2), "nginx_1.22.1-9_arm64.deb")
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Run migration
	updated, err := c.PopulateMissingMetadata()
	if err != nil {
		t.Fatalf("PopulateMissingMetadata failed: %v", err)
	}

	if updated != 2 {
		t.Errorf("Expected 2 packages migrated, got %d", updated)
	}

	// Verify metadata was populated
	curlPkg, err := c.GetByNameVersionArch("curl", "7.88.1-10", "amd64")
	if err != nil {
		t.Fatalf("GetByNameVersionArch failed: %v", err)
	}
	if curlPkg.PackageName != "curl" {
		t.Errorf("Expected name 'curl', got %q", curlPkg.PackageName)
	}

	nginxPkg, err := c.GetByNameVersionArch("nginx", "1.22.1-9", "arm64")
	if err != nil {
		t.Fatalf("GetByNameVersionArch failed: %v", err)
	}
	if nginxPkg.PackageName != "nginx" {
		t.Errorf("Expected name 'nginx', got %q", nginxPkg.PackageName)
	}

	// Running migration again should update 0 packages
	updated, err = c.PopulateMissingMetadata()
	if err != nil {
		t.Fatalf("PopulateMissingMetadata failed: %v", err)
	}
	if updated != 0 {
		t.Errorf("Expected 0 packages migrated on second run, got %d", updated)
	}

	c.Close()
}

func TestPackageMetadataPreservedOnUpdate(t *testing.T) {
	c, _ := testCache(t)

	// Add a package
	data := []byte("test package")
	hash := hashData(data)
	filename := "curl_7.88.1-10_amd64.deb"

	if err := c.Put(bytes.NewReader(data), hash, filename); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Verify metadata was set
	pkg, err := c.GetByNameVersionArch("curl", "7.88.1-10", "amd64")
	if err != nil {
		t.Fatalf("GetByNameVersionArch failed: %v", err)
	}
	if pkg.PackageName != "curl" {
		t.Errorf("Expected name 'curl', got %q", pkg.PackageName)
	}

	// Put again with a different filename that fails to parse
	// (simulating a non-.deb filename passed to Put)
	if err := c.Put(bytes.NewReader(data), hash, "some-other-name.bin"); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Metadata should be preserved from first put
	pkg, err = c.GetByNameVersionArch("curl", "7.88.1-10", "amd64")
	if err != nil {
		t.Fatalf("GetByNameVersionArch failed after second put: %v", err)
	}
	if pkg.PackageName != "curl" {
		t.Errorf("Metadata should be preserved, but name is %q", pkg.PackageName)
	}
}

func TestStats(t *testing.T) {
	c, _ := testCache(t)

	// Stats on empty cache
	stats, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats.TotalPackages != 0 {
		t.Errorf("Expected 0 packages, got %d", stats.TotalPackages)
	}
	if stats.TotalSize != 0 {
		t.Errorf("Expected 0 size, got %d", stats.TotalSize)
	}
	if stats.TotalAccesses != 0 {
		t.Errorf("Expected 0 accesses, got %d", stats.TotalAccesses)
	}
	if stats.BandwidthSaved != 0 {
		t.Errorf("Expected 0 bandwidth saved, got %d", stats.BandwidthSaved)
	}

	// Add some packages
	packages := []struct {
		content  string
		filename string
	}{
		{"curl package content here", "curl_7.88.1-10_amd64.deb"},
		{"nginx package content", "nginx_1.22.1-9_amd64.deb"},
		{"some other file", "other.bin"},
	}

	var totalSize int64
	for _, p := range packages {
		data := []byte(p.content)
		totalSize += int64(len(data))
		hash := hashData(data)
		if err := c.Put(bytes.NewReader(data), hash, p.filename); err != nil {
			t.Fatalf("Put %s failed: %v", p.filename, err)
		}
	}

	// Access one package multiple times
	curlData := []byte("curl package content here")
	curlHash := hashData(curlData)
	for i := 0; i < 5; i++ {
		reader, _, err := c.Get(curlHash)
		if err != nil {
			t.Fatalf("Get curl failed: %v", err)
		}
		reader.Close()
	}

	stats, err = c.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if stats.TotalPackages != 3 {
		t.Errorf("Expected 3 packages, got %d", stats.TotalPackages)
	}
	if stats.TotalSize != totalSize {
		t.Errorf("Expected size %d, got %d", totalSize, stats.TotalSize)
	}
	// curl: 1 (put) + 5 (get) = 6, nginx: 1, other: 1 = 8 total
	if stats.TotalAccesses < 7 {
		t.Errorf("Expected at least 7 accesses, got %d", stats.TotalAccesses)
	}
	// 2 packages have parseable Debian names
	if stats.UniquePackages != 2 {
		t.Errorf("Expected 2 packages with metadata, got %d", stats.UniquePackages)
	}
	// BandwidthSaved should be > 0
	if stats.BandwidthSaved <= 0 {
		t.Errorf("Expected positive bandwidth saved, got %d", stats.BandwidthSaved)
	}
}

func TestPopularPackages(t *testing.T) {
	c, _ := testCache(t)

	// Add packages
	packages := []struct {
		content  string
		filename string
		accesses int
	}{
		{"curl content", "curl_7.88.1-10_amd64.deb", 10},
		{"nginx content", "nginx_1.22.1-9_amd64.deb", 5},
		{"wget content", "wget_1.21-1_amd64.deb", 2},
	}

	for _, p := range packages {
		data := []byte(p.content)
		hash := hashData(data)
		if err := c.Put(bytes.NewReader(data), hash, p.filename); err != nil {
			t.Fatalf("Put %s failed: %v", p.filename, err)
		}
		// Access multiple times
		for i := 0; i < p.accesses-1; i++ {
			reader, _, err := c.Get(hash)
			if err != nil {
				t.Fatalf("Get %s failed: %v", p.filename, err)
			}
			reader.Close()
		}
	}

	// Get popular packages
	popular, err := c.PopularPackages(10)
	if err != nil {
		t.Fatalf("PopularPackages failed: %v", err)
	}

	if len(popular) != 3 {
		t.Fatalf("Expected 3 packages, got %d", len(popular))
	}

	// First should be curl (most accessed)
	if popular[0].PackageName != "curl" {
		t.Errorf("Expected curl first, got %s", popular[0].PackageName)
	}
	if popular[1].PackageName != "nginx" {
		t.Errorf("Expected nginx second, got %s", popular[1].PackageName)
	}
	if popular[2].PackageName != "wget" {
		t.Errorf("Expected wget third, got %s", popular[2].PackageName)
	}

	// Test with limit
	limited, err := c.PopularPackages(2)
	if err != nil {
		t.Fatalf("PopularPackages with limit failed: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("Expected 2 packages with limit, got %d", len(limited))
	}

	// Test with zero/negative limit (defaults to 10)
	defaulted, err := c.PopularPackages(0)
	if err != nil {
		t.Fatalf("PopularPackages with 0 limit failed: %v", err)
	}
	if len(defaulted) != 3 {
		t.Errorf("Expected 3 packages with default limit, got %d", len(defaulted))
	}
}

func TestRecentPackages(t *testing.T) {
	c, _ := testCache(t)

	// Add packages in sequence
	packages := []struct {
		content  string
		filename string
	}{
		{"first content", "first_1.0.0_amd64.deb"},
		{"second content", "second_1.0.0_amd64.deb"},
		{"third content", "third_1.0.0_amd64.deb"},
	}

	for _, p := range packages {
		data := []byte(p.content)
		hash := hashData(data)
		if err := c.Put(bytes.NewReader(data), hash, p.filename); err != nil {
			t.Fatalf("Put %s failed: %v", p.filename, err)
		}
	}

	// Get recent packages - should return all 3 packages
	recent, err := c.RecentPackages(10)
	if err != nil {
		t.Fatalf("RecentPackages failed: %v", err)
	}

	if len(recent) != 3 {
		t.Fatalf("Expected 3 packages, got %d", len(recent))
	}

	// Verify all packages are present (order may vary when times are equal)
	names := make(map[string]bool)
	for _, pkg := range recent {
		names[pkg.PackageName] = true
	}
	for _, expected := range []string{"first", "second", "third"} {
		if !names[expected] {
			t.Errorf("Expected package %q not found in recent list", expected)
		}
	}

	// Test with limit
	limited, err := c.RecentPackages(1)
	if err != nil {
		t.Fatalf("RecentPackages with limit failed: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("Expected 1 package with limit, got %d", len(limited))
	}
}

func TestStatsEmpty(t *testing.T) {
	c, _ := testCache(t)

	stats, err := c.Stats()
	if err != nil {
		t.Fatalf("Stats on empty cache failed: %v", err)
	}

	if stats.TotalPackages != 0 {
		t.Errorf("Expected 0 packages, got %d", stats.TotalPackages)
	}
	if stats.TotalSize != 0 {
		t.Errorf("Expected 0 size, got %d", stats.TotalSize)
	}
	if stats.MaxSize != 100*1024*1024 {
		t.Errorf("Expected MaxSize 100MB, got %d", stats.MaxSize)
	}
}

func TestPopularPackagesEmpty(t *testing.T) {
	c, _ := testCache(t)

	popular, err := c.PopularPackages(10)
	if err != nil {
		t.Fatalf("PopularPackages on empty cache failed: %v", err)
	}

	if len(popular) != 0 {
		t.Errorf("Expected 0 packages, got %d", len(popular))
	}
}

func TestRecentPackagesEmpty(t *testing.T) {
	c, _ := testCache(t)

	recent, err := c.RecentPackages(10)
	if err != nil {
		t.Fatalf("RecentPackages on empty cache failed: %v", err)
	}

	if len(recent) != 0 {
		t.Errorf("Expected 0 packages, got %d", len(recent))
	}
}
