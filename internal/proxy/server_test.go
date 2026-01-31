package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/timeouts"
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
		// by-hash URLs (APT Acquire-By-Hash feature)
		{"http://archive.ubuntu.com/ubuntu/dists/jammy/main/binary-amd64/by-hash/SHA256/abc123", requestTypeIndex},
		{"http://archive.ubuntu.com/ubuntu/dists/jammy/main/source/by-hash/SHA256/def456", requestTypeIndex},
		// by-hash URLs that should NOT be classified as index (translations, commands)
		{"http://archive.ubuntu.com/ubuntu/dists/jammy/main/i18n/by-hash/SHA256/abc123", requestTypeUnknown},
		{"http://archive.ubuntu.com/ubuntu/dists/jammy/main/cnf/by-hash/SHA256/def456", requestTypeUnknown},
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

// newTestServerWithMirror creates a test server for testing with mock mirrors
func newTestServerWithMirror(t *testing.T) *Server {
	t.Helper()
	cfg := &Config{
		Addr:           "127.0.0.1:0",
		P2PTimeout:     5 * time.Second,
		DHTLookupLimit: 10,
		MetricsPort:    0,
		Metrics:        metrics.New(),
		Timeouts:       timeouts.NewManager(nil),
		Scorer:         peers.NewScorer(),
	}

	pkgCache := newTestCache(t)
	logger := newTestLogger()
	idx := index.New(t.TempDir(), logger)

	// Create fetcher - it will use the mock server URLs directly
	fetcher := mirror.NewFetcher(nil, logger)

	return NewServer(cfg, pkgCache, idx, nil, fetcher, logger)
}

func TestHandleIndexRequest(t *testing.T) {
	// Create mock mirror server
	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a minimal Packages file content
		packagesContent := `Package: hello
Version: 2.10-2
Architecture: amd64
Filename: pool/main/h/hello/hello_2.10-2_amd64.deb
Size: 52832
SHA256: 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824

`
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(packagesContent))
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	// Make request for index file
	req := httptest.NewRequest("GET", "/"+mockMirror.URL+"/dists/jammy/main/binary-amd64/Packages", nil)
	w := httptest.NewRecorder()

	server.handleIndexRequest(w, req, mockMirror.URL+"/dists/jammy/main/binary-amd64/Packages")

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	if !strings.Contains(w.Body.String(), "Package: hello") {
		t.Error("Response should contain package data")
	}
}

