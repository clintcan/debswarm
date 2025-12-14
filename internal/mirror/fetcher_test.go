package mirror

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	return zap.NewNop()
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Timeout != 60*time.Second {
		t.Errorf("Expected timeout 60s, got %v", cfg.Timeout)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("Expected max retries 3, got %d", cfg.MaxRetries)
	}
	if cfg.MaxResponseSize != DefaultMaxResponseSize {
		t.Errorf("Expected max response size %d, got %d", DefaultMaxResponseSize, cfg.MaxResponseSize)
	}
}

func TestNewFetcher(t *testing.T) {
	f := NewFetcher(nil, testLogger())

	if f == nil {
		t.Fatal("NewFetcher returned nil")
	}
	if f.maxRetries != 3 {
		t.Errorf("Expected default max retries 3, got %d", f.maxRetries)
	}
	if f.maxResponseSize != DefaultMaxResponseSize {
		t.Errorf("Expected default max response size, got %d", f.maxResponseSize)
	}
}

func TestNewFetcherWithConfig(t *testing.T) {
	cfg := &Config{
		Timeout:         30 * time.Second,
		MaxRetries:      5,
		UserAgent:       "test-agent",
		MaxResponseSize: 1024,
	}

	f := NewFetcher(cfg, testLogger())

	if f.maxRetries != 5 {
		t.Errorf("Expected max retries 5, got %d", f.maxRetries)
	}
	if f.userAgent != "test-agent" {
		t.Errorf("Expected user agent 'test-agent', got %s", f.userAgent)
	}
	if f.maxResponseSize != 1024 {
		t.Errorf("Expected max response size 1024, got %d", f.maxResponseSize)
	}
}

func TestFetchSuccess(t *testing.T) {
	expectedBody := []byte("test content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(expectedBody)
	}))
	defer server.Close()

	f := NewFetcher(nil, testLogger())
	data, err := f.Fetch(context.Background(), server.URL+"/test.deb")

	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if !bytes.Equal(data, expectedBody) {
		t.Errorf("Expected %s, got %s", expectedBody, data)
	}
}

func TestFetchUserAgent(t *testing.T) {
	var receivedUA string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	cfg := &Config{
		UserAgent:  "custom-agent/1.0",
		MaxRetries: 1,
	}
	f := NewFetcher(cfg, testLogger())
	_, err := f.Fetch(context.Background(), server.URL)

	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if receivedUA != "custom-agent/1.0" {
		t.Errorf("Expected User-Agent 'custom-agent/1.0', got '%s'", receivedUA)
	}
}

func TestFetch404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 1}
	f := NewFetcher(cfg, testLogger())
	_, err := f.Fetch(context.Background(), server.URL)

	if err == nil {
		t.Fatal("Expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("Expected 404 in error, got: %v", err)
	}
}

func TestFetch500WithRetry(t *testing.T) {
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

	cfg := &Config{MaxRetries: 3, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	data, err := f.Fetch(context.Background(), server.URL)

	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if string(data) != "success" {
		t.Errorf("Expected 'success', got '%s'", data)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

func TestFetchAllRetriesFail(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 2, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())
	_, err := f.Fetch(context.Background(), server.URL)

	if err == nil {
		t.Fatal("Expected error after all retries fail")
	}
	if !strings.Contains(err.Error(), "failed after 2 retries") {
		t.Errorf("Expected retry failure message, got: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("Expected 2 attempts, got %d", attempts)
	}
}

func TestFetchContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 1, Timeout: 10 * time.Second}
	f := NewFetcher(cfg, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := f.Fetch(ctx, server.URL)

	if err == nil {
		t.Fatal("Expected context cancellation error")
	}
}

func TestFetchMaxResponseSize(t *testing.T) {
	largeBody := bytes.Repeat([]byte("x"), 2000)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(largeBody)
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 1, MaxResponseSize: 1000}
	f := NewFetcher(cfg, testLogger())
	_, err := f.Fetch(context.Background(), server.URL)

	if err == nil {
		t.Fatal("Expected error for exceeding max response size")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("Expected size exceeded error, got: %v", err)
	}
}

func TestFetchToWriter(t *testing.T) {
	expectedBody := []byte("test content for writer")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(expectedBody)
	}))
	defer server.Close()

	f := NewFetcher(nil, testLogger())
	var buf bytes.Buffer
	written, err := f.FetchToWriter(context.Background(), server.URL, &buf)

	if err != nil {
		t.Fatalf("FetchToWriter failed: %v", err)
	}
	if written != int64(len(expectedBody)) {
		t.Errorf("Expected %d bytes written, got %d", len(expectedBody), written)
	}
	if !bytes.Equal(buf.Bytes(), expectedBody) {
		t.Errorf("Expected %s, got %s", expectedBody, buf.Bytes())
	}
}

