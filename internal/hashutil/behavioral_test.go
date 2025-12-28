package hashutil

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Behavioral tests to verify hashutil produces identical results to original patterns

// TestBehavior_HashingWriterMatchesMultiWriter verifies HashingWriter produces
// the same hash as the original io.MultiWriter pattern used in cache.go
func TestBehavior_HashingWriterMatchesMultiWriter(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"small", []byte("hello world")},
		{"medium", bytes.Repeat([]byte("test data "), 1000)},
		{"large", make([]byte, 1024*1024)}, // 1MB
		{"binary", []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0xfd}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Original pattern from cache.go:
			// hasher := sha256.New()
			// writer := io.MultiWriter(f, hasher)
			// io.Copy(writer, data)
			// actualHash := hex.EncodeToString(hasher.Sum(nil))

			var origBuf bytes.Buffer
			origHasher := sha256.New()
			origWriter := io.MultiWriter(&origBuf, origHasher)
			io.Copy(origWriter, bytes.NewReader(tc.data))
			origHash := hex.EncodeToString(origHasher.Sum(nil))

			// New pattern with HashingWriter:
			var newBuf bytes.Buffer
			hw := NewHashingWriter(&newBuf)
			io.Copy(hw, bytes.NewReader(tc.data))
			newHash := hw.Sum()

			// Verify hashes match
			if origHash != newHash {
				t.Errorf("hash mismatch:\n  original: %s\n  new:      %s", origHash, newHash)
			}

			// Verify data written matches
			if !bytes.Equal(origBuf.Bytes(), newBuf.Bytes()) {
				t.Error("written data mismatch")
			}
		})
	}
}

// TestBehavior_HashReaderMatchesOriginalPattern verifies HashReader produces
// the same hash as the original pattern used in downloader.go
func TestBehavior_HashReaderMatchesOriginalPattern(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"small", []byte("hello world")},
		{"medium", bytes.Repeat([]byte("chunk data "), 1000)},
		{"large", make([]byte, 1024*1024)}, // 1MB
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Original pattern from downloader.go:
			// hasher := sha256.New()
			// io.Copy(hasher, f)
			// actualHashHex := hex.EncodeToString(hasher.Sum(nil))

			origHasher := sha256.New()
			io.Copy(origHasher, bytes.NewReader(tc.data))
			origHash := hex.EncodeToString(origHasher.Sum(nil))

			// New pattern with HashReader:
			newHash, err := HashReader(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("HashReader failed: %v", err)
			}

			if origHash != newHash {
				t.Errorf("hash mismatch:\n  original: %s\n  new:      %s", origHash, newHash)
			}
		})
	}
}

// TestBehavior_HashBytesMatchesOriginalPattern verifies HashBytes produces
// the same hash as the original sha256.Sum256 pattern in downloader.go
func TestBehavior_HashBytesMatchesOriginalPattern(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"small", []byte("hello world")},
		{"medium", bytes.Repeat([]byte("race data "), 100)},
		{"binary", []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0xfd}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Original pattern from downloader.go:
			// actualHash := sha256.Sum256(res.data)
			// actualHashHex := hex.EncodeToString(actualHash[:])

			origHash := sha256.Sum256(tc.data)
			origHashHex := hex.EncodeToString(origHash[:])

			// New pattern with HashBytes:
			newHashHex := HashBytes(tc.data)

			if origHashHex != newHashHex {
				t.Errorf("hash mismatch:\n  original: %s\n  new:      %s", origHashHex, newHashHex)
			}
		})
	}
}

