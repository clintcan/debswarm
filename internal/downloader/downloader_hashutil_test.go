package downloader

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/debswarm/debswarm/internal/hashutil"
)

// Integration tests to verify hashutil integration in downloader.go

// TestHashutil_MatchesOriginalPattern verifies hashutil.HashReader produces
// identical results to the original pattern used in assembleChunks
func TestHashutil_MatchesOriginalPattern(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"small", []byte("hello world")},
		{"medium", bytes.Repeat([]byte("chunk data "), 1000)},
		{"large", make([]byte, 1024*1024)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Original pattern:
			// hasher := sha256.New()
			// io.Copy(hasher, f)
			// actualHashHex := hex.EncodeToString(hasher.Sum(nil))

			origHasher := sha256.New()
			origHasher.Write(tc.data)
			origHash := hex.EncodeToString(origHasher.Sum(nil))

			// New pattern with hashutil.HashReader:
			newHash, err := hashutil.HashReader(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("HashReader failed: %v", err)
			}

			if origHash != newHash {
				t.Errorf("hash mismatch:\n  original: %s\n  new:      %s", origHash, newHash)
			}
		})
	}
}

// TestHashutil_HashBytesMatchesOriginal verifies hashutil.HashBytes produces
// identical results to the original sha256.Sum256 pattern
func TestHashutil_HashBytesMatchesOriginal(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"small", []byte("race result data")},
		{"medium", bytes.Repeat([]byte("test "), 100)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Original pattern:
			// actualHash := sha256.Sum256(res.data)
			// actualHashHex := hex.EncodeToString(actualHash[:])

			origHash := sha256.Sum256(tc.data)
			origHashHex := hex.EncodeToString(origHash[:])

			// New pattern:
			newHashHex := hashutil.HashBytes(tc.data)

			if origHashHex != newHashHex {
				t.Errorf("hash mismatch:\n  original: %s\n  new:      %s", origHashHex, newHashHex)
			}
		})
	}
}

// TestHashutil_FileVerificationWorkflow simulates the actual file verification
// workflow used in assembleChunks
func TestHashutil_FileVerificationWorkflow(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test data simulating assembled chunks
	data := bytes.Repeat([]byte("chunk data for assembly "), 1000)
	expectedHash := computeHash(data)

	// Write to file
	assemblyFile := filepath.Join(tmpDir, "assembled")
	err := os.WriteFile(assemblyFile, data, 0600)
	if err != nil {
		t.Fatalf("failed to write assembly file: %v", err)
	}

	// Open for reading (as done in assembleChunks)
	f, err := os.OpenFile(assemblyFile, os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to open assembly file: %v", err)
	}

	// Seek to beginning (as done in downloader.go)
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		t.Fatalf("failed to seek: %v", err)
	}

	// Verify hash using hashutil
	actualHash, err := hashutil.HashReader(f)
	f.Close()
	if err != nil {
		t.Fatalf("HashReader failed: %v", err)
	}

	if actualHash != expectedHash {
		t.Errorf("hash mismatch:\n  expected: %s\n  actual:   %s", expectedHash, actualHash)
	}
}

// TestHashutil_HashMismatchDetection verifies hash mismatch is correctly detected
func TestHashutil_HashMismatchDetection(t *testing.T) {
	data := []byte("original data")
	correctHash := computeHash(data)
	wrongHash := computeHash([]byte("different data"))

	// Test with HashReader
	t.Run("hash_reader", func(t *testing.T) {
		hash, _ := hashutil.HashReader(bytes.NewReader(data))
		if hash != correctHash {
			t.Error("should match correct hash")
		}
		if hash == wrongHash {
			t.Error("should not match wrong hash")
		}
	})

	// Test with HashBytes
	t.Run("hash_bytes", func(t *testing.T) {
		hash := hashutil.HashBytes(data)
		if hash != correctHash {
			t.Error("should match correct hash")
		}
		if hash == wrongHash {
			t.Error("should not match wrong hash")
		}
	})
}

// TestHashutil_ChunkedAssemblyVerification simulates chunk assembly and verification
func TestHashutil_ChunkedAssemblyVerification(t *testing.T) {
	tmpDir := t.TempDir()

	// Simulate chunked data
	chunks := [][]byte{
		bytes.Repeat([]byte("chunk0"), 1000),
		bytes.Repeat([]byte("chunk1"), 1000),
		bytes.Repeat([]byte("chunk2"), 1000),
		bytes.Repeat([]byte("chunk3"), 500), // Last chunk smaller
	}

	// Compute expected hash of full data
	var fullData []byte
	for _, chunk := range chunks {
		fullData = append(fullData, chunk...)
	}
	expectedHash := computeHash(fullData)

	// Create assembly file
	assemblyFile := filepath.Join(tmpDir, "assembled")
	f, err := os.OpenFile(assemblyFile, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to create assembly file: %v", err)
	}

	// Pre-allocate
	if err := f.Truncate(int64(len(fullData))); err != nil {
		f.Close()
		t.Fatalf("failed to truncate: %v", err)
	}

	// Write chunks at correct offsets
	offset := int64(0)
	for _, chunk := range chunks {
		if _, err := f.WriteAt(chunk, offset); err != nil {
			f.Close()
			t.Fatalf("failed to write chunk: %v", err)
		}
		offset += int64(len(chunk))
	}

	// Seek and verify (as done in assembleChunks)
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		t.Fatalf("failed to seek: %v", err)
	}

	actualHash, err := hashutil.HashReader(f)
	f.Close()
	if err != nil {
		t.Fatalf("HashReader failed: %v", err)
	}

	if actualHash != expectedHash {
		t.Errorf("hash mismatch after chunk assembly:\n  expected: %s\n  actual:   %s", expectedHash, actualHash)
	}
}

func computeHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
