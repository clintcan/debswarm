package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/connectivity"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/timeouts"
)

// countingMirror is a mock APT mirror that serves a fixed body with an ETag and
// honors conditional GETs, counting total and conditional requests so a test can
// prove debswarm revalidated instead of re-downloading.
type countingMirror struct {
	body        []byte
	etag        string
	requests    int32
	conditional int32
}

func (m *countingMirror) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.requests, 1)
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			atomic.AddInt32(&m.conditional, 1)
			if inm == m.etag {
				w.Header().Set("ETag", m.etag)
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.Header().Set("ETag", m.etag)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(m.body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(m.body)
	}
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func serverWith(t *testing.T, c *cache.Cache, idx *index.Index) *Server {
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
	return NewServer(cfg, c, idx, nil, mirror.NewFetcher(nil, newTestLogger()), newTestLogger())
}

// TestMetadataCache_RevalidatesAndServesCachedBody proves the core win: after the
// first fetch caches a Packages index, a second cold-client request revalidates
// with the mirror (a cheap conditional GET), the mirror returns 304 with no body,
// and debswarm still serves the full body from cache.
func TestMetadataCache_RevalidatesAndServesCachedBody(t *testing.T) {
	payload := bytes.Repeat([]byte("Package: hello\n\n"), 4096)
	m := &countingMirror{body: payload, etag: `"v1"`}
	mockMirror := httptest.NewServer(m.handler())
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)
	server.cache.SetMetadataMaxSize(10 * 1024 * 1024)

	url := mockMirror.URL + "/dists/stable/main/binary-amd64/Packages"

	// Request 1: cold cache → full download from the mirror.
	w1 := httptest.NewRecorder()
	server.handleIndexRequest(w1, httptest.NewRequest("GET", "/"+url, nil), url)
	if w1.Code != http.StatusOK || !bytes.Equal(w1.Body.Bytes(), payload) {
		t.Fatalf("request 1: code=%d bodyLen=%d want 200/%d", w1.Code, w1.Body.Len(), len(payload))
	}
	if got := atomic.LoadInt32(&m.requests); got != 1 {
		t.Fatalf("mirror requests after cold fetch = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&m.conditional); got != 0 {
		t.Fatalf("cold fetch should not be conditional, got %d", got)
	}

	// Request 2: fresh client (no validators). debswarm revalidates with its own
	// ETag; the mirror answers 304 (no body); debswarm serves the cached body.
	w2 := httptest.NewRecorder()
	server.handleIndexRequest(w2, httptest.NewRequest("GET", "/"+url, nil), url)
	if w2.Code != http.StatusOK || !bytes.Equal(w2.Body.Bytes(), payload) {
		t.Fatalf("request 2: code=%d bodyLen=%d want 200/%d (served from cache)", w2.Code, w2.Body.Len(), len(payload))
	}
	if got := atomic.LoadInt32(&m.requests); got != 2 {
		t.Fatalf("mirror requests after revalidation = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&m.conditional); got != 1 {
		t.Fatalf("second fetch should be conditional (debswarm sent its ETag), got %d", got)
	}
	if got := atomic.LoadInt64(&server.metadataHits); got != 1 {
		t.Errorf("metadataHits = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&server.metadataBytesSaved); got != int64(len(payload)) {
		t.Errorf("metadataBytesSaved = %d, want %d", got, len(payload))
	}
}

// TestMetadataCache_ClientRevalidationReturns304 verifies that when the client
// itself sends a matching validator, and the cached copy is confirmed current,
// debswarm answers the client with a 304 rather than re-sending the body.
func TestMetadataCache_ClientRevalidationReturns304(t *testing.T) {
	payload := []byte("Origin: Debian\n")
	m := &countingMirror{body: payload, etag: `"rel1"`}
	mockMirror := httptest.NewServer(m.handler())
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)
	server.cache.SetMetadataMaxSize(1 * 1024 * 1024)

	url := mockMirror.URL + "/dists/stable/InRelease"

	// Prime the cache.
	w1 := httptest.NewRecorder()
	server.handlePassthrough(w1, httptest.NewRequest("GET", "/"+url, nil), url)
	if w1.Code != http.StatusOK {
		t.Fatalf("prime: code=%d want 200", w1.Code)
	}

	// Client now presents the same ETag → expect a 304 to the client.
	req := httptest.NewRequest("GET", "/"+url, nil)
	req.Header.Set("If-None-Match", `"rel1"`)
	w2 := httptest.NewRecorder()
	server.handlePassthrough(w2, req, url)
	if w2.Code != http.StatusNotModified {
		t.Fatalf("client revalidation: code=%d want 304", w2.Code)
	}
	if w2.Body.Len() != 0 {
		t.Errorf("304 response should have no body, got %d bytes", w2.Body.Len())
	}
}

// TestMetadataCache_ImmutableByHashSkipsUpstream verifies that a cached by-hash
// URL (immutable content) is served with no upstream request at all on the
// second hit.
func TestMetadataCache_ImmutableByHashSkipsUpstream(t *testing.T) {
	payload := bytes.Repeat([]byte("immutable\n"), 512)
	m := &countingMirror{body: payload, etag: `"x"`}
	mockMirror := httptest.NewServer(m.handler())
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)
	server.cache.SetMetadataMaxSize(1 * 1024 * 1024)

	url := mockMirror.URL + "/dists/stable/main/binary-amd64/by-hash/SHA256/" + sha256Hex(payload)

	// First hit populates the cache (and verifies the by-hash digest on store).
	w1 := httptest.NewRecorder()
	server.handleIndexRequest(w1, httptest.NewRequest("GET", "/"+url, nil), url)
	if w1.Code != http.StatusOK || !bytes.Equal(w1.Body.Bytes(), payload) {
		t.Fatalf("first by-hash fetch: code=%d bodyLen=%d", w1.Code, w1.Body.Len())
	}
	if got := atomic.LoadInt32(&m.requests); got != 1 {
		t.Fatalf("mirror requests = %d, want 1", got)
	}

	// Second hit must be served entirely from cache — no upstream round-trip.
	w2 := httptest.NewRecorder()
	server.handleIndexRequest(w2, httptest.NewRequest("GET", "/"+url, nil), url)
	if w2.Code != http.StatusOK || !bytes.Equal(w2.Body.Bytes(), payload) {
		t.Fatalf("second by-hash fetch: code=%d bodyLen=%d", w2.Code, w2.Body.Len())
	}
	if got := atomic.LoadInt32(&m.requests); got != 1 {
		t.Errorf("immutable second hit contacted the mirror: requests = %d, want 1", got)
	}
}

// TestMetadataCache_WarmsIndexAfterRestart proves that caching Packages bytes lets
// a fresh (cold) in-memory index be re-populated from cache after a daemon
// restart, without re-downloading the index body from the WAN.
func TestMetadataCache_WarmsIndexAfterRestart(t *testing.T) {
	packages := []byte("Package: hello\n" +
		"Version: 2.10-2\n" +
		"Architecture: amd64\n" +
		"Filename: pool/main/h/hello/hello_2.10-2_amd64.deb\n" +
		"Size: 100\n" +
		"SHA256: " + sha256Hex([]byte("deb-bytes")) + "\n\n")
	m := &countingMirror{body: packages, etag: `"pk1"`}
	mockMirror := httptest.NewServer(m.handler())
	defer mockMirror.Close()

	dir := t.TempDir()
	logger := newTestLogger()

	// First "boot": fetch and cache the Packages file; the index gets populated.
	c1, err := cache.New(dir, 100*1024*1024, logger)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	c1.SetMetadataMaxSize(10 * 1024 * 1024)
	idx1 := index.New(dir, logger)
	srv1 := serverWith(t, c1, idx1)

	url := mockMirror.URL + "/dists/stable/main/binary-amd64/Packages"
	w := httptest.NewRecorder()
	srv1.handleIndexRequest(w, httptest.NewRequest("GET", "/"+url, nil), url)
	if idx1.Count() == 0 {
		t.Fatal("index not populated on first boot")
	}
	_ = c1.Close()

	// Second "boot": reopen the same cache dir with a fresh, empty index.
	c2, err := cache.New(dir, 100*1024*1024, logger)
	if err != nil {
		t.Fatalf("cache.New (reboot): %v", err)
	}
	defer c2.Close()
	c2.SetMetadataMaxSize(10 * 1024 * 1024)
	idx2 := index.New(dir, logger)
	if idx2.Count() != 0 {
		t.Fatalf("fresh index should be empty, has %d", idx2.Count())
	}
	srv2 := serverWith(t, c2, idx2)

	before := atomic.LoadInt32(&m.requests)
	w2 := httptest.NewRecorder()
	srv2.handleIndexRequest(w2, httptest.NewRequest("GET", "/"+url, nil), url)

	if idx2.Count() == 0 {
		t.Fatal("index not warmed from cache after restart")
	}
	if w2.Code != http.StatusOK {
		t.Fatalf("post-restart serve code=%d want 200", w2.Code)
	}
	// The refetch must have been a conditional 304, not a full re-download.
	if atomic.LoadInt32(&m.conditional) == 0 {
		t.Error("post-restart fetch was not conditional (no revalidation happened)")
	}
	_ = before
}

// TestMetadataCache_ServesStaleWhenUpstreamDown proves the offline win: once a
// metadata file is cached, a later request served while the mirror is
// unreachable still returns the cached body (flagged stale) instead of failing
// apt-get update. APT itself still enforces the GPG signature and Valid-Until.
func TestMetadataCache_ServesStaleWhenUpstreamDown(t *testing.T) {
	payload := []byte("Origin: Debian\nSuite: stable\nValid-Until: Thu, 01 Jan 2099 00:00:00 UTC\n")
	m := &countingMirror{body: payload, etag: `"rel1"`}
	mockMirror := httptest.NewServer(m.handler())

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)
	server.cache.SetMetadataMaxSize(1 * 1024 * 1024)
	server.metadataServeStale = true

	url := mockMirror.URL + "/dists/stable/InRelease"

	// Prime the cache while the mirror is up.
	w1 := httptest.NewRecorder()
	server.handlePassthrough(w1, httptest.NewRequest("GET", "/"+url, nil), url)
	if w1.Code != http.StatusOK {
		t.Fatalf("prime: code=%d want 200", w1.Code)
	}

	// Mirror goes down.
	mockMirror.Close()

	w2 := httptest.NewRecorder()
	server.handlePassthrough(w2, httptest.NewRequest("GET", "/"+url, nil), url)
	if w2.Code != http.StatusOK {
		t.Fatalf("stale serve: code=%d want 200 (served from cache while mirror down)", w2.Code)
	}
	if !bytes.Equal(w2.Body.Bytes(), payload) {
		t.Fatal("stale body differs from cached copy")
	}
	if got := w2.Header().Get("X-Debswarm-Stale"); got != "true" {
		t.Errorf("X-Debswarm-Stale = %q, want \"true\" (headers: %v)", got, w2.Header())
	}
	if got := server.metrics.MetadataCacheStaleServed.Value(); got != 1 {
		t.Errorf("MetadataCacheStaleServed = %d, want 1", got)
	}
}

