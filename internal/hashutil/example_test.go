package hashutil_test

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/debswarm/debswarm/internal/hashutil"
)

func ExampleHashingWriter() {
	// Create a buffer to write to
	var buf bytes.Buffer

	// Wrap it with HashingWriter
	hw := hashutil.NewHashingWriter(&buf)

	// Write some data
	hw.Write([]byte("hello world"))

	// Get the hash of what was written
	hash := hw.Sum()

	fmt.Printf("Written: %s\n", buf.String())
	fmt.Printf("Hash: %s\n", hash)
	// Output:
	// Written: hello world
	// Hash: b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9
}

func ExampleHashingReader() {
	// Create a reader with some data
	data := "hello world"
	r := strings.NewReader(data)

	// Wrap it with HashingReader
	hr := hashutil.NewHashingReader(r)

	// Read all the data
	result, _ := io.ReadAll(hr)

	// Get the hash of what was read
	hash := hr.Sum()

	fmt.Printf("Read: %s\n", string(result))
	fmt.Printf("Hash: %s\n", hash)
	// Output:
	// Read: hello world
	// Hash: b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9
}

func ExampleHashingWriter_streaming() {
	// HashingWriter is useful for computing hash while streaming to disk
	var buf bytes.Buffer
	hw := hashutil.NewHashingWriter(&buf)

	// Simulate streaming writes
	hw.Write([]byte("chunk1"))
	hw.Write([]byte("chunk2"))
	hw.Write([]byte("chunk3"))

	// Hash is computed incrementally
	fmt.Printf("Final hash: %s\n", hw.Sum()[:16]+"...")
	// Output: Final hash: bfe08b41e4577d49...
}

func ExampleHashReader() {
	// Compute hash of an entire reader
	r := strings.NewReader("hello world")
	hash, err := hashutil.HashReader(r)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Hash: %s\n", hash)
	// Output: Hash: b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9
}

func ExampleVerify() {
	data := "hello world"
	expectedHash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	// Verify the data matches the expected hash
	ok, err := hashutil.Verify(strings.NewReader(data), expectedHash)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Valid: %v\n", ok)
	// Output: Valid: true
}

func ExampleVerify_mismatch() {
	data := "tampered data"
	expectedHash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	ok, _ := hashutil.Verify(strings.NewReader(data), expectedHash)
	fmt.Printf("Valid: %v\n", ok)
	// Output: Valid: false
}

func ExampleHashBytes() {
	data := []byte("hello world")
	hash := hashutil.HashBytes(data)

	fmt.Printf("Hash: %s\n", hash)
	// Output: Hash: b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9
}