func TestHandleIndexRequest_Error(t *testing.T) {
	// Create mock server that returns error
	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	req := httptest.NewRequest("GET", "/"+mockMirror.URL+"/Packages", nil)
	w := httptest.NewRecorder()

	server.handleIndexRequest(w, req, mockMirror.URL+"/Packages")

	if w.Code != http.StatusBadGateway {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestHandlePassthrough(t *testing.T) {
	expectedContent := "Release file content"
	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(expectedContent))
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	req := httptest.NewRequest("GET", "/"+mockMirror.URL+"/Release", nil)
	w := httptest.NewRecorder()

	server.handlePassthrough(w, req, mockMirror.URL+"/Release")

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	if w.Body.String() != expectedContent {
		t.Errorf("Body = %q, want %q", w.Body.String(), expectedContent)
	}

	// Verify stats updated
	stats := server.GetStats()
	if stats.RequestsMirror != 1 {
		t.Errorf("RequestsMirror = %d, want 1", stats.RequestsMirror)
	}
	if stats.BytesFromMirror != int64(len(expectedContent)) {
		t.Errorf("BytesFromMirror = %d, want %d", stats.BytesFromMirror, len(expectedContent))
	}
}

func TestHandlePassthrough_Error(t *testing.T) {
	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	req := httptest.NewRequest("GET", "/"+mockMirror.URL+"/Release", nil)
	w := httptest.NewRecorder()

	server.handlePassthrough(w, req, mockMirror.URL+"/Release")

	if w.Code != http.StatusBadGateway {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestHandleReleaseRequest(t *testing.T) {
	expectedContent := "InRelease content"
	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(expectedContent))
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	req := httptest.NewRequest("GET", "/"+mockMirror.URL+"/InRelease", nil)
	w := httptest.NewRecorder()

	server.handleReleaseRequest(w, req, mockMirror.URL+"/InRelease")

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	if w.Body.String() != expectedContent {
		t.Errorf("Body = %q, want %q", w.Body.String(), expectedContent)
	}
}

func TestServeFromCache(t *testing.T) {
	server := newTestServer(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	// Pre-populate cache with test data
	testData := []byte("test package content")
	testHash := "5e724d7612dcfb976620c30f396459d3f5ccb9f750ba6f8251fc354ba8e9aa99"

	err := server.cache.Put(strings.NewReader(string(testData)), testHash, "test.deb")
	if err != nil {
		t.Fatalf("Failed to populate cache: %v", err)
	}

	// Serve from cache
	w := httptest.NewRecorder()
	server.serveFromCache(w, testHash)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	if w.Header().Get("Content-Type") != "application/vnd.debian.binary-package" {
		t.Errorf("Content-Type = %q, want debian package", w.Header().Get("Content-Type"))
	}

	if w.Header().Get("X-Debswarm-Source") != "cache" {
		t.Errorf("X-Debswarm-Source = %q, want cache", w.Header().Get("X-Debswarm-Source"))
	}

	if w.Body.String() != string(testData) {
		t.Errorf("Body = %q, want %q", w.Body.String(), string(testData))
	}
}

func TestServeFromCache_NotFound(t *testing.T) {
	server := newTestServer(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	w := httptest.NewRecorder()
	server.serveFromCache(w, "nonexistent_hash_1234567890abcdef1234567890abcdef")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestHandlePackageRequest_CacheHit(t *testing.T) {
	server := newTestServer(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	// Pre-populate cache
	testData := []byte("cached package data")
	testHash := "ed4fadeed15018a95148883178b673dcbf15d03a5a77c92d2d82827fac612b51"

	err := server.cache.Put(strings.NewReader(string(testData)), testHash, "pool/main/h/hello/hello.deb")
	if err != nil {
		t.Fatalf("Failed to populate cache: %v", err)
	}

	// Add package to index so it can be found
	packagesContent := `Package: hello
Version: 2.10
Filename: pool/main/h/hello/hello.deb
Size: 19
SHA256: ed4fadeed15018a95148883178b673dcbf15d03a5a77c92d2d82827fac612b51

`
	err = server.index.LoadFromData([]byte(packagesContent), "http://archive.ubuntu.com/ubuntu")
	if err != nil {
		t.Fatalf("Failed to load index: %v", err)
	}

	// Make request
	req := httptest.NewRequest("GET", "/http://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello.deb", nil)
	w := httptest.NewRecorder()

	server.handlePackageRequest(w, req, "http://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello.deb")

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	if w.Header().Get("X-Debswarm-Source") != "cache" {
		t.Errorf("X-Debswarm-Source = %q, want cache", w.Header().Get("X-Debswarm-Source"))
	}

	// Verify cache hit stats
	stats := server.GetStats()
	if stats.CacheHits != 1 {
		t.Errorf("CacheHits = %d, want 1", stats.CacheHits)
	}
}

func TestHandlePackageRequest_MirrorFallback(t *testing.T) {
	packageContent := []byte("package binary content")

	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(packageContent)
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	// Request without hash in index - will fall back to mirror
	req := httptest.NewRequest("GET", "/"+mockMirror.URL+"/pool/main/h/hello/hello.deb", nil)
	w := httptest.NewRecorder()

	server.handlePackageRequest(w, req, mockMirror.URL+"/pool/main/h/hello/hello.deb")

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	if w.Body.String() != string(packageContent) {
		t.Errorf("Body mismatch")
	}

	// Should count as mirror request
	stats := server.GetStats()
	if stats.RequestsMirror != 1 {
		t.Errorf("RequestsMirror = %d, want 1", stats.RequestsMirror)
	}
}

func TestHandlePackageRequest_MirrorError(t *testing.T) {
	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	req := httptest.NewRequest("GET", "/"+mockMirror.URL+"/nonexistent.deb", nil)
	w := httptest.NewRecorder()

	server.handlePackageRequest(w, req, mockMirror.URL+"/nonexistent.deb")

	if w.Code != http.StatusBadGateway {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestHandleRequest_StatsTracking(t *testing.T) {
	server := newTestServer(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	// Make request - will fail due to invalid URL but should still track stats
	req := httptest.NewRequest("GET", "/invalid", nil)
	w := httptest.NewRecorder()

	server.handleRequest(w, req)

	// Verify request was counted even for invalid requests
	stats := server.GetStats()
	if stats.RequestsTotal != 1 {
		t.Errorf("RequestsTotal = %d, want 1", stats.RequestsTotal)
	}
}

func TestHandleRequest_ActiveConnTracking(t *testing.T) {
	server := newTestServer(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	// Before request
	stats := server.GetStats()
	if stats.ActiveConnections != 0 {
		t.Errorf("Initial ActiveConnections = %d, want 0", stats.ActiveConnections)
	}

	// Make request
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	server.handleRequest(w, req)

	// After request completes, connections should be back to 0
	stats = server.GetStats()
	if stats.ActiveConnections != 0 {
		t.Errorf("Final ActiveConnections = %d, want 0", stats.ActiveConnections)
	}
}

func TestServePackageResult(t *testing.T) {
	server := newTestServer(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	result := &packageDownloadResult{
		data:        []byte("test data"),
		hash:        "testhash",
		source:      "peer",
		contentType: "application/vnd.debian.binary-package",
	}

	w := httptest.NewRecorder()
	server.servePackageResult(w, result)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	if w.Header().Get("Content-Type") != "application/vnd.debian.binary-package" {
		t.Errorf("Content-Type mismatch")
	}

	if w.Header().Get("X-Debswarm-Source") != "peer" {
		t.Errorf("X-Debswarm-Source = %q, want peer", w.Header().Get("X-Debswarm-Source"))
	}

	if w.Body.String() != "test data" {
		t.Errorf("Body mismatch")
	}
}

func TestCacheAndAnnounce(t *testing.T) {
	server := newTestServer(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	testData := []byte("test content")
	testHash := "6ae8a75555209fd6c44157c0aed8016e763ff435a19cf186f76863140143ff72"

	// Should not panic without p2p node
	server.cacheAndAnnounce(testData, testHash, "test.deb")

	// Verify data was cached
	if !server.cache.Has(testHash) {
		t.Error("Data should be cached")
	}
}

func TestGetDashboardStats_WithCacheMaxSize(t *testing.T) {
	cfg := &Config{
		Addr:           "127.0.0.1:0",
		P2PTimeout:     5 * time.Second,
		DHTLookupLimit: 10,
		MetricsPort:    0,
		CacheMaxSize:   1024 * 1024 * 100, // 100MB
		Metrics:        metrics.New(),
		Timeouts:       timeouts.NewManager(nil),
		Scorer:         peers.NewScorer(),
	}

	pkgCache := newTestCache(t)
	logger := newTestLogger()
	idx := index.New(t.TempDir(), logger)
	fetcher := mirror.NewFetcher(nil, logger)

	server := NewServer(cfg, pkgCache, idx, nil, fetcher, logger)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	stats := server.GetDashboardStats()

	if stats.CacheMaxSize == "" {
		t.Error("CacheMaxSize should be formatted")
	}

	// With empty cache, usage should be 0
	if stats.CacheUsagePercent != 0 {
		t.Errorf("CacheUsagePercent = %f, want 0", stats.CacheUsagePercent)
	}
}

func TestAnnouncementWorker_ChannelFull(t *testing.T) {
	server := newTestServer(t)

	// Fill up announcement channel (capacity is 100)
	for i := 0; i < 150; i++ {
		server.announceAsync("hash" + string(rune(i)))
	}

	// Should not block or panic
	server.announceAsync("final_hash")

	// Shutdown properly
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func TestUpdateMetrics_NilMetrics(t *testing.T) {
	server := newTestServer(t)
	server.metrics = nil

	// Should not panic
	server.UpdateMetrics()
}
