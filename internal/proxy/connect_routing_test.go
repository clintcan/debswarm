package proxy

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// rawProxyRequest sends a raw HTTP request line through the server's real
// handler chain (s.server.Handler, as wired by NewServer) and returns the
// response status code. Going through the real chain matters: unit tests that
// call handleRequest directly could not see that an http.ServeMux in front of
// it answered every CONNECT with a 301.
func rawProxyRequest(t *testing.T, s *Server, requestLine, host string) int {
	t.Helper()
	ts := httptest.NewServer(s.server.Handler)
	defer ts.Close()

	conn, err := net.DialTimeout("tcp", ts.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := fmt.Fprintf(conn, "%s\r\nHost: %s\r\n\r\n", requestLine, host); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestServerHandler_ConnectReachesHandler is a regression test: CONNECT must be
// dispatched to handleConnect, not redirected. A disallowed target proves the
// request got there, because only handleConnect answers it with 403.
func TestServerHandler_ConnectReachesHandler(t *testing.T) {
	s := newTestServer(t)

	got := rawProxyRequest(t, s, "CONNECT blocked.invalid:443 HTTP/1.1", "blocked.invalid:443")
	if got == http.StatusMovedPermanently {
		t.Fatalf("CONNECT was redirected (301) instead of reaching handleConnect")
	}
	if got != http.StatusForbidden {
		t.Fatalf("CONNECT to a disallowed host: status = %d, want %d", got, http.StatusForbidden)
	}
}

// TestServerHandler_DoubleSlashPathNotRedirected ensures absolute-form proxy
// requests whose path is not canonical ("//") are handled, not 301-redirected.
func TestServerHandler_DoubleSlashPathNotRedirected(t *testing.T) {
	s := newTestServer(t)

	got := rawProxyRequest(t, s, "GET http://blocked.invalid//debian/pool/main/h/hello/hello.deb HTTP/1.1", "blocked.invalid")
	if got == http.StatusMovedPermanently {
		t.Fatalf("request with // in path was redirected (301) instead of handled")
	}
	if got != http.StatusForbidden {
		t.Fatalf("request to a disallowed host: status = %d, want %d", got, http.StatusForbidden)
	}
}
