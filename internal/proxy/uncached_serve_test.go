package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/timeouts"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newObservedServerWithMirror builds a proxy Server whose logger records INFO+
// level logs so tests can assert on them, with no P2P node (mirror-only).
func newObservedServerWithMirror(t *testing.T) (*Server, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)

	cfg := &Config{
		Addr:           "127.0.0.1:0",
		P2PTimeout:     5 * time.Second,
		DHTLookupLimit: 10,
		MetricsPort:    0,
		Metrics:        metrics.New(),
		Timeouts:       timeouts.NewManager(nil),
		Scorer:         peers.NewScorer(),
	}
	pkgCache, err := cache.New(t.TempDir(), 100*1024*1024, logger)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	t.Cleanup(func() {
		if err := pkgCache.Close(); err != nil {
			t.Logf("Failed to close cache: %v", err)
		}
	})
	idx := index.New(t.TempDir(), logger)
	fetcher := mirror.NewFetcher(nil, logger)
	return NewServer(cfg, pkgCache, idx, nil, fetcher, logger), logs
}

func shutdownServer(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.Shutdown(ctx)
}

// TestUncachedServe_MetricAndLog verifies that when a package is served straight
// from the mirror because it has no signed index entry, the
// packages_served_uncached metric increments once per serve, the package is not
// cached, and an INFO notice is logged exactly once per repository host.
func TestUncachedServe_MetricAndLog(t *testing.T) {
	debPayload := []byte("fake .deb payload with no index entry")
	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(debPayload)
	}))
	defer mockMirror.Close()

	server, logs := newObservedServerWithMirror(t)
	defer shutdownServer(t, server)

	pkgURL := mockMirror.URL + "/pool/main/h/hello/hello_2.10-2_amd64.deb"

	// Two sequential requests for the same (unindexed) package.
	for i := range 2 {
		req := httptest.NewRequest("GET", "/"+pkgURL, nil)
		w := httptest.NewRecorder()
		server.handlePackageRequest(w, req, pkgURL)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, w.Code)
		}
		if w.Body.String() != string(debPayload) {
			t.Fatalf("request %d: body mismatch", i)
		}
	}

	// The counter increments on every uncached serve.
	if got := server.metrics.PackagesServedUncached.Value(); got != 2 {
		t.Errorf("PackagesServedUncached = %d, want 2", got)
	}

	// An uncached package must never be cached (no trusted hash to verify against).
	if got := server.cache.Count(); got != 0 {
		t.Errorf("cache count = %d, want 0 (uncached serve must not cache)", got)
	}

	// The INFO notice is emitted once per host; the second serve logs at DEBUG
	// (below the observer's INFO level), so exactly one INFO is recorded.
	infoLogs := logs.FilterLevelExact(zapcore.InfoLevel).FilterMessageSnippet("uncached").All()
	if len(infoLogs) != 1 {
		t.Errorf("uncached INFO logs = %d, want 1 (once per host)", len(infoLogs))
	}
}

// TestUncachedServe_NotCountedWhenIndexed verifies that a package with a signed
// index entry is verified and cached, and does NOT increment the uncached metric.
func TestUncachedServe_NotCountedWhenIndexed(t *testing.T) {
	payload := []byte("verified package payload")
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)

	pkgPath := "pool/main/h/hello/hello_1.0_amd64.deb"
	packages := fmt.Sprintf("Package: hello\nVersion: 1.0\nArchitecture: amd64\nFilename: %s\nSize: %d\nSHA256: %s\n\n",
		pkgPath, len(payload), hexSum)
	repoURL := mockMirror.URL + "/dists/stable/main/binary-amd64/Packages"
	if err := server.index.LoadFromData([]byte(packages), repoURL); err != nil {
		t.Fatalf("LoadFromData: %v", err)
	}

	pkgURL := mockMirror.URL + "/" + pkgPath
	req := httptest.NewRequest("GET", "/"+pkgURL, nil)
	w := httptest.NewRecorder()
	server.handlePackageRequest(w, req, pkgURL)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := server.metrics.PackagesServedUncached.Value(); got != 0 {
		t.Errorf("PackagesServedUncached = %d, want 0 for an indexed package", got)
	}
	if got := server.cache.Count(); got != 1 {
		t.Errorf("cache count = %d, want 1 (indexed package should be cached)", got)
	}
}