// TestBehavior_FileHashVerification simulates the actual file hashing workflow
func TestBehavior_FileHashVerification(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	testData := bytes.Repeat([]byte("file content for hash verification "), 1000)
	expectedHash := computeExpectedHash(testData)

	// Test 1: Write file with HashingWriter and verify hash
	t.Run("write_and_verify", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "test1.bin")
		f, err := os.Create(filePath)
		if err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		hw := NewHashingWriter(f)
		_, err = io.Copy(hw, bytes.NewReader(testData))
		if err != nil {
			f.Close()
			t.Fatalf("failed to write: %v", err)
		}
		f.Close()

		actualHash := hw.Sum()
		if actualHash != expectedHash {
			t.Errorf("hash mismatch after write:\n  expected: %s\n  actual:   %s", expectedHash, actualHash)
		}

		// Verify file contents
		readBack, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("failed to read back: %v", err)
		}
		if !bytes.Equal(readBack, testData) {
			t.Error("file contents mismatch")
		}
	})

	// Test 2: Read file and verify hash with HashReader
	t.Run("read_and_verify", func(t *testing.T) {
		filePath := filepath.Join(tmpDir, "test2.bin")
		err := os.WriteFile(filePath, testData, 0644)
		if err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		f, err := os.Open(filePath)
		if err != nil {
			t.Fatalf("failed to open file: %v", err)
		}
		defer f.Close()

		actualHash, err := HashReader(f)
		if err != nil {
			t.Fatalf("HashReader failed: %v", err)
		}

		if actualHash != expectedHash {
			t.Errorf("hash mismatch after read:\n  expected: %s\n  actual:   %s", expectedHash, actualHash)
		}
	})

	// Test 3: Simulate cache.go Put() workflow
	t.Run("cache_put_workflow", func(t *testing.T) {
		pendingPath := filepath.Join(tmpDir, "pending")
		f, err := os.Create(pendingPath)
		if err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		hw := NewHashingWriter(f)
		size, err := io.Copy(hw, bytes.NewReader(testData))
		if err != nil {
			f.Close()
			t.Fatalf("failed to copy: %v", err)
		}
		f.Close()

		actualHash := hw.Sum()

		// Verify size
		if size != int64(len(testData)) {
			t.Errorf("size mismatch: got %d, want %d", size, len(testData))
		}

		// Verify hash
		if actualHash != expectedHash {
			t.Errorf("hash mismatch:\n  expected: %s\n  actual:   %s", expectedHash, actualHash)
		}
	})

	// Test 4: Simulate downloader.go assembly verification workflow
	t.Run("downloader_verify_workflow", func(t *testing.T) {
		assemblyPath := filepath.Join(tmpDir, "assembly")
		err := os.WriteFile(assemblyPath, testData, 0600)
		if err != nil {
			t.Fatalf("failed to write assembly: %v", err)
		}

		f, err := os.OpenFile(assemblyPath, os.O_RDWR, 0600)
		if err != nil {
			t.Fatalf("failed to open assembly: %v", err)
		}

		// Seek to beginning (as done in downloader.go)
		if _, err := f.Seek(0, 0); err != nil {
			f.Close()
			t.Fatalf("failed to seek: %v", err)
		}

		actualHash, err := HashReader(f)
		f.Close()
		if err != nil {
			t.Fatalf("HashReader failed: %v", err)
		}

		if actualHash != expectedHash {
			t.Errorf("hash mismatch:\n  expected: %s\n  actual:   %s", expectedHash, actualHash)
		}
	})
}

// TestBehavior_HashMismatchDetection verifies hash mismatches are properly detected
func TestBehavior_HashMismatchDetection(t *testing.T) {
	data := []byte("original data")
	correctHash := computeExpectedHash(data)
	wrongHash := computeExpectedHash([]byte("different data"))

	// Test HashingWriter detects mismatch
	t.Run("hashing_writer_mismatch", func(t *testing.T) {
		var buf bytes.Buffer
		hw := NewHashingWriter(&buf)
		io.Copy(hw, bytes.NewReader(data))

		if hw.Sum() == wrongHash {
			t.Error("should detect hash mismatch")
		}
		if hw.Sum() != correctHash {
			t.Error("should match correct hash")
		}
	})

	// Test HashReader detects mismatch
	t.Run("hash_reader_mismatch", func(t *testing.T) {
		hash, _ := HashReader(bytes.NewReader(data))

		if hash == wrongHash {
			t.Error("should detect hash mismatch")
		}
		if hash != correctHash {
			t.Error("should match correct hash")
		}
	})

	// Test Verify function
	t.Run("verify_function", func(t *testing.T) {
		match, err := Verify(bytes.NewReader(data), correctHash)
		if err != nil {
			t.Fatalf("Verify failed: %v", err)
		}
		if !match {
			t.Error("should verify correct hash")
		}

		match, err = Verify(bytes.NewReader(data), wrongHash)
		if err != nil {
			t.Fatalf("Verify failed: %v", err)
		}
		if match {
			t.Error("should reject wrong hash")
		}
	})

	// Test HashBytes detects mismatch
	t.Run("hash_bytes_mismatch", func(t *testing.T) {
		hash := HashBytes(data)

		if hash == wrongHash {
			t.Error("should detect hash mismatch")
		}
		if hash != correctHash {
			t.Error("should match correct hash")
		}
	})
}