// TestMetadataCache_StaleGateOffReturnsError proves the safety valve: with stale
// serving disabled, an unreachable mirror produces a hard 502 even when a cached
// copy exists — the operator opted out of ever serving unrevalidated metadata.
func TestMetadataCache_StaleGateOffReturnsError(t *testing.T) {
	payload := []byte("Origin: Debian\n")
	m := &countingMirror{body: payload, etag: `"r"`}
	mockMirror := httptest.NewServer(m.handler())

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)
	server.cache.SetMetadataMaxSize(1 * 1024 * 1024)
	server.metadataServeStale = false // gate off

	url := mockMirror.URL + "/dists/stable/InRelease"

	w1 := httptest.NewRecorder()
	server.handlePassthrough(w1, httptest.NewRequest("GET", "/"+url, nil), url)
	if w1.Code != http.StatusOK {
		t.Fatalf("prime: code=%d want 200", w1.Code)
	}
	mockMirror.Close()

	w2 := httptest.NewRecorder()
	server.handlePassthrough(w2, httptest.NewRequest("GET", "/"+url, nil), url)
	if w2.Code != http.StatusBadGateway {
		t.Fatalf("gate off: code=%d want 502 (stale serving disabled)", w2.Code)
	}
	if got := server.metrics.MetadataCacheStaleServed.Value(); got != 0 {
		t.Errorf("MetadataCacheStaleServed = %d, want 0 (nothing stale should have been served)", got)
	}
}

