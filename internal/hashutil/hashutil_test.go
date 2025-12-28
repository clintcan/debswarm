package hashutil

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// Known test vectors
const (
	emptyHash  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	helloHash  = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // "hello"
	helloWorld = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9" // "hello world"
)

func TestHashingWriter_Empty(t *testing.T) {
	var buf bytes.Buffer
	hw := NewHashingWriter(&buf)

	if hw.Sum() != emptyHash {
		t.Errorf("empty hash mismatch: got %s, want %s", hw.Sum(), emptyHash)
	}
}

func TestHashingWriter_SingleWrite(t *testing.T) {
	var buf bytes.Buffer
	hw := NewHashingWriter(&buf)

	n, err := hw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != 5 {
		t.Errorf("Write returned %d, want 5", n)
	}

	if buf.String() != "hello" {
		t.Errorf("underlying buffer got %q, want %q", buf.String(), "hello")
	}

	if hw.Sum() != helloHash {
		t.Errorf("hash mismatch: got %s, want %s", hw.Sum(), helloHash)
	}
}

func TestHashingWriter_MultipleWrites(t *testing.T) {
	var buf bytes.Buffer
	hw := NewHashingWriter(&buf)

	hw.Write([]byte("hello"))
	hw.Write([]byte(" "))
	hw.Write([]byte("world"))

	if buf.String() != "hello world" {
		t.Errorf("underlying buffer got %q, want %q", buf.String(), "hello world")
	}

	if hw.Sum() != helloWorld {
		t.Errorf("hash mismatch: got %s, want %s", hw.Sum(), helloWorld)
	}
}

func TestHashingWriter_PartialWrite(t *testing.T) {
	// Writer that only accepts 3 bytes at a time
	limited := &limitedWriter{limit: 3}
	hw := NewHashingWriter(limited)

	n, err := hw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != 3 {
		t.Errorf("Write returned %d, want 3", n)
	}

	// Hash should only include what was actually written
	expected := computeHash([]byte("hel"))
	if hw.Sum() != expected {
		t.Errorf("hash mismatch after partial write: got %s, want %s", hw.Sum(), expected)
	}
}

func TestHashingWriter_ErrorPropagation(t *testing.T) {
	errWriter := &errorWriter{err: errors.New("write error")}
	hw := NewHashingWriter(errWriter)

	_, err := hw.Write([]byte("test"))
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestHashingReader_Empty(t *testing.T) {
	hr := NewHashingReader(strings.NewReader(""))

	buf := make([]byte, 10)
	n, err := hr.Read(buf)
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}

	if hr.Sum() != emptyHash {
		t.Errorf("empty hash mismatch: got %s, want %s", hr.Sum(), emptyHash)
	}
}

func TestHashingReader_SingleRead(t *testing.T) {
	hr := NewHashingReader(strings.NewReader("hello"))

	buf := make([]byte, 10)
	n, err := hr.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read failed: %v", err)
	}
	if n != 5 {
		t.Errorf("Read returned %d, want 5", n)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("Read got %q, want %q", string(buf[:n]), "hello")
	}

	if hr.Sum() != helloHash {
		t.Errorf("hash mismatch: got %s, want %s", hr.Sum(), helloHash)
	}
}

