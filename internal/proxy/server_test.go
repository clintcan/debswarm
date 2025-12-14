package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/timeouts"
	"go.uber.org/zap"
)

func newTestLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

func newTestCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.New(t.TempDir(), 100*1024*1024, newTestLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	return c
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &Config{
		Addr:           "127.0.0.1:0",
		P2PTimeout:     5 * time.Second,
		DHTLookupLimit: 10,
		MetricsPort:    0, // Disable metrics server
		Metrics:        metrics.New(),
		Timeouts:       timeouts.NewManager(nil),
		Scorer:         peers.NewScorer(),
	}

	pkgCache := newTestCache(t)
	logger := newTestLogger()
	idx := index.New(t.TempDir(), logger)
	fetcher := mirror.NewFetcher(nil, logger)

	return NewServer(cfg, pkgCache, idx, nil, fetcher, logger)
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	if cfg.Addr != "127.0.0.1:9977" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, "127.0.0.1:9977")
	}
	if cfg.P2PTimeout != 5*time.Second {
		t.Errorf("P2PTimeout = %v, want %v", cfg.P2PTimeout, 5*time.Second)
	}
	if cfg.DHTLookupLimit != 10 {
		t.Errorf("DHTLookupLimit = %d, want 10", cfg.DHTLookupLimit)
	}
	if cfg.MetricsPort != 9978 {
		t.Errorf("MetricsPort = %d, want 9978", cfg.MetricsPort)
	}
}

func TestNewServer(t *testing.T) {
	server := newTestServer(t)
	if server == nil {
		t.Fatal("NewServer returned nil")
	}

	// Verify components are initialized
	if server.cache == nil {
		t.Error("cache is nil")
	}
	if server.index == nil {
		t.Error("index is nil")
	}
	if server.metrics == nil {
		t.Error("metrics is nil")
	}
	if server.timeouts == nil {
		t.Error("timeouts is nil")
	}
	if server.scorer == nil {
		t.Error("scorer is nil")
	}
}

func TestNewServer_NilConfig(t *testing.T) {
	pkgCache := newTestCache(t)
	logger := newTestLogger()
	idx := index.New(t.TempDir(), logger)
	fetcher := mirror.NewFetcher(nil, logger)

	server := NewServer(nil, pkgCache, idx, nil, fetcher, logger)
	if server == nil {
		t.Fatal("NewServer with nil config returned nil")
	}

	// Should use default values
	if server.addr != "127.0.0.1:9977" {
		t.Errorf("addr = %q, want default", server.addr)
	}
}

func TestGetStats(t *testing.T) {
	server := newTestServer(t)

	stats := server.GetStats()

	// Initial stats should be zero
	if stats.RequestsTotal != 0 {
		t.Errorf("RequestsTotal = %d, want 0", stats.RequestsTotal)
	}
	if stats.RequestsP2P != 0 {
		t.Errorf("RequestsP2P = %d, want 0", stats.RequestsP2P)
	}
	if stats.BytesFromP2P != 0 {
		t.Errorf("BytesFromP2P = %d, want 0", stats.BytesFromP2P)
	}
}

func TestHandleStats(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest("GET", "/stats", nil)
	w := httptest.NewRecorder()

	server.handleStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	// Check security headers
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("X-Content-Type-Options header not set")
	}

	// Check response contains expected fields
	body := w.Body.String()
	expectedFields := []string{
		"requests_total",
		"requests_p2p",
		"requests_mirror",
		"bytes_from_p2p",
		"p2p_ratio_percent",
		"cache_size_bytes",
	}
	for _, field := range expectedFields {
		if !strings.Contains(body, field) {
			t.Errorf("Response missing field %q", field)
		}
	}
}

func TestClassifyRequest(t *testing.T) {
	server := newTestServer(t)

	tests := []struct {
		url      string
		expected requestType
	}{
		{"http://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello_2.10-2_amd64.deb", requestTypePackage},
		{"http://archive.ubuntu.com/ubuntu/dists/jammy/main/binary-amd64/Packages.gz", requestTypeIndex},
		{"http://archive.ubuntu.com/ubuntu/dists/jammy/main/source/Sources.xz", requestTypeIndex},
		{"http://archive.ubuntu.com/ubuntu/dists/jammy/Release", requestTypeRelease},
		{"http://archive.ubuntu.com/ubuntu/dists/jammy/InRelease", requestTypeRelease},
		{"http://archive.ubuntu.com/ubuntu/dists/jammy/Release.gpg", requestTypeRelease},
		{"http://example.com/some/other/file.txt", requestTypeUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			result := server.classifyRequest(tc.url)
			if result != tc.expected {
				t.Errorf("classifyRequest(%q) = %d, want %d", tc.url, result, tc.expected)
			}
		})
	}
}

