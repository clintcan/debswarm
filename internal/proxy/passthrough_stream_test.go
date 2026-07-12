package proxy

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestPassthrough_StreamsLargeBody verifies the passthrough path relays a large
// upstream body to the client byte-for-byte, echoes the upstream Content-Length,
// and records the transfer against the mirror counters.
func TestPassthrough_StreamsLargeBody(t *testing.T) {
	// A multi-megabyte, deterministic payload: large enough that the handler is
	// streaming rather than trivially buffering, and cheap to compare exactly.
	const size = 3 * 1024 * 1024
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251) // 251 is prime, so no alignment with any chunk size
	}

	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)

	url := mockMirror.URL + "/dists/stable/Release"
	req := httptest.NewRequest("GET", "/"+url, nil)
	w := httptest.NewRecorder()

	server.handlePassthrough(w, req, url)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.Len(); got != len(payload) {
		t.Fatalf("body length = %d, want %d", got, len(payload))
	}
	if !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatalf("streamed body does not match upstream payload")
	}
	if got := w.Header().Get("Content-Length"); got != fmt.Sprintf("%d", len(payload)) {
		t.Errorf("Content-Length header = %q, want %d", got, len(payload))
	}

	// The transfer is accounted to the mirror, not to any peer/cache source.
	if got := atomic.LoadInt64(&server.requestsMirror); got != 1 {
		t.Errorf("requestsMirror = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&server.bytesFromMirror); got != int64(len(payload)) {
		t.Errorf("bytesFromMirror = %d, want %d", got, len(payload))
	}
}

// TestPassthrough_UpstreamErrorReturns502BeforeBytes verifies that when the
// upstream returns a non-200 status, the client sees a 502 and none of the
// upstream error body leaks through — the status is decided before any bytes
// are written. The mirror byte counter must not advance on a failed fetch.
func TestPassthrough_UpstreamErrorReturns502BeforeBytes(t *testing.T) {
	const upstreamBody = "upstream 404 error page that must not reach the client"
	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)

	url := mockMirror.URL + "/dists/stable/Release"
	req := httptest.NewRequest("GET", "/"+url, nil)
	w := httptest.NewRecorder()

	server.handlePassthrough(w, req, url)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	if strings.Contains(w.Body.String(), upstreamBody) {
		t.Errorf("client body leaked upstream error content: %q", w.Body.String())
	}
	// A failed fetch delivers no bytes, so the mirror byte counter stays at zero.
	if got := atomic.LoadInt64(&server.bytesFromMirror); got != 0 {
		t.Errorf("bytesFromMirror = %d, want 0 on upstream error", got)
	}
}
