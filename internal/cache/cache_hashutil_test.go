package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// Integration tests to verify hashutil integration in cache.go

// TestPut_HashVerification verifies that Put correctly computes and verifies hashes
func TestPut_HashVerification(t *testing.T) {
	tmpDir := t.TempDir()
	c, err := New(tmpDir, 100*1024*1024, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer c.Close()

	testCases := []struct {
		name string
		data []byte
	}{
		{"small", []byte("hello world")},
		{"medium", bytes.Repeat([]byte("test data "), 1000)},
		{"binary", []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0xfd}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Compute expected hash
			h := sha256.Sum256(tc.data)
			expectedHash := hex.EncodeToString(h[:])

			// Put with correct hash should succeed
			err := c.Put(bytes.NewReader(tc.data), expectedHash, "test.deb")
			if err != nil {
				t.Errorf("Put with correct hash failed: %v", err)
			}

			// Verify we can get it back
			reader, _, err := c.Get(expectedHash)
			if err != nil {
				t.Errorf("Get failed: %v", err)
				return
			}

			readBack, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Errorf("ReadAll failed: %v", err)
				return
			}

			if !bytes.Equal(readBack, tc.data) {
				t.Errorf("data mismatch: got %d bytes, want %d bytes", len(readBack), len(tc.data))
			}
		})
	}
}

// TestPut_HashMismatchRejection verifies that Put rejects data with wrong hash
func TestPut_HashMismatchRejection(t *testing.T) {
	tmpDir := t.TempDir()
	c, err := New(tmpDir, 100*1024*1024, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer c.Close()

	data := []byte("original data")
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"

	err = c.Put(bytes.NewReader(data), wrongHash, "test.deb")
	if err == nil {
		t.Error("expected error for hash mismatch, got nil")
	}
	if !errors.Is(err, ErrHashMismatch) {
		t.Errorf("expected ErrHashMismatch, got %v", err)
	}

	// Verify data was not stored
	_, _, err = c.Get(wrongHash)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestPut_LargeFile verifies hash computation works for large files
func TestPut_LargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	c, err := New(tmpDir, 100*1024*1024, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer c.Close()

	// Create 5MB of data
	data := make([]byte, 5*1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	h := sha256.Sum256(data)
	expectedHash := hex.EncodeToString(h[:])

	err = c.Put(bytes.NewReader(data), expectedHash, "large.deb")
	if err != nil {
		t.Fatalf("Put large file failed: %v", err)
	}

	// Verify file exists and has correct content
	reader, pkg, err := c.Get(expectedHash)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	defer reader.Close()

	if pkg.Size != int64(len(data)) {
		t.Errorf("size mismatch: got %d, want %d", pkg.Size, len(data))
	}

	// Verify by re-hashing
	hasher := sha256.New()
	io.Copy(hasher, reader)
	actualHash := hex.EncodeToString(hasher.Sum(nil))

	if actualHash != expectedHash {
		t.Errorf("stored file hash mismatch:\n  expected: %s\n  actual:   %s", expectedHash, actualHash)
	}
}

// TestPut_PendingFileCleanup verifies pending file is cleaned up on hash mismatch
func TestPut_PendingFileCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	c, err := New(tmpDir, 100*1024*1024, zap.NewNop())
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer c.Close()

	data := []byte("test data")
	wrongHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	pendingDir := filepath.Join(tmpDir, "packages", "pending")

	// Try to put with wrong hash
	err = c.Put(bytes.NewReader(data), wrongHash, "test.deb")
	if err == nil {
		t.Error("expected error")
	}

	// Verify pending file was cleaned up
	entries, err := os.ReadDir(pendingDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to read pending dir: %v", err)
	}

	for _, e := range entries {
		if e.Name() == wrongHash {
			t.Error("pending file should have been cleaned up")
		}
	}
}