func TestExtractTargetURL(t *testing.T) {
	server := newTestServer(t)

	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "http URL",
			path:     "/http://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello.deb",
			expected: "http://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello.deb",
		},
		{
			name:     "https URL",
			path:     "/https://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello.deb",
			expected: "https://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello.deb",
		},
		{
			name:     "host path format",
			path:     "/archive.ubuntu.com/ubuntu/pool/main/h/hello/hello.deb",
			expected: "http://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello.deb",
		},
		{
			name:     "empty path",
			path:     "/",
			expected: "",
		},
		{
			name:     "no slash",
			path:     "/singleword",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			result := server.extractTargetURL(req)
			if result != tc.expected {
				t.Errorf("extractTargetURL(%q) = %q, want %q", tc.path, result, tc.expected)
			}
		})
	}
}

func TestExtractTargetURL_BlockedURLs(t *testing.T) {
	server := newTestServer(t)

	// URLs that should be blocked (SSRF protection)
	blockedURLs := []string{
		"/http://localhost/secret",
		"/http://127.0.0.1/internal",
		"/http://169.254.169.254/latest/meta-data/", // AWS metadata
		"/http://[::1]/internal",
		"/http://internal.example.com/api",
	}

	for _, path := range blockedURLs {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest("GET", path, nil)
			result := server.extractTargetURL(req)
			if result != "" {
				t.Errorf("extractTargetURL should block %q, got %q", path, result)
			}
		})
	}
}

func TestHandleRequest_InvalidRequest(t *testing.T) {
	server := newTestServer(t)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	server.handleRequest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1024, "1.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}

	for _, tc := range tests {
		result := formatBytes(tc.input)
		if result != tc.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{90 * time.Second, "1m"},
	}

	for _, tc := range tests {
		result := formatDuration(tc.input)
		if result != tc.expected {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestShutdown(t *testing.T) {
	server := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Shutdown should not panic and should complete
	err := server.Shutdown(ctx)
	// May error if server wasn't started, but shouldn't panic
	_ = err
}

func TestSetDashboard(t *testing.T) {
	server := newTestServer(t)

	if server.dashboard != nil {
		t.Error("dashboard should be nil initially")
	}

	// Setting dashboard should work
	server.SetDashboard(nil) // Even nil should be accepted

	// We can't easily test with real dashboard without circular import
}

func TestGetDashboardStats(t *testing.T) {
	server := newTestServer(t)

	stats := server.GetDashboardStats()
	if stats == nil {
		t.Fatal("GetDashboardStats returned nil")
	}

	// Initial stats should have reasonable defaults
	if stats.CacheCount < 0 {
		t.Error("CacheCount should not be negative")
	}
}

func TestGetPeerInfo_NoNode(t *testing.T) {
	server := newTestServer(t)

	peers := server.GetPeerInfo()
	if peers != nil {
		t.Errorf("GetPeerInfo without p2p node should return nil, got %v", peers)
	}
}

func TestUpdateMetrics(t *testing.T) {
	server := newTestServer(t)

	// Should not panic
	server.UpdateMetrics()

	// Verify metrics were updated
	cacheSize := server.metrics.CacheSize.Value()
	if cacheSize < 0 {
		t.Error("CacheSize metric should not be negative")
	}
}

func TestLoadIndex(t *testing.T) {
	server := newTestServer(t)

	// Loading from invalid URL should fail
	err := server.LoadIndex("http://invalid.example.com/nonexistent")
	if err == nil {
		t.Error("LoadIndex should fail for invalid URL")
	}
}

func TestReannouncePackages_NoNode(t *testing.T) {
	server := newTestServer(t)

	ctx := context.Background()
	err := server.ReannouncePackages(ctx)
	if err != nil {
		t.Errorf("ReannouncePackages without node should return nil, got %v", err)
	}
}

func TestAnnounceAsync_NoNode(t *testing.T) {
	server := newTestServer(t)

	// Should not panic
	server.announceAsync("abc123")
}

func TestStats_AtomicOperations(t *testing.T) {
	server := newTestServer(t)

	// Simulate concurrent requests
	done := make(chan bool)

	go func() {
		for i := 0; i < 100; i++ {
			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()
			server.handleRequest(w, req)
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			_ = server.GetStats()
		}
		done <- true
	}()

	<-done
	<-done

	stats := server.GetStats()
	if stats.RequestsTotal < 100 {
		t.Errorf("RequestsTotal = %d, want >= 100", stats.RequestsTotal)
	}
}

func TestSetSecurityHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	setSecurityHeaders(w)

	expectedHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"X-XSS-Protection":       "1; mode=block",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
		"Cache-Control":          "no-store, no-cache, must-revalidate",
	}

	for name, expected := range expectedHeaders {
		got := w.Header().Get(name)
		if got != expected {
			t.Errorf("Header %s = %q, want %q", name, got, expected)
		}
	}
}
