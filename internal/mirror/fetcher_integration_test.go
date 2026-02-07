package mirror

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Additional integration tests to verify retry behavior after refactoring

func TestFetch_4xxNoRetry(t *testing.T) {
	// 4xx errors should NOT trigger retries
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest) // 400
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 5, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	_, err := f.Fetch(context.Background(), server.URL)

	if err == nil {
		t.Fatal("expected error for 400")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("4xx should not retry, expected 1 attempt, got %d", attempts)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should contain 400: %v", err)
	}
}

func TestFetch_403Forbidden_NoRetry(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusForbidden) // 403
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 3, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	_, err := f.Fetch(context.Background(), server.URL)

	if err == nil {
		t.Fatal("expected error for 403")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("403 should not retry, expected 1 attempt, got %d", attempts)
	}
}

func TestFetch_502BadGateway_Retries(t *testing.T) {
	// 5xx errors should trigger retries
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 2 {
			w.WriteHeader(http.StatusBadGateway) // 502
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("recovered"))
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 3, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	data, err := f.Fetch(context.Background(), server.URL)

	if err != nil {
		t.Fatalf("expected success after retry: %v", err)
	}
	if string(data) != "recovered" {
		t.Errorf("expected 'recovered', got %q", data)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestFetch_503ServiceUnavailable_Retries(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			w.WriteHeader(http.StatusServiceUnavailable) // 503
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("available now"))
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 5, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	data, err := f.Fetch(context.Background(), server.URL)

	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if string(data) != "available now" {
		t.Errorf("expected 'available now', got %q", data)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestFetch_MaxResponseSize_NoRetry(t *testing.T) {
	// Exceeding max response size should not retry
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusOK)
		// Write more than the limit
		w.Write(make([]byte, 200))
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 5, Timeout: 10 * time.Second, MaxResponseSize: 100}
	f := NewFetcher(cfg, testLogger())
	_, err := f.Fetch(context.Background(), server.URL)

	if err == nil {
		t.Fatal("expected error for exceeding size limit")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("size limit should not retry, expected 1 attempt, got %d", attempts)
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("error should mention size limit: %v", err)
	}
}

func TestFetchToWriter_5xxNoRetry(t *testing.T) {
	// FetchToWriter does NOT retry because the writer cannot be rewound.
	// A 5xx error should fail immediately.
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 3, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())

	var buf strings.Builder
	_, err := f.FetchToWriter(context.Background(), server.URL, &buf)

	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected 1 attempt (no retries), got %d", attempts)
	}
}

func TestFetchToWriter_4xxNoRetry(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusNotFound) // 404
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 5, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())

	var buf strings.Builder
	_, err := f.FetchToWriter(context.Background(), server.URL, &buf)

	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("4xx should not retry, expected 1 attempt, got %d", attempts)
	}
}

func TestFetchRange_5xxRetries(t *testing.T) {
	var attempts int32
	content := "0123456789ABCDEF"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Range", "bytes 5-9/16")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte(content[5:10]))
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 3, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	data, err := f.FetchRange(context.Background(), server.URL, 5, 9)

	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if string(data) != "56789" {
		t.Errorf("expected '56789', got %q", data)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestFetchRange_4xxNoRetry(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable) // 416
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 5, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	_, err := f.FetchRange(context.Background(), server.URL, 1000, 2000)

	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("4xx should not retry, expected 1 attempt, got %d", attempts)
	}
}

func TestFetch_StatsRecordedCorrectly(t *testing.T) {
	// Verify stats are recorded on each failed attempt and on success
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 5, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	_, err := f.Fetch(context.Background(), server.URL)

	if err != nil {
		t.Fatalf("expected success: %v", err)
	}

	stats := f.GetMirrorStats(server.URL)
	if stats == nil {
		t.Fatal("expected stats to be recorded")
	}

	// Should have 2 errors (attempts 1 and 2) and 1 success
	if stats.ErrorCount != 2 {
		t.Errorf("expected 2 errors, got %d", stats.ErrorCount)
	}
	if stats.SuccessCount != 1 {
		t.Errorf("expected 1 success, got %d", stats.SuccessCount)
	}
}

func TestFetch_BackoffTiming(t *testing.T) {
	// Verify that backoff is applied between retries
	// With 2 retries, backoff between attempt 1 and 2 is 1*1 = 1 second
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 2, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())

	start := time.Now()
	_, err := f.Fetch(context.Background(), server.URL)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}

	// With exponential backoff (1*1 = 1 second between attempt 0 and 1)
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected ~1s backoff, got %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("backoff took too long: %v", elapsed)
	}
}

func TestFetch_ContextCancelledBeforeRetry(t *testing.T) {
	var attempts int32
	ctx, cancel := context.WithCancel(context.Background())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count == 1 {
			// Cancel after first attempt
			go func() {
				time.Sleep(10 * time.Millisecond)
				cancel()
			}()
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 5, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	_, err := f.Fetch(ctx, server.URL)

	if err == nil {
		t.Fatal("expected error")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected 1 attempt before cancellation, got %d", attempts)
	}
}

func TestFetchToWriter_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately before the request

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data"))
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 5, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())

	var buf strings.Builder
	_, err := f.FetchToWriter(ctx, server.URL, &buf)

	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestFetch_ReadBodyError_Retries(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 2 {
			// Send headers but close connection before body
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusOK)
			// Connection will be closed without sending full body
			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, _ := hijacker.Hijack()
				conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("complete"))
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 3, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	data, err := f.Fetch(context.Background(), server.URL)

	if err != nil {
		t.Fatalf("expected success after retry: %v", err)
	}
	if string(data) != "complete" {
		t.Errorf("expected 'complete', got %q", data)
	}
}

// Test that FetchToWriter properly handles partial writes
func TestFetchToWriter_PartialWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello world"))
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 3, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())

	// Use a limited writer that fails after some bytes
	limitedWriter := &limitedWriter{limit: 5}
	_, err := f.FetchToWriter(context.Background(), server.URL, limitedWriter)

	// Should get an error from the writer
	if err == nil {
		t.Fatal("expected error from limited writer")
	}
}

type limitedWriter struct {
	limit   int
	written int
}

func (w *limitedWriter) Write(p []byte) (n int, err error) {
	remaining := w.limit - w.written
	if remaining <= 0 {
		return 0, io.ErrShortWrite
	}
	if len(p) > remaining {
		w.written += remaining
		return remaining, io.ErrShortWrite
	}
	w.written += len(p)
	return len(p), nil
}
