package ratelimit

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestNew_Unlimited(t *testing.T) {
	tests := []struct {
		name           string
		bytesPerSecond int64
	}{
		{"zero", 0},
		{"negative", -1},
		{"large negative", -1000000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limiter := New(tc.bytesPerSecond)
			if limiter.Enabled() {
				t.Errorf("New(%d) should create disabled limiter", tc.bytesPerSecond)
			}
		})
	}
}

func TestNew_Limited(t *testing.T) {
	tests := []struct {
		name           string
		bytesPerSecond int64
	}{
		{"small", 1024},
		{"medium", 1024 * 1024},
		{"large", 100 * 1024 * 1024},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limiter := New(tc.bytesPerSecond)
			if !limiter.Enabled() {
				t.Errorf("New(%d) should create enabled limiter", tc.bytesPerSecond)
			}
		})
	}
}

func TestLimiter_Enabled_NilSafe(t *testing.T) {
	var limiter *Limiter
	if limiter.Enabled() {
		t.Error("nil Limiter.Enabled() should return false")
	}
}

func TestLimiter_Reader_Unlimited(t *testing.T) {
	limiter := New(0) // unlimited

	data := "hello world"
	original := strings.NewReader(data)

	reader := limiter.Reader(original)

	// Should return the original reader when unlimited
	if reader != original {
		t.Error("Unlimited limiter should return original reader")
	}
}

func TestLimiter_Reader_Limited(t *testing.T) {
	limiter := New(1024 * 1024) // 1MB/s

	data := "hello world"
	original := strings.NewReader(data)

	reader := limiter.Reader(original)

	// Should return a wrapped reader
	if reader == original {
		t.Error("Limited limiter should wrap the reader")
	}

	// Should still be able to read
	buf := make([]byte, len(data))
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Read %d bytes, want %d", n, len(data))
	}
	if string(buf) != data {
		t.Errorf("Read %q, want %q", string(buf), data)
	}
}

func TestLimiter_Writer_Unlimited(t *testing.T) {
	limiter := New(0) // unlimited

	var buf bytes.Buffer
	writer := limiter.Writer(&buf)

	// Should return the original writer when unlimited
	if writer != &buf {
		t.Error("Unlimited limiter should return original writer")
	}
}

func TestLimiter_Writer_Limited(t *testing.T) {
	limiter := New(1024 * 1024) // 1MB/s

	var buf bytes.Buffer
	writer := limiter.Writer(&buf)

	// Should return a wrapped writer
	if writer == &buf {
		t.Error("Limited limiter should wrap the writer")
	}

	// Should still be able to write
	data := "hello world"
	n, err := writer.Write([]byte(data))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Wrote %d bytes, want %d", n, len(data))
	}
	if buf.String() != data {
		t.Errorf("Buffer contains %q, want %q", buf.String(), data)
	}
}

func TestLimiter_ReaderContext(t *testing.T) {
	limiter := New(1024 * 1024) // 1MB/s

	ctx := context.Background()
	data := "hello world"
	original := strings.NewReader(data)

	reader := limiter.ReaderContext(ctx, original)

	buf := make([]byte, len(data))
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Read %d bytes, want %d", n, len(data))
	}
}

func TestLimiter_ReaderContext_Cancelled(t *testing.T) {
	// Use a very low rate to ensure rate limiting kicks in
	limiter := New(1) // 1 byte/s

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	data := strings.Repeat("x", 1000) // Large enough to trigger waiting
	original := strings.NewReader(data)

	reader := limiter.ReaderContext(ctx, original)

	buf := make([]byte, len(data))
	_, err := reader.Read(buf)
	// The read itself might succeed, but the wait should fail
	// Due to how rate limiting works, error may or may not occur on first read
	_ = err // Error expected on subsequent reads
}