// TestMetadataCache_UpstreamDownNoCacheReturnsError proves that stale serving is
// not a way to conjure content: an unreachable mirror with nothing cached still
// fails hard rather than inventing an empty 200.
func TestMetadataCache_UpstreamDownNoCacheReturnsError(t *testing.T) {
	m := &countingMirror{body: []byte("x"), etag: `"r"`}
	mockMirror := httptest.NewServer(m.handler())
	mockMirror.Close() // down before the cache is ever primed

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)
	server.cache.SetMetadataMaxSize(1 * 1024 * 1024)
	server.metadataServeStale = true

	url := mockMirror.URL + "/dists/stable/InRelease"

	w := httptest.NewRecorder()
	server.handlePassthrough(w, httptest.NewRequest("GET", "/"+url, nil), url)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("no cache + mirror down: code=%d want 502", w.Code)
	}
}

// TestMetadataCache_OfflineFastPathSkipsUpstream proves the connectivity
// fast-path: when the monitor reports offline, a cached metadata request is
// served straight from cache without even attempting the doomed upstream call.
func TestMetadataCache_OfflineFastPathSkipsUpstream(t *testing.T) {
	payload := []byte("Origin: Debian\n")
	m := &countingMirror{body: payload, etag: `"r"`}
	mockMirror := httptest.NewServer(m.handler())
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)
	server.cache.SetMetadataMaxSize(1 * 1024 * 1024)
	server.metadataServeStale = true

	url := mockMirror.URL + "/dists/stable/InRelease"

	// Prime while online (connectivity monitor unset == treated as reachable).
	w1 := httptest.NewRecorder()
	server.handlePassthrough(w1, httptest.NewRequest("GET", "/"+url, nil), url)
	if w1.Code != http.StatusOK {
		t.Fatalf("prime: code=%d want 200", w1.Code)
	}
	primeReqs := atomic.LoadInt32(&m.requests)

	// Flip to offline; the fast-path must serve from cache with no upstream call.
	mon := connectivity.NewMonitor(nil, newTestLogger())
	mon.ForceMode(connectivity.ModeOffline)
	server.connectivity = mon

	w2 := httptest.NewRecorder()
	server.handlePassthrough(w2, httptest.NewRequest("GET", "/"+url, nil), url)
	if w2.Code != http.StatusOK || !bytes.Equal(w2.Body.Bytes(), payload) {
		t.Fatalf("offline serve: code=%d bodyLen=%d want 200/%d", w2.Code, w2.Body.Len(), len(payload))
	}
	if got := w2.Header().Get("X-Debswarm-Stale"); got != "true" {
		t.Errorf("offline serve X-Debswarm-Stale = %q, want \"true\"", got)
	}
	if got := atomic.LoadInt32(&m.requests); got != primeReqs {
		t.Errorf("offline fast-path contacted the mirror: requests %d -> %d", primeReqs, got)
	}
}
