package proxy

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/connectivity"
	"github.com/debswarm/debswarm/internal/index"
)

const (
	offlinePkgURL = "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages"
	offlineDebURL = "http://deb.debian.org/debian/pool/main/h/hello/hello_2.10-2_amd64.deb"
	offlineDebRel = "pool/main/h/hello/hello_2.10-2_amd64.deb"
)

// offlineCache builds a cache holding a cached Packages *metadata* entry (so the
// package's SHA256 is resolvable via an index warm) and, when withDeb is true,
// the .deb bytes themselves. The returned index is COLD (never warmed), modeling
// a fresh daemon start where no apt-get update has run this session.
func offlineCache(t *testing.T, withDeb bool) (*cache.Cache, *index.Index, []byte) {
	t.Helper()
	dir := t.TempDir()
	logger := newTestLogger()
	c, err := cache.New(dir, 100*1024*1024, logger)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.SetMetadataMaxSize(10 * 1024 * 1024)

	debData := []byte("hello debian package payload bytes")
	hash := sha256Hex(debData)
	if withDeb {
		if err := c.Put(bytes.NewReader(debData), hash, offlineDebRel); err != nil {
			t.Fatalf("cache.Put deb: %v", err)
		}
	}

	packages := fmt.Sprintf("Package: hello\nVersion: 2.10-2\nArchitecture: amd64\n"+
		"Filename: %s\nSize: %d\nSHA256: %s\n\n", offlineDebRel, len(debData), hash)
	mw, err := c.NewMetadataWriter(offlinePkgURL, "", "", "")
	if err != nil {
		t.Fatalf("NewMetadataWriter: %v", err)
	}
	if _, err := mw.Write([]byte(packages)); err != nil {
		t.Fatalf("metadata write: %v", err)
	}
	if err := mw.Commit(); err != nil {
		t.Fatalf("metadata commit: %v", err)
	}

	idx := index.New(dir, logger) // cold: nothing loaded yet
	return c, idx, debData
}

func forceOffline(t *testing.T, s *Server) {
	t.Helper()
	mon := connectivity.NewMonitor(nil, newTestLogger())
	mon.ForceMode(connectivity.ModeOffline)
	s.connectivity = mon
}

// TestOffline_ServesCachedDebViaIndexWarm proves the headline: a fresh daemon
// (cold index) that is offline still serves an already-cached .deb, by warming
// the in-memory index from cached Packages metadata to resolve the URL->SHA256.
func TestOffline_ServesCachedDebViaIndexWarm(t *testing.T) {
	c, idx, debData := offlineCache(t, true)
	srv := serverWith(t, c, idx)
	forceOffline(t, srv)

	if idx.Count() != 0 {
		t.Fatalf("index should start cold, has %d entries", idx.Count())
	}

	req := httptest.NewRequest("GET", "/"+offlineDebURL, nil)
	w := httptest.NewRecorder()
	srv.handlePackageRequest(w, req, offlineDebURL)

	if w.Code != http.StatusOK {
		t.Fatalf("offline cached .deb: code=%d want 200 (body=%q)", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), debData) {
		t.Error("served body does not match the cached .deb")
	}
	if got := w.Header().Get("X-Debswarm-Source"); got != "cache" {
		t.Errorf("X-Debswarm-Source = %q, want cache", got)
	}
	if idx.Count() == 0 {
		t.Error("index was not warmed from cached metadata")
	}
}

// TestOffline_UncachedDebFailsFast proves the second half: when the package is
// resolvable but NOT cached and the node is offline, the request fails fast with
// 503 instead of grinding through the doomed fleet/DHT/P2P/mirror chain.
func TestOffline_UncachedDebFailsFast(t *testing.T) {
	c, idx, _ := offlineCache(t, false) // metadata present, .deb absent
	srv := serverWith(t, c, idx)
	forceOffline(t, srv)

	req := httptest.NewRequest("GET", "/"+offlineDebURL, nil)
	w := httptest.NewRecorder()
	srv.handlePackageRequest(w, req, offlineDebURL)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline uncached .deb: code=%d want 503", w.Code)
	}
}

// TestOffline_IndexWarmIsIdempotent confirms warmIndexFromCacheOnce runs its work
// only once even across multiple package requests.
func TestOffline_IndexWarmIsIdempotent(t *testing.T) {
	c, idx, _ := offlineCache(t, true)
	srv := serverWith(t, c, idx)
	forceOffline(t, srv)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/"+offlineDebURL, nil)
		w := httptest.NewRecorder()
		srv.handlePackageRequest(w, req, offlineDebURL)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: code=%d want 200", i, w.Code)
		}
	}
	if got := idx.Count(); got != 1 {
		t.Errorf("index entry count = %d, want 1 (no duplicate warms)", got)
	}
}