// TestBehavior_IncrementalHashing verifies incremental writes/reads produce correct hash
func TestBehavior_IncrementalHashing(t *testing.T) {
	chunks := [][]byte{
		[]byte("first chunk "),
		[]byte("second chunk "),
		[]byte("third chunk "),
		[]byte("final chunk"),
	}

	var fullData []byte
	for _, chunk := range chunks {
		fullData = append(fullData, chunk...)
	}
	expectedHash := computeExpectedHash(fullData)

	// Test incremental writes
	t.Run("incremental_writes", func(t *testing.T) {
		var buf bytes.Buffer
		hw := NewHashingWriter(&buf)

		for _, chunk := range chunks {
			_, err := hw.Write(chunk)
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}
		}

		if hw.Sum() != expectedHash {
			t.Errorf("hash mismatch:\n  expected: %s\n  actual:   %s", expectedHash, hw.Sum())
		}
	})

	// Test incremental reads
	t.Run("incremental_reads", func(t *testing.T) {
		hr := NewHashingReader(bytes.NewReader(fullData))

		buf := make([]byte, 10)
		for {
			_, err := hr.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Read failed: %v", err)
			}
		}

		if hr.Sum() != expectedHash {
			t.Errorf("hash mismatch:\n  expected: %s\n  actual:   %s", expectedHash, hr.Sum())
		}
	})
}

// TestBehavior_KnownTestVectors verifies against known SHA256 test vectors
func TestBehavior_KnownTestVectors(t *testing.T) {
	// Standard SHA256 test vectors
	vectors := []struct {
		input    string
		expected string
	}{
		{"", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"hello", "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{"The quick brown fox jumps over the lazy dog", "d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592"},
	}

	for _, v := range vectors {
		t.Run(v.input, func(t *testing.T) {
			// Test HashingWriter
			var buf bytes.Buffer
			hw := NewHashingWriter(&buf)
			hw.Write([]byte(v.input))
			if hw.Sum() != v.expected {
				t.Errorf("HashingWriter: got %s, want %s", hw.Sum(), v.expected)
			}

			// Test HashingReader
			hr := NewHashingReader(strings.NewReader(v.input))
			io.ReadAll(hr)
			if hr.Sum() != v.expected {
				t.Errorf("HashingReader: got %s, want %s", hr.Sum(), v.expected)
			}

			// Test HashReader
			hash, _ := HashReader(strings.NewReader(v.input))
			if hash != v.expected {
				t.Errorf("HashReader: got %s, want %s", hash, v.expected)
			}

			// Test HashBytes
			if HashBytes([]byte(v.input)) != v.expected {
				t.Errorf("HashBytes: got %s, want %s", HashBytes([]byte(v.input)), v.expected)
			}
		})
	}
}

// TestBehavior_ConcurrentUsage verifies thread safety
func TestBehavior_ConcurrentUsage(t *testing.T) {
	data := bytes.Repeat([]byte("concurrent test data "), 100)
	expectedHash := computeExpectedHash(data)

	t.Run("concurrent_hash_bytes", func(t *testing.T) {
		done := make(chan bool, 10)

		for i := 0; i < 10; i++ {
			go func() {
				hash := HashBytes(data)
				if hash != expectedHash {
					t.Errorf("hash mismatch in goroutine")
				}
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}
	})

	t.Run("concurrent_hash_reader", func(t *testing.T) {
		done := make(chan bool, 10)

		for i := 0; i < 10; i++ {
			go func() {
				hash, err := HashReader(bytes.NewReader(data))
				if err != nil {
					t.Errorf("HashReader failed: %v", err)
				}
				if hash != expectedHash {
					t.Errorf("hash mismatch in goroutine")
				}
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

func computeExpectedHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
