package proxy

import "testing"

// The proxy HTTP server must use ReadHeaderTimeout, not a blanket ReadTimeout.
// A full ReadTimeout is a deadline on the whole request lifecycle of a
// connection; on the keep-alive/pipelined connections APT uses by default, it
// fires mid-handler once a large index response (e.g. an 8 MB Packages file)
// pushes the cycle past the limit, canceling the request and stalling
// `apt-get update`. Regression for that hang.
func TestServer_UsesReadHeaderTimeoutNotReadTimeout(t *testing.T) {
	s := newTestServer(t)

	if s.server.ReadTimeout != 0 {
		t.Errorf("http.Server.ReadTimeout = %v, want 0 — a blanket ReadTimeout kills APT's "+
			"pipelined/keep-alive connections mid-handler on large indices", s.server.ReadTimeout)
	}
	if s.server.ReadHeaderTimeout == 0 {
		t.Error("http.Server.ReadHeaderTimeout = 0, want > 0 for slow-loris protection on headers")
	}
}
