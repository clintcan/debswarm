// Package hashutil provides utilities for computing SHA256 hashes during I/O operations.
package hashutil

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

// HashingWriter wraps an io.Writer and computes a SHA256 hash of all data written.
type HashingWriter struct {
	w      io.Writer
	hasher hash.Hash
}

// NewHashingWriter creates a new HashingWriter that writes to w while computing a hash.
func NewHashingWriter(w io.Writer) *HashingWriter {
	return &HashingWriter{
		w:      w,
		hasher: sha256.New(),
	}
}

// Write writes p to the underlying writer and updates the hash.
func (hw *HashingWriter) Write(p []byte) (int, error) {
	n, err := hw.w.Write(p)
	if n > 0 {
		hw.hasher.Write(p[:n])
	}
	return n, err
}

// Sum returns the hex-encoded SHA256 hash of all data written so far.
func (hw *HashingWriter) Sum() string {
	return hex.EncodeToString(hw.hasher.Sum(nil))
}

// HashingReader wraps an io.Reader and computes a SHA256 hash of all data read.
type HashingReader struct {
	r      io.Reader
	hasher hash.Hash
}

// NewHashingReader creates a new HashingReader that reads from r while computing a hash.
func NewHashingReader(r io.Reader) *HashingReader {
	return &HashingReader{
		r:      r,
		hasher: sha256.New(),
	}
}

// Read reads from the underlying reader and updates the hash.
func (hr *HashingReader) Read(p []byte) (int, error) {
	n, err := hr.r.Read(p)
	if n > 0 {
		hr.hasher.Write(p[:n])
	}
	return n, err
}

// Sum returns the hex-encoded SHA256 hash of all data read so far.
func (hr *HashingReader) Sum() string {
	return hex.EncodeToString(hr.hasher.Sum(nil))
}

// HashReader reads all data from r and returns the hex-encoded SHA256 hash.
// This is useful for verifying file contents after assembly.
func HashReader(r io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// Verify reads all data from r and returns true if the hash matches expectedHash.
func Verify(r io.Reader, expectedHash string) (bool, error) {
	actualHash, err := HashReader(r)
	if err != nil {
		return false, err
	}
	return actualHash == expectedHash, nil
}

// HashBytes returns the hex-encoded SHA256 hash of the given byte slice.
func HashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
