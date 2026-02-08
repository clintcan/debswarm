package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	cfg := &Config{
		Version:         "1.0.0",
		PeerID:          "12D3KooWTestPeerID",
		MaxUploadRate:   "10MB/s",
		MaxDownloadRate: "50MB/s",
	}

	statsProvider := func() *Stats { return &Stats{} }
	peersProvider := func() []PeerInfo { return nil }

	d := New(cfg, statsProvider, peersProvider)

	if d == nil {
		t.Fatal("New returned nil")
	}
	if d.version != "1.0.0" {
		t.Errorf("version = %q, want %q", d.version, "1.0.0")
	}
	if d.peerID != "12D3KooWTestPeerID" {
		t.Errorf("peerID = %q, want %q", d.peerID, "12D3KooWTestPeerID")
	}
}

func TestRecordDownload(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })

	// Record a download
	d.RecordDownload("test-package.deb", 1024*1024, "peer", 500*time.Millisecond)

	if len(d.recentDLs) != 1 {
		t.Errorf("recentDLs length = %d, want 1", len(d.recentDLs))
	}

	dl := d.recentDLs[0]
	if dl.Filename != "test-package.deb" {
		t.Errorf("Filename = %q, want %q", dl.Filename, "test-package.deb")
	}
	if dl.Source != "peer" {
		t.Errorf("Source = %q, want %q", dl.Source, "peer")
	}
}

func TestRecordDownload_MaxLimit(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })

	// Record more than max downloads
	for i := 0; i < 60; i++ {
		d.RecordDownload("package.deb", 1024, "peer", time.Millisecond)
	}

	if len(d.recentDLs) > d.maxRecent {
		t.Errorf("recentDLs length = %d, want <= %d", len(d.recentDLs), d.maxRecent)
	}
}

func TestRecordDownload_Concurrent(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				d.RecordDownload("package.deb", 1024, "peer", time.Millisecond)
			}
		}()
	}
	wg.Wait()

	// Should not exceed max
	if len(d.recentDLs) > d.maxRecent {
		t.Errorf("recentDLs length = %d, want <= %d", len(d.recentDLs), d.maxRecent)
	}
}

func TestHandler_Dashboard(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "12D3KooWTestPeer"}
	statsProvider := func() *Stats {
		return &Stats{
			RequestsTotal: 100,
			RequestsP2P:   60,
			CacheHits:     40,
		}
	}
	d := New(cfg, statsProvider, func() []PeerInfo { return nil })

	handler := d.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Check HTML content
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("Response should contain HTML doctype")
	}
	if !strings.Contains(body, "debswarm") {
		t.Error("Response should contain 'debswarm'")
	}
	if !strings.Contains(body, "12D3KooWTestPeer") {
		t.Error("Response should contain peer ID")
	}
}

func TestHandler_APIStats(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "testpeer"}
	statsProvider := func() *Stats {
		return &Stats{
			RequestsTotal:  100,
			RequestsP2P:    60,
			RequestsMirror: 40,
			BytesFromP2P:   1024 * 1024,
		}
	}
	d := New(cfg, statsProvider, func() []PeerInfo { return nil })

	handler := d.Handler()

	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	var stats Stats
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	if stats.RequestsTotal != 100 {
		t.Errorf("RequestsTotal = %d, want 100", stats.RequestsTotal)
	}
	if stats.PeerID != "testpeer" {
		t.Errorf("PeerID = %q, want %q", stats.PeerID, "testpeer")
	}
}

func TestHandler_APIPeers(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "testpeer"}
	peersProvider := func() []PeerInfo {
		return []PeerInfo{
			{ID: "peer1", ShortID: "peer1", Score: 0.9, Category: "Excellent"},
			{ID: "peer2", ShortID: "peer2", Score: 0.5, Category: "Fair"},
		}
	}
	d := New(cfg, func() *Stats { return &Stats{} }, peersProvider)

	handler := d.Handler()

	req := httptest.NewRequest("GET", "/api/peers", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	var peers []PeerInfo
	if err := json.NewDecoder(w.Body).Decode(&peers); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	if len(peers) != 2 {
		t.Errorf("Peers count = %d, want 2", len(peers))
	}
}

func TestHandler_NotFound(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })

	handler := d.Handler()

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_SecurityHeaders(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })

	handler := d.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Check security headers
	headers := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"X-XSS-Protection":       "1; mode=block",
	}

	for name, expected := range headers {
		if got := w.Header().Get(name); got != expected {
			t.Errorf("Header %s = %q, want %q", name, got, expected)
		}
	}

	// Check CSP contains nonce for dashboard page
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("Content-Security-Policy header not set")
	}
	if !strings.Contains(csp, "script-src 'nonce-") {
		t.Errorf("Dashboard CSP should contain nonce-based script-src, got: %s", csp)
	}
	if !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("Dashboard CSP should allow connect-src 'self', got: %s", csp)
	}
}

