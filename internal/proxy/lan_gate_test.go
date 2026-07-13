package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad CIDR %q: %v", s, err)
	}
	return n
}

func TestClientAllowed(t *testing.T) {
	s := &Server{allowedClientNets: []*net.IPNet{cidr(t, "192.168.1.0/24"), cidr(t, "fd00::/8")}}
	tests := []struct {
		name   string
		remote string
		xff    string
		want   bool
	}{
		{"loopback v4 always allowed", "127.0.0.1:5000", "", true},
		{"loopback v6 always allowed", "[::1]:5000", "", true},
		{"in allowlisted v4 range", "192.168.1.50:4000", "", true},
		{"outside allowlisted v4 range", "192.168.2.50:4000", "", false},
		{"public address rejected", "8.8.8.8:4000", "", false},
		{"in allowlisted v6 range", "[fd00::1]:4000", "", true},
		{"X-Forwarded-For is never trusted", "8.8.8.8:4000", "192.168.1.50", false},
		{"unparseable remote rejected", "garbage", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := s.clientAllowed(r); got != tt.want {
				t.Errorf("clientAllowed(remote=%q xff=%q) = %v, want %v", tt.remote, tt.xff, got, tt.want)
			}
		})
	}
}

func TestClientAllowed_EmptyAllowlistIsLoopbackOnly(t *testing.T) {
	s := &Server{} // no allowlist == default loopback-bound behavior
	loopback := httptest.NewRequest(http.MethodGet, "/", nil)
	loopback.RemoteAddr = "127.0.0.1:4000"
	if !s.clientAllowed(loopback) {
		t.Error("loopback client should be allowed with an empty allowlist")
	}
	lan := httptest.NewRequest(http.MethodGet, "/", nil)
	lan.RemoteAddr = "192.168.1.9:4000"
	if s.clientAllowed(lan) {
		t.Error("non-loopback client should be rejected with an empty allowlist")
	}
}

func TestGateClient(t *testing.T) {
	s := &Server{allowedClientNets: []*net.IPNet{cidr(t, "10.0.0.0/8")}}
	handler := s.gateClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	deny := httptest.NewRequest(http.MethodGet, "/", nil)
	deny.RemoteAddr = "203.0.113.5:4000"
	dw := httptest.NewRecorder()
	handler.ServeHTTP(dw, deny)
	if dw.Code != http.StatusForbidden {
		t.Errorf("rejected client: got %d, want 403", dw.Code)
	}
	if body := dw.Body.String(); strings.Contains(body, "10.0.0.0") {
		t.Errorf("403 body should not disclose the allowlist, got %q", body)
	}

	allow := httptest.NewRequest(http.MethodGet, "/", nil)
	allow.RemoteAddr = "10.1.2.3:4000"
	aw := httptest.NewRecorder()
	handler.ServeHTTP(aw, allow)
	if aw.Code != http.StatusOK {
		t.Errorf("allowlisted client: got %d, want 200", aw.Code)
	}
}

func TestBindIsLoopback(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"", true},
		{"localhost", true},
		{"127.0.0.1", true},
		{"127.0.0.53", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"::", false},
		{"192.168.1.10", false},
		{"10.0.0.1", false},
	}
	for _, tt := range tests {
		if got := bindIsLoopback(tt.host); got != tt.want {
			t.Errorf("bindIsLoopback(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}
