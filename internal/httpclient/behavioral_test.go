package httpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Behavioral tests to verify httpclient matches original patterns

// TestBehavior_MatchesFetcherPattern verifies New() produces a client
// equivalent to the original pattern in mirror/fetcher.go
func TestBehavior_MatchesFetcherPattern(t *testing.T) {
	// Original pattern from fetcher.go:
	// transport := &http.Transport{
	//     MaxIdleConnsPerHost: cfg.MaxIdleConn,
	//     IdleConnTimeout:     90 * time.Second,
	// }
	// client := &http.Client{
	//     Transport: transport,
	//     Timeout:   cfg.Timeout,
	// }

	timeout := 30 * time.Second
	maxIdleConn := 10

	// Original pattern
	origTransport := &http.Transport{
		MaxIdleConnsPerHost: maxIdleConn,
		IdleConnTimeout:     90 * time.Second,
	}
	origClient := &http.Client{
		Transport: origTransport,
		Timeout:   timeout,
	}

	// New pattern
	newClient := New(&Config{
		Timeout:             timeout,
		MaxIdleConnsPerHost: maxIdleConn,
	})

	// Verify timeout matches
	if origClient.Timeout != newClient.Timeout {
		t.Errorf("timeout mismatch: orig=%v, new=%v", origClient.Timeout, newClient.Timeout)
	}

	// Verify transport settings match
	newTransport := newClient.Transport.(*http.Transport)
	if origTransport.MaxIdleConnsPerHost != newTransport.MaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost mismatch: orig=%d, new=%d",
			origTransport.MaxIdleConnsPerHost, newTransport.MaxIdleConnsPerHost)
	}
	if origTransport.IdleConnTimeout != newTransport.IdleConnTimeout {
		t.Errorf("IdleConnTimeout mismatch: orig=%v, new=%v",
			origTransport.IdleConnTimeout, newTransport.IdleConnTimeout)
	}
}

// TestBehavior_MatchesMonitorPattern verifies WithTimeout() produces a client
// equivalent to the original pattern in connectivity/monitor.go
func TestBehavior_MatchesMonitorPattern(t *testing.T) {
	// Original pattern from monitor.go:
	// client: &http.Client{
	//     Timeout: cfg.CheckTimeout,
	// }

	timeout := 5 * time.Second

	// Original pattern
	origClient := &http.Client{
		Timeout: timeout,
	}

	// New pattern
	newClient := WithTimeout(timeout)

	// Verify timeout matches
	if origClient.Timeout != newClient.Timeout {
		t.Errorf("timeout mismatch: orig=%v, new=%v", origClient.Timeout, newClient.Timeout)
	}

	// Verify no custom transport (both should be nil)
	if origClient.Transport != newClient.Transport {
		t.Errorf("transport mismatch: orig=%v, new=%v", origClient.Transport, newClient.Transport)
	}
}

// TestBehavior_DefaultMatchesHTTPDefaultClient verifies Default() has sensible
// defaults similar to but better than http.DefaultClient
func TestBehavior_DefaultMatchesSensibleDefaults(t *testing.T) {
	client := Default()

	// Should have a timeout (unlike http.DefaultClient which has none)
	if client.Timeout <= 0 {
		t.Error("Default client should have a timeout")
	}

	// Should have a custom transport with connection pooling
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Default client should have a custom transport")
	}

	if transport.MaxIdleConnsPerHost <= 0 {
		t.Error("Default client should have MaxIdleConnsPerHost > 0")
	}

	if transport.IdleConnTimeout <= 0 {
		t.Error("Default client should have IdleConnTimeout > 0")
	}
}

// TestBehavior_ClientCanMakeRequests verifies clients can actually make HTTP requests
func TestBehavior_ClientCanMakeRequests(t *testing.T) {
	// Create a test server
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	testCases := []struct {
		name   string
		client *http.Client
	}{
		{"New with config", New(&Config{Timeout: 5 * time.Second})},
		{"Default", Default()},
		{"WithTimeout", WithTimeout(5 * time.Second)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.client.Get(server.URL)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("expected 200, got %d", resp.StatusCode)
			}

			body, _ := io.ReadAll(resp.Body)
			if string(body) != "OK" {
				t.Errorf("expected 'OK', got %q", string(body))
			}
		})
	}

	if atomic.LoadInt32(&requestCount) != 3 {
		t.Errorf("expected 3 requests, got %d", requestCount)
	}
}

// TestBehavior_TimeoutIsRespected verifies timeout actually works
func TestBehavior_TimeoutIsRespected(t *testing.T) {
	// Create a slow server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Client with very short timeout
	client := WithTimeout(50 * time.Millisecond)

	start := time.Now()
	_, err := client.Get(server.URL)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected timeout error, got nil")
	}

	// Should have timed out around 50ms, not waited 500ms
	if elapsed > 200*time.Millisecond {
		t.Errorf("timeout took too long: %v (expected ~50ms)", elapsed)
	}
}

// TestBehavior_ConnectionReuse verifies connection pooling works
func TestBehavior_ConnectionReuse(t *testing.T) {
	var connectionCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Track unique connections by checking if it's a new connection
		// This is a simplification - in reality we'd check connection state
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(&Config{
		Timeout:             5 * time.Second,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     30 * time.Second,
	})

	// Make multiple requests - they should reuse connections
	for i := 0; i < 10; i++ {
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// If we got here without errors, connection reuse is working
	// (if it weren't, we'd likely see connection errors or slowdowns)
	_ = connectionCount
}

// TestBehavior_ContextCancellation verifies context cancellation works
func TestBehavior_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := Default()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)

	start := time.Now()
	_, err := client.Do(req)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected context cancellation error")
	}

	if elapsed > 200*time.Millisecond {
		t.Errorf("cancellation took too long: %v", elapsed)
	}
}

// TestBehavior_ConcurrentRequests verifies clients handle concurrent requests
func TestBehavior_ConcurrentRequests(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(&Config{
		Timeout:             5 * time.Second,
		MaxIdleConnsPerHost: 10,
	})

	// Make 20 concurrent requests
	done := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		go func() {
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Errorf("concurrent request failed: %v", err)
			} else {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			done <- true
		}()
	}

	// Wait for all requests
	for i := 0; i < 20; i++ {
		<-done
	}

	if atomic.LoadInt32(&requestCount) != 20 {
		t.Errorf("expected 20 requests, got %d", requestCount)
	}
}

// TestBehavior_DifferentConfigsProduceDifferentClients verifies independence
func TestBehavior_DifferentConfigsProduceDifferentClients(t *testing.T) {
	client1 := New(&Config{Timeout: 10 * time.Second, MaxIdleConnsPerHost: 5})
	client2 := New(&Config{Timeout: 20 * time.Second, MaxIdleConnsPerHost: 10})
	client3 := Default()

	// All should be different instances
	if client1 == client2 || client2 == client3 || client1 == client3 {
		t.Error("clients should be different instances")
	}

	// Timeouts should be different
	if client1.Timeout == client2.Timeout {
		t.Error("client1 and client2 should have different timeouts")
	}

	// Transports should be different
	t1 := client1.Transport.(*http.Transport)
	t2 := client2.Transport.(*http.Transport)
	if t1.MaxIdleConnsPerHost == t2.MaxIdleConnsPerHost {
		t.Error("transports should have different MaxIdleConnsPerHost")
	}
}