func TestHandler_NilStatsProvider(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	// Stats provider returns nil
	d := New(cfg, func() *Stats { return nil }, func() []PeerInfo { return nil })

	handler := d.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should not panic, should return OK
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandler_NilPeersProvider(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	// Peers provider returns nil
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })

	handler := d.Handler()

	req := httptest.NewRequest("GET", "/api/peers", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	// Should return empty array, not null
	body := strings.TrimSpace(w.Body.String())
	if body != "[]" {
		t.Errorf("Body = %q, want %q", body, "[]")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
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
		{1 * time.Second, "1.0s"},
		{30 * time.Second, "30.0s"},
		{90 * time.Second, "1m 30s"},
		{2 * time.Hour, "2h 0m"},
		{2*time.Hour + 30*time.Minute, "2h 30m"},
	}

	for _, tc := range tests {
		result := formatDuration(tc.input)
		if result != tc.expected {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestTruncateFilename(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short.deb", 20, "short.deb"},
		{"exactly-ten", 11, "exactly-ten"},
		{"this-is-a-very-long-filename.deb", 20, "this-is-a-very-lo..."},
		{"test", 10, "test"},
	}

	for _, tc := range tests {
		result := truncateFilename(tc.input, tc.maxLen)
		if result != tc.expected {
			t.Errorf("truncateFilename(%q, %d) = %q, want %q", tc.input, tc.maxLen, result, tc.expected)
		}
	}
}

func TestSanitizeForCSS(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"peer", "peer"},
		{"mirror", "mirror"},
		{"cache", "cache"},
		{"p2p-source", "p2p-source"},
		{"<script>alert(1)</script>", "scriptalert1script"},
		{"source with spaces", "sourcewithspaces"},
		{"", "unknown"},
		{"!!!###", "unknown"},
		{"Test123", "Test123"},
	}

	for _, tc := range tests {
		result := sanitizeForCSS(tc.input)
		if result != tc.expected {
			t.Errorf("sanitizeForCSS(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestStats_ByteFormatting(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	statsProvider := func() *Stats {
		return &Stats{
			BytesFromP2P:    1024 * 1024 * 100,  // 100 MB
			BytesFromMirror: 1024 * 1024 * 1024, // 1 GB
			CacheSizeBytes:  1024 * 1024 * 500,  // 500 MB
		}
	}
	d := New(cfg, statsProvider, func() []PeerInfo { return nil })

	handler := d.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// Check that formatted sizes appear in output
	if !strings.Contains(body, "MB") && !strings.Contains(body, "GB") {
		t.Error("Response should contain formatted byte sizes")
	}
}

func TestDashboard_UptimeCalculation(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })

	// Wait a tiny bit
	time.Sleep(10 * time.Millisecond)

	handler := d.Handler()

	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var stats Stats
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	// Uptime should be set
	if stats.Uptime == "" {
		t.Error("Uptime should not be empty")
	}
}

func TestHandler_CSPNonce(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })
	handler := d.Handler()

	// First request
	req1 := httptest.NewRequest("GET", "/", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	csp1 := w1.Header().Get("Content-Security-Policy")
	body1 := w1.Body.String()

	// Extract nonce from CSP header
	const prefix = "script-src 'nonce-"
	idx := strings.Index(csp1, prefix)
	if idx == -1 {
		t.Fatal("CSP header missing nonce")
	}
	nonceStart := idx + len(prefix)
	nonceEnd := strings.Index(csp1[nonceStart:], "'")
	if nonceEnd == -1 {
		t.Fatal("CSP nonce not properly terminated")
	}
	nonce1 := csp1[nonceStart : nonceStart+nonceEnd]

	// Verify nonce appears in script tag
	scriptAttr := `nonce="` + nonce1 + `"`
	if !strings.Contains(body1, scriptAttr) {
		t.Errorf("Body should contain script tag with nonce %q", nonce1)
	}

	// Second request should have a different nonce
	req2 := httptest.NewRequest("GET", "/", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	csp2 := w2.Header().Get("Content-Security-Policy")
	idx2 := strings.Index(csp2, prefix)
	if idx2 == -1 {
		t.Fatal("Second request CSP header missing nonce")
	}
	nonceStart2 := idx2 + len(prefix)
	nonceEnd2 := strings.Index(csp2[nonceStart2:], "'")
	nonce2 := csp2[nonceStart2 : nonceStart2+nonceEnd2]

	if nonce1 == nonce2 {
		t.Error("Nonces should differ between requests")
	}
}

func TestHandler_NoMetaRefresh(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })
	handler := d.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// The meta refresh should only appear inside <noscript>, not at the top level
	// Remove <noscript> blocks and check that no meta refresh remains
	noNoscript := body
	for {
		start := strings.Index(noNoscript, "<noscript>")
		if start == -1 {
			break
		}
		end := strings.Index(noNoscript[start:], "</noscript>")
		if end == -1 {
			break
		}
		noNoscript = noNoscript[:start] + noNoscript[start+end+len("</noscript>"):]
	}
	if strings.Contains(noNoscript, `http-equiv="refresh"`) {
		t.Error("Meta refresh should only appear inside <noscript> block")
	}
}

func TestHandler_NoscriptFallback(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })
	handler := d.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	if !strings.Contains(body, "<noscript>") {
		t.Error("Body should contain <noscript> fallback")
	}
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("Noscript block should contain meta refresh")
	}
}

func TestHandler_ChartCanvases(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })
	handler := d.Handler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	body := w.Body.String()

	canvases := []string{
		`id="chart-throughput"`,
		`id="chart-requests"`,
		`id="chart-p2p-ratio"`,
		`id="chart-peers"`,
	}
	for _, c := range canvases {
		if !strings.Contains(body, c) {
			t.Errorf("Body should contain canvas with %s", c)
		}
	}
}

func TestHandler_APIStatsCSPNoScript(t *testing.T) {
	cfg := &Config{Version: "1.0.0", PeerID: "test"}
	d := New(cfg, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })
	handler := d.Handler()

	// API endpoints should keep script-src 'none' from middleware
	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'none'") {
		t.Errorf("API endpoint CSP should have script-src 'none', got: %s", csp)
	}
}
