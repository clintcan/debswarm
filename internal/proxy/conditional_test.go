package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"go.uber.org/zap/zapcore"
)

const condLastMod = "Mon, 01 Jan 2026 00:00:00 GMT"

// condMirror simulates a mirror that honors If-Modified-Since and records
// whether each request carried it.
type condMirror struct {
	packages  []byte
	fullGets  atomic.Int64
	revalGets atomic.Int64
}

func (m *condMirror) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", condLastMod)
		if r.Header.Get("If-Modified-Since") == condLastMod {
			m.revalGets.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		m.fullGets.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(m.packages)
	}
}

// TestIndexRequest_ConditionalRevalidation verifies the full conditional-GET
// flow for Packages files: the first fetch must NOT forward the client's
// revalidation headers (our index is empty — a 304 would leave every package
// unverifiable), while a later fetch of an already-parsed index forwards them,
// relays the upstream 304, and keeps the parsed index intact.
func TestIndexRequest_ConditionalRevalidation(t *testing.T) {
	pkgPath := "pool/main/c/condpkg/condpkg_1.0_amd64.deb"
	pkgHash := "cccc567890123456789012345678901234567890123456789012345678901234"
	mirror := &condMirror{packages: fmt.Appendf(nil,
		"Package: condpkg\nVersion: 1.0\nArchitecture: amd64\nFilename: %s\nSize: 100\nSHA256: %s\n\n",
		pkgPath, pkgHash)}
	srv := httptest.NewServer(mirror.handler())
	defer srv.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)

	indexURL := srv.URL + "/dists/stable/main/binary-amd64/Packages"

	// First request: client already has a warm cache (sends IMS), but our
	// index is cold — the proxy must strip the header and fetch in full.
	req := httptest.NewRequest("GET", "/"+indexURL, nil)
	req.Header.Set("If-Modified-Since", condLastMod)
	w := httptest.NewRecorder()
	server.handleIndexRequest(w, req, indexURL)

	if w.Code != http.StatusOK {
		t.Fatalf("first fetch status = %d, want 200", w.Code)
	}
	if got := mirror.fullGets.Load(); got != 1 {
		t.Fatalf("full fetches = %d, want 1 (cold index must not revalidate)", got)
	}
	if server.index.GetBySHA256(pkgHash) == nil {
		t.Fatal("index not parsed after full fetch")
	}
	if got := w.Header().Get("Last-Modified"); got != condLastMod {
		t.Errorf("Last-Modified not relayed on 200: %q", got)
	}

	// Second request: index is parsed, client revalidates → upstream 304 is
	// relayed and the parsed entries survive untouched.
	req2 := httptest.NewRequest("GET", "/"+indexURL, nil)
	req2.Header.Set("If-Modified-Since", condLastMod)
	w2 := httptest.NewRecorder()
	server.handleIndexRequest(w2, req2, indexURL)

	if w2.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want 304", w2.Code)
	}
	if got := mirror.revalGets.Load(); got != 1 {
		t.Errorf("revalidated fetches = %d, want 1", got)
	}
	if got := mirror.fullGets.Load(); got != 1 {
		t.Errorf("full fetches after revalidation = %d, want still 1", got)
	}
	if server.index.GetBySHA256(pkgHash) == nil {
		t.Error("parsed index lost after a 304 revalidation")
	}
}

// TestPassthrough_Relays304 verifies Release-file revalidation: the client's
// If-Modified-Since goes upstream unconditionally and a 304 comes back with
// validators, with no body transfer.
func TestPassthrough_Relays304(t *testing.T) {
	mirror := &condMirror{packages: []byte("Origin: Debian\nSuite: stable\n")}
	srv := httptest.NewServer(mirror.handler())
	defer srv.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)

	relURL := srv.URL + "/dists/stable/InRelease"
	req := httptest.NewRequest("GET", "/"+relURL, nil)
	req.Header.Set("If-Modified-Since", condLastMod)
	w := httptest.NewRecorder()
	server.handlePassthrough(w, req, relURL)

	if w.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("304 carried a body of %d bytes", w.Body.Len())
	}
	if got := w.Header().Get("Last-Modified"); got != condLastMod {
		t.Errorf("Last-Modified not relayed on 304: %q", got)
	}
	if got := mirror.fullGets.Load(); got != 0 {
		t.Errorf("full fetches = %d, want 0", got)
	}
}

// TestClientCancel_LogsDebugNotError verifies that a fetch aborted because the
// CLIENT hung up (APT routinely abandons redundant index requests) is not
// logged as a server error — it used to put an ERROR line in the log on every
// apt-get update.
func TestClientCancel_LogsDebugNotError(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked // hold the request until the client gives up
	}))
	defer srv.Close()

	server, logs := newObservedServerWithMirror(t)
	defer shutdownServer(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	indexURL := srv.URL + "/dists/stable/main/binary-amd64/Packages"
	req := httptest.NewRequest("GET", "/"+indexURL, nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		server.handleIndexRequest(w, req, indexURL)
		close(done)
	}()
	cancel() // client disconnects
	<-done

	if n := logs.FilterLevelExact(zapcore.ErrorLevel).Len(); n != 0 {
		t.Errorf("client cancellation produced %d ERROR log entries, want 0: %v",
			n, logs.FilterLevelExact(zapcore.ErrorLevel).All())
	}
}
