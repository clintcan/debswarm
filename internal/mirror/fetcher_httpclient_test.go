package mirror

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// Integration tests to verify httpclient integration in mirror/fetcher.go

// TestFetcher_HTTPClientIntegration verifies the fetcher works with httpclient
func TestFetcher_HTTPClientIntegration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test content"))
	}))
	defer server.Close()

	f := NewFetcher(&Config{
		Timeout:     5 * time.Second,
		MaxIdleConn: 10,
		MaxRetries:  1,
	}, zap.NewNop())

	data, err := f.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	if string(data) != "test content" {
		t.Errorf("expected 'test content', got %q", string(data))
	}
}

// TestFetcher_TimeoutWorks verifies the client timeout is respected
func TestFetcher_TimeoutWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	f := NewFetcher(&Config{
		Timeout:    50 * time.Millisecond, // Very short timeout
		MaxRetries: 0,                     // No retries
	}, zap.NewNop())

	start := time.Now()
	_, err := f.Fetch(context.Background(), server.URL)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error")
	}

	// Should timeout quickly, not wait for server
	if elapsed > 300*time.Millisecond {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

// TestFetcher_ConnectionPooling verifies connection reuse works
func TestFetcher_ConnectionPooling(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	f := NewFetcher(&Config{
		Timeout:     5 * time.Second,
		MaxIdleConn: 5,
		MaxRetries:  0,
	}, zap.NewNop())

	// Make multiple requests - should reuse connections
	for i := 0; i < 10; i++ {
		_, err := f.Fetch(context.Background(), server.URL)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}

	if atomic.LoadInt32(&requestCount) != 10 {
		t.Errorf("expected 10 requests, got %d", requestCount)
	}
}

// TestFetcher_ConcurrentRequests verifies concurrent fetches work
func TestFetcher_ConcurrentRequests(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	f := NewFetcher(&Config{
		Timeout:     5 * time.Second,
		MaxIdleConn: 10,
		MaxRetries:  0,
	}, zap.NewNop())

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := f.Fetch(context.Background(), server.URL)
			if err != nil {
				t.Errorf("concurrent fetch failed: %v", err)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	if atomic.LoadInt32(&requestCount) != 10 {
		t.Errorf("expected 10 requests, got %d", requestCount)
	}
}

// TestFetcher_FetchToWriter verifies FetchToWriter works with httpclient
func TestFetcher_FetchToWriter(t *testing.T) {
	content := "large content for testing fetch to writer functionality"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(content))
	}))
	defer server.Close()

	f := NewFetcher(&Config{
		Timeout:     5 * time.Second,
		MaxIdleConn: 10,
		MaxRetries:  1,
	}, zap.NewNop())

	var buf testBuffer
	size, err := f.FetchToWriter(context.Background(), server.URL, &buf)
	if err != nil {
		t.Fatalf("FetchToWriter failed: %v", err)
	}

	if size != int64(len(content)) {
		t.Errorf("size mismatch: got %d, want %d", size, len(content))
	}

	if buf.String() != content {
		t.Errorf("content mismatch: got %q, want %q", buf.String(), content)
	}
}

// TestFetcher_FetchRange verifies FetchRange works with httpclient
func TestFetcher_FetchRange(t *testing.T) {
	content := "0123456789ABCDEF"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "bytes=5-10" {
			w.Header().Set("Content-Range", "bytes 5-10/16")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte(content[5:11]))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(content))
		}
	}))
	defer server.Close()

	f := NewFetcher(&Config{
		Timeout:     5 * time.Second,
		MaxIdleConn: 10,
		MaxRetries:  1,
	}, zap.NewNop())

	data, err := f.FetchRange(context.Background(), server.URL, 5, 10)
	if err != nil {
		t.Fatalf("FetchRange failed: %v", err)
	}

	expected := content[5:11]
	if string(data) != expected {
		t.Errorf("content mismatch: got %q, want %q", string(data), expected)
	}
}

// TestFetcher_DefaultConfig verifies default config works
func TestFetcher_DefaultConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Use nil config - should use defaults
	f := NewFetcher(nil, zap.NewNop())

	data, err := f.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch with default config failed: %v", err)
	}

	if string(data) != "ok" {
		t.Errorf("expected 'ok', got %q", string(data))
	}
}

// testBuffer is a simple buffer for testing
type testBuffer struct {
	data []byte
}

func (b *testBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *testBuffer) String() string {
	return string(b.data)
}

func (b *testBuffer) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}