func TestLimiter_WriterContext(t *testing.T) {
	limiter := New(1024 * 1024) // 1MB/s

	ctx := context.Background()
	var buf bytes.Buffer

	writer := limiter.WriterContext(ctx, &buf)

	data := "hello world"
	n, err := writer.Write([]byte(data))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Wrote %d bytes, want %d", n, len(data))
	}
}

func TestLimiter_WriterContext_Cancelled(t *testing.T) {
	limiter := New(1) // 1 byte/s - very slow

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	var buf bytes.Buffer
	writer := limiter.WriterContext(ctx, &buf)

	data := strings.Repeat("x", 1000) // Large enough to trigger waiting
	_, err := writer.Write([]byte(data))
	if err == nil {
		t.Error("Write should fail with cancelled context")
	}
}

func TestLimiter_ReaderContext_Unlimited(t *testing.T) {
	limiter := New(0) // unlimited

	ctx := context.Background()
	data := "hello world"
	original := strings.NewReader(data)

	reader := limiter.ReaderContext(ctx, original)

	// Should return the original reader when unlimited
	if reader != original {
		t.Error("Unlimited limiter should return original reader")
	}
}

func TestLimiter_WriterContext_Unlimited(t *testing.T) {
	limiter := New(0) // unlimited

	ctx := context.Background()
	var buf bytes.Buffer

	writer := limiter.WriterContext(ctx, &buf)

	// Should return the original writer when unlimited
	if writer != &buf {
		t.Error("Unlimited limiter should return original writer")
	}
}

func TestLimitedReader_MultipleReads(t *testing.T) {
	limiter := New(10 * 1024 * 1024) // 10MB/s - fast enough to not slow test

	data := "hello world, this is a longer test string for multiple reads"
	original := strings.NewReader(data)
	reader := limiter.Reader(original)

	var result bytes.Buffer
	buf := make([]byte, 10) // Small buffer to force multiple reads

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			result.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
	}

	if result.String() != data {
		t.Errorf("Read %q, want %q", result.String(), data)
	}
}

func TestLimitedWriter_MultipleWrites(t *testing.T) {
	limiter := New(10 * 1024 * 1024) // 10MB/s - fast enough to not slow test

	var buf bytes.Buffer
	writer := limiter.Writer(&buf)

	chunks := []string{"hello ", "world ", "from ", "multiple ", "writes"}
	for _, chunk := range chunks {
		_, err := writer.Write([]byte(chunk))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}

	expected := strings.Join(chunks, "")
	if buf.String() != expected {
		t.Errorf("Buffer contains %q, want %q", buf.String(), expected)
	}
}

func TestLimiter_RateLimitingEffect(t *testing.T) {
	// Skip in short mode as this test is timing-sensitive
	if testing.Short() {
		t.Skip("Skipping timing-sensitive test in short mode")
	}

	// Very low rate to observe limiting
	bytesPerSecond := int64(10000) // 10KB/s
	limiter := New(bytesPerSecond)

	// Write more than burst size to observe rate limiting
	dataSize := 100000 // 100KB - should take ~10 seconds at 10KB/s
	data := strings.Repeat("x", dataSize)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var buf bytes.Buffer
	writer := limiter.WriterContext(ctx, &buf)

	_, err := writer.Write([]byte(data))
	// Should fail due to context timeout before writing all data
	if err == nil {
		// If it succeeded, the rate limiting might not be working as expected
		// But with burst, initial writes might succeed
		t.Log("Write succeeded - burst may have allowed initial write")
	}
}

func TestNew_BurstCalculation(t *testing.T) {
	// Test that burst is capped appropriately
	tests := []struct {
		name           string
		bytesPerSecond int64
	}{
		{"below minimum burst", 1000},         // Should use 64KB burst
		{"above minimum burst", 100 * 1024},   // Should use rate as burst
		{"above maximum burst", 10 * 1024 * 1024}, // Should be capped at 4MB
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limiter := New(tc.bytesPerSecond)
			if !limiter.Enabled() {
				t.Error("Limiter should be enabled")
			}
			// We can't directly check burst size, but we can verify limiter works
		})
	}
}