func TestFetchToWriter404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 1}
	f := NewFetcher(cfg, testLogger())
	var buf bytes.Buffer
	_, err := f.FetchToWriter(context.Background(), server.URL, &buf)

	if err == nil {
		t.Fatal("Expected error for 404")
	}
}

func TestStream(t *testing.T) {
	expectedBody := []byte("streaming content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "17")
		w.WriteHeader(http.StatusOK)
		w.Write(expectedBody)
	}))
	defer server.Close()

	f := NewFetcher(nil, testLogger())
	reader, contentLength, err := f.Stream(context.Background(), server.URL)

	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	defer reader.Close()

	if contentLength != 17 {
		t.Errorf("Expected content length 17, got %d", contentLength)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Failed to read stream: %v", err)
	}
	if !bytes.Equal(data, expectedBody) {
		t.Errorf("Expected %s, got %s", expectedBody, data)
	}
}

func TestStream404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	f := NewFetcher(nil, testLogger())
	_, _, err := f.Stream(context.Background(), server.URL)

	if err == nil {
		t.Fatal("Expected error for 404")
	}
}

func TestHead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("Expected HEAD method, got %s", r.Method)
		}
		w.Header().Set("Content-Length", "12345")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	f := NewFetcher(nil, testLogger())
	resp, err := f.Head(context.Background(), server.URL)

	if err != nil {
		t.Fatalf("Head failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Length") != "12345" {
		t.Errorf("Expected Content-Length 12345, got %s", resp.Header.Get("Content-Length"))
	}
}

func TestStatsTracking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test"))
	}))
	defer server.Close()

	f := NewFetcher(nil, testLogger())

	// Make a few successful requests
	for i := 0; i < 3; i++ {
		_, err := f.Fetch(context.Background(), server.URL)
		if err != nil {
			t.Fatalf("Fetch failed: %v", err)
		}
	}

	stats := f.GetStats()
	if len(stats) != 1 {
		t.Fatalf("Expected 1 stats entry, got %d", len(stats))
	}

	if stats[0].SuccessCount != 3 {
		t.Errorf("Expected 3 successes, got %d", stats[0].SuccessCount)
	}
	if stats[0].ErrorCount != 0 {
		t.Errorf("Expected 0 errors, got %d", stats[0].ErrorCount)
	}
}

func TestStatsTrackingErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &Config{MaxRetries: 1}
	f := NewFetcher(cfg, testLogger())

	// Make a failing request
	_, _ = f.Fetch(context.Background(), server.URL)

	stats := f.GetMirrorStats(server.URL)
	if stats == nil {
		t.Fatal("Expected stats to exist")
	}
	if stats.ErrorCount != 1 {
		t.Errorf("Expected 1 error, got %d", stats.ErrorCount)
	}
}

func TestGetMirrorStatsNotFound(t *testing.T) {
	f := NewFetcher(nil, testLogger())
	stats := f.GetMirrorStats("http://nonexistent.example.com/")

	if stats != nil {
		t.Error("Expected nil for nonexistent mirror")
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"http://example.com/path", "example.com"},
		{"https://example.com/path", "example.com"},
		{"http://example.com:8080/path", "example.com"},
		{"https://sub.example.com/path/to/file", "sub.example.com"},
		{"http://127.0.0.1/test", "127.0.0.1"},
		{"example.com/path", "example.com"},
	}

	for _, tt := range tests {
		result := extractHost(tt.url)
		if result != tt.expected {
			t.Errorf("extractHost(%q) = %q, want %q", tt.url, result, tt.expected)
		}
	}
}

func TestStatsCopyOnReturn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test"))
	}))
	defer server.Close()

	f := NewFetcher(nil, testLogger())
	_, _ = f.Fetch(context.Background(), server.URL)

	// Get stats and modify the returned copy
	stats := f.GetStats()
	stats[0].SuccessCount = 999

	// Original should be unchanged
	originalStats := f.GetStats()
	if originalStats[0].SuccessCount == 999 {
		t.Error("Stats modification affected original")
	}
}

func TestConcurrentFetch(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	f := NewFetcher(nil, testLogger())

	// Run concurrent fetches
	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := f.Fetch(context.Background(), server.URL)
			done <- err
		}()
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		if err := <-done; err != nil {
			t.Errorf("Concurrent fetch failed: %v", err)
		}
	}

	if atomic.LoadInt32(&requests) != 10 {
		t.Errorf("Expected 10 requests, got %d", requests)
	}

	stats := f.GetStats()
	if len(stats) != 1 || stats[0].SuccessCount != 10 {
		t.Errorf("Expected 10 successes in stats")
	}
}
