package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// indexPackage registers a package payload in the server's index so the
// download path treats it as signed/verifiable, and returns its request URL.
func indexPackage(t *testing.T, server *Server, mirrorURL, pkgPath string, payload []byte) string {
	t.Helper()
	sum := sha256.Sum256(payload)
	packages := fmt.Sprintf("Package: streampkg\nVersion: 1.0\nArchitecture: amd64\nFilename: %s\nSize: %d\nSHA256: %s\n\n",
		pkgPath, len(payload), hex.EncodeToString(sum[:]))
	repoURL := mirrorURL + "/dists/stable/main/binary-amd64/Packages"
	if err := server.index.LoadFromData([]byte(packages), repoURL); err != nil {
		t.Fatalf("LoadFromData: %v", err)
	}
	return mirrorURL + "/" + pkgPath
}

// TestMirrorFallback_LargePackageStreamedToCacheAndServed verifies the mirror
// fallback path: a multi-megabyte indexed package is verified, cached, and
// served byte-for-byte. The path streams into the cache and serves from the
// cached file, so this also covers the serveFromCache result plumbing.
func TestMirrorFallback_LargePackageStreamedToCacheAndServed(t *testing.T) {
	const size = 5 * 1024 * 1024
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)

	pkgURL := indexPackage(t, server, mockMirror.URL, "pool/main/s/streampkg/streampkg_1.0_amd64.deb", payload)

	req := httptest.NewRequest("GET", "/"+pkgURL, nil)
	w := httptest.NewRecorder()
	server.handlePackageRequest(w, req, pkgURL)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatalf("served body does not match upstream payload (len %d vs %d)", w.Body.Len(), len(payload))
	}
	if got := w.Header().Get("X-Debswarm-Source"); got != "mirror" {
		t.Errorf("X-Debswarm-Source = %q, want mirror", got)
	}
	if got := server.cache.Count(); got != 1 {
		t.Errorf("cache count = %d, want 1 (verified package must be cached)", got)
	}

	// A second request must be a cache hit served with identical content.
	req2 := httptest.NewRequest("GET", "/"+pkgURL, nil)
	w2 := httptest.NewRecorder()
	server.handlePackageRequest(w2, req2, pkgURL)
	if w2.Code != http.StatusOK || !bytes.Equal(w2.Body.Bytes(), payload) {
		t.Fatalf("cache-hit replay mismatch: status %d, len %d", w2.Code, w2.Body.Len())
	}
	if got := server.metrics.CacheHits.Value(); got != 1 {
		t.Errorf("CacheHits = %d, want 1", got)
	}
}

// TestMirrorFallback_HashMismatchRejectedAndNotCached verifies that a mirror
// serving content that does not match the signed index hash results in a 502
// and nothing cached — the verification gate must hold on the streaming path.
func TestMirrorFallback_HashMismatchRejectedAndNotCached(t *testing.T) {
	goodPayload := []byte("the payload the index was signed for")
	evilPayload := []byte("something else entirely served by the mirror")

	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(evilPayload)
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)

	// Index is signed for goodPayload; the mirror serves evilPayload.
	pkgURL := indexPackage(t, server, mockMirror.URL, "pool/main/s/streampkg/streampkg_1.0_amd64.deb", goodPayload)

	req := httptest.NewRequest("GET", "/"+pkgURL, nil)
	w := httptest.NewRecorder()
	server.handlePackageRequest(w, req, pkgURL)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for hash mismatch", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), evilPayload) {
		t.Error("unverified mirror content leaked to the client")
	}
	if got := server.cache.Count(); got != 0 {
		t.Errorf("cache count = %d, want 0 (mismatched content must not be cached)", got)
	}
	if got := server.metrics.VerificationFailures.Value(); got < 1 {
		t.Errorf("VerificationFailures = %d, want >= 1", got)
	}
}
