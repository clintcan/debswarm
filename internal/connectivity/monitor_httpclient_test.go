package connectivity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// Integration tests to verify httpclient integration in connectivity/monitor.go

// TestMonitor_HTTPClientIntegration verifies the monitor works with httpclient
func TestMonitor_HTTPClientIntegration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	m := NewMonitor(&Config{
		Mode:          "auto",
		CheckURL:      server.URL,
		CheckTimeout:  5 * time.Second,
		CheckInterval: 1 * time.Second,
	}, zap.NewNop())

	// Should be able to check connectivity
	mode := m.checkConnectivity(context.Background())
	if mode != ModeOnline {
		t.Errorf("expected ModeOnline, got %v", mode)
	}
}

// TestMonitor_ClientFieldInitialized verifies the client field is set
func TestMonitor_ClientFieldInitialized(t *testing.T) {
	m := NewMonitor(&Config{
		CheckTimeout: 5 * time.Second,
	}, zap.NewNop())

	if m.client == nil {
		t.Fatal("client field should be initialized")
	}
}

// TestMonitor_ClientTimeoutMatches verifies the client timeout matches config
func TestMonitor_ClientTimeoutMatches(t *testing.T) {
	timeout := 3 * time.Second
	m := NewMonitor(&Config{
		CheckTimeout: timeout,
	}, zap.NewNop())

	if m.client.Timeout != timeout {
		t.Errorf("client timeout mismatch: got %v, want %v", m.client.Timeout, timeout)
	}
}

// TestMonitor_TimeoutWorks verifies the client timeout is respected
func TestMonitor_TimeoutWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	m := NewMonitor(&Config{
		Mode:          "auto",
		CheckURL:      server.URL,
		CheckTimeout:  50 * time.Millisecond, // Very short timeout
		CheckInterval: 1 * time.Second,
	}, zap.NewNop())

	start := time.Now()
	mode := m.checkConnectivity(context.Background())
	elapsed := time.Since(start)

	// Should timeout and report offline
	if mode == ModeOnline {
		t.Error("expected offline due to timeout")
	}

	// Should timeout quickly, not wait for server
	if elapsed > 300*time.Millisecond {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

// TestMonitor_MultipleChecks verifies multiple connectivity checks work
func TestMonitor_MultipleChecks(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	m := NewMonitor(&Config{
		Mode:          "auto",
		CheckURL:      server.URL,
		CheckTimeout:  5 * time.Second,
		CheckInterval: 1 * time.Second,
	}, zap.NewNop())

	ctx := context.Background()

	// Make multiple checks
	for i := 0; i < 5; i++ {
		mode := m.checkConnectivity(ctx)
		if mode != ModeOnline {
			t.Errorf("check %d: expected ModeOnline, got %v", i, mode)
		}
	}

	if atomic.LoadInt32(&requestCount) != 5 {
		t.Errorf("expected 5 requests, got %d", requestCount)
	}
}

// TestMonitor_ServerResponses verifies any server response indicates connectivity
// Note: The monitor treats ANY HTTP response (even errors) as online,
// because receiving any response means internet connectivity exists.
func TestMonitor_ServerErrors(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
		expectMode Mode
	}{
		{"success", http.StatusOK, ModeOnline},
		{"server error", http.StatusInternalServerError, ModeOnline}, // Any response = online
		{"not found", http.StatusNotFound, ModeOnline},               // Any response = online
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
			}))
			defer server.Close()

			m := NewMonitor(&Config{
				Mode:          "auto",
				CheckURL:      server.URL,
				CheckTimeout:  5 * time.Second,
				CheckInterval: 1 * time.Second,
			}, zap.NewNop())

			mode := m.checkConnectivity(context.Background())
			if mode != tc.expectMode {
				t.Errorf("expected %v for status %d, got %v", tc.expectMode, tc.statusCode, mode)
			}
		})
	}
}

// TestMonitor_DefaultConfigClient verifies default config initializes client
func TestMonitor_DefaultConfigClient(t *testing.T) {
	// Use nil config - should use defaults
	m := NewMonitor(nil, zap.NewNop())

	if m.client == nil {
		t.Fatal("client should be initialized with default config")
	}

	if m.client.Timeout <= 0 {
		t.Error("client should have a timeout")
	}
}

// TestMonitor_MultipleMonitorsIndependent verifies independence
func TestMonitor_MultipleMonitorsIndependent(t *testing.T) {
	m1 := NewMonitor(&Config{CheckTimeout: 1 * time.Second}, zap.NewNop())
	m2 := NewMonitor(&Config{CheckTimeout: 2 * time.Second}, zap.NewNop())

	if m1.client == m2.client {
		t.Error("monitors should have separate clients")
	}

	if m1.client.Timeout == m2.client.Timeout {
		t.Error("clients should have different timeouts")
	}
}