func TestHashingReader_MultipleReads(t *testing.T) {
	hr := NewHashingReader(strings.NewReader("hello world"))

	// Read in small chunks
	var result []byte
	buf := make([]byte, 3)
	for {
		n, err := hr.Read(buf)
		if n > 0 {
			result = append(result, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
	}

	if string(result) != "hello world" {
		t.Errorf("Read got %q, want %q", string(result), "hello world")
	}

	if hr.Sum() != helloWorld {
		t.Errorf("hash mismatch: got %s, want %s", hr.Sum(), helloWorld)
	}
}

func TestHashingReader_WithCopy(t *testing.T) {
	hr := NewHashingReader(strings.NewReader("hello world"))

	var buf bytes.Buffer
	_, err := io.Copy(&buf, hr)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	if buf.String() != "hello world" {
		t.Errorf("Copy got %q, want %q", buf.String(), "hello world")
	}

	if hr.Sum() != helloWorld {
		t.Errorf("hash mismatch: got %s, want %s", hr.Sum(), helloWorld)
	}
}

func TestHashReader_Success(t *testing.T) {
	hash, err := HashReader(strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("HashReader failed: %v", err)
	}

	if hash != helloWorld {
		t.Errorf("hash mismatch: got %s, want %s", hash, helloWorld)
	}
}

func TestHashReader_Empty(t *testing.T) {
	hash, err := HashReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("HashReader failed: %v", err)
	}

	if hash != emptyHash {
		t.Errorf("hash mismatch: got %s, want %s", hash, emptyHash)
	}
}

func TestHashReader_Error(t *testing.T) {
	_, err := HashReader(&errorReader{err: errors.New("read error")})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestVerify_Match(t *testing.T) {
	match, err := Verify(strings.NewReader("hello world"), helloWorld)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !match {
		t.Error("expected match, got mismatch")
	}
}

func TestVerify_Mismatch(t *testing.T) {
	match, err := Verify(strings.NewReader("hello world"), helloHash)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if match {
		t.Error("expected mismatch, got match")
	}
}

func TestVerify_Error(t *testing.T) {
	_, err := Verify(&errorReader{err: errors.New("read error")}, helloHash)
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestHashingWriter_SumMultipleCalls(t *testing.T) {
	var buf bytes.Buffer
	hw := NewHashingWriter(&buf)

	hw.Write([]byte("hello"))

	// Sum should be consistent across multiple calls
	sum1 := hw.Sum()
	sum2 := hw.Sum()

	if sum1 != sum2 {
		t.Errorf("Sum() not consistent: %s != %s", sum1, sum2)
	}
	if sum1 != helloHash {
		t.Errorf("hash mismatch: got %s, want %s", sum1, helloHash)
	}
}

func TestHashingReader_SumMultipleCalls(t *testing.T) {
	hr := NewHashingReader(strings.NewReader("hello"))
	io.ReadAll(hr)

	// Sum should be consistent across multiple calls
	sum1 := hr.Sum()
	sum2 := hr.Sum()

	if sum1 != sum2 {
		t.Errorf("Sum() not consistent: %s != %s", sum1, sum2)
	}
	if sum1 != helloHash {
		t.Errorf("hash mismatch: got %s, want %s", sum1, helloHash)
	}
}

func TestHashingWriter_LargeData(t *testing.T) {
	// Test with 1MB of data
	data := make([]byte, 1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	var buf bytes.Buffer
	hw := NewHashingWriter(&buf)

	n, err := hw.Write(data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Write returned %d, want %d", n, len(data))
	}

	expected := computeHash(data)
	if hw.Sum() != expected {
		t.Errorf("hash mismatch for large data")
	}
}

func TestHashingReader_LargeData(t *testing.T) {
	// Test with 1MB of data
	data := make([]byte, 1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	hr := NewHashingReader(bytes.NewReader(data))
	_, err := io.ReadAll(hr)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	expected := computeHash(data)
	if hr.Sum() != expected {
		t.Errorf("hash mismatch for large data")
	}
}

func TestHashBytes(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected string
	}{
		{"empty", []byte{}, emptyHash},
		{"hello", []byte("hello"), helloHash},
		{"hello world", []byte("hello world"), helloWorld},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HashBytes(tt.data)
			if got != tt.expected {
				t.Errorf("HashBytes() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestHashBytes_ConsistentWithHashReader(t *testing.T) {
	data := []byte("test data for consistency check")

	bytesHash := HashBytes(data)
	readerHash, err := HashReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("HashReader failed: %v", err)
	}

	if bytesHash != readerHash {
		t.Errorf("HashBytes and HashReader produce different results: %s != %s", bytesHash, readerHash)
	}
}

// Helper functions and types

func computeHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

type limitedWriter struct {
	limit int
	buf   bytes.Buffer
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > lw.limit {
		p = p[:lw.limit]
	}
	return lw.buf.Write(p)
}

type errorWriter struct {
	err error
}

func (ew *errorWriter) Write(p []byte) (int, error) {
	return 0, ew.err
}

type errorReader struct {
	err error
}

func (er *errorReader) Read(p []byte) (int, error) {
	return 0, er.err
}
