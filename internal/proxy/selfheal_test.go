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

// TestCacheSelfHeal_OrphanedFileRedownloaded reproduces the aftermath of
// database corruption recovery: the package file exists on disk but its
// metadata row is gone, so Has() is true while Get() fails. The proxy used to
// answer 500 for every such package until a manual `cache rebuild`; it must
// instead treat the entry as a miss, re-download, and self-heal the row.
func TestCacheSelfHeal_OrphanedFileRedownloaded(t *testing.T) {
	payload := []byte("self-heal payload")
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])

	mockMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer mockMirror.Close()

	server := newTestServerWithMirror(t)
	defer shutdownServer(t, server)

	pkgPath := "pool/main/s/selfheal/selfheal_1.0_amd64.deb"
	packages := fmt.Sprintf("Package: selfheal\nVersion: 1.0\nArchitecture: amd64\nFilename: %s\nSize: %d\nSHA256: %s\n\n",
		pkgPath, len(payload), hash)
	repoURL := mockMirror.URL + "/dists/stable/main/binary-amd64/Packages"
	if err := server.index.LoadFromData([]byte(packages), repoURL); err != nil {
		t.Fatalf("LoadFromData: %v", err)
	}

	// Cache the package normally, then orphan it: delete the metadata row
	// while leaving the file on disk — exactly what corruption recovery does.
	pkgURL := mockMirror.URL + "/" + pkgPath
	req := httptest.NewRequest("GET", "/"+pkgURL, nil)
	w := httptest.NewRecorder()
	server.handlePackageRequest(w, req, pkgURL)
	if w.Code != http.StatusOK {
		t.Fatalf("initial download: status %d", w.Code)
	}
	if !server.cache.Has(hash) {
		t.Fatal("package not cached after initial download")
	}
	if _, err := server.cache.GetDB().Exec("DELETE FROM packages WHERE sha256 = ?", hash); err != nil {
		t.Fatalf("orphaning row: %v", err)
	}

	// The orphaned state: file present, row gone.
	if !server.cache.Has(hash) {
		t.Fatal("test setup broken: file should still be present")
	}

	// Request again: must NOT be a 500 — the proxy re-downloads and serves.
	req2 := httptest.NewRequest("GET", "/"+pkgURL, nil)
	w2 := httptest.NewRecorder()
	server.handlePackageRequest(w2, req2, pkgURL)

	if w2.Code != http.StatusOK {
		t.Fatalf("orphaned entry returned status %d, want 200 (self-heal re-download)", w2.Code)
	}
	if !bytes.Equal(w2.Body.Bytes(), payload) {
		t.Fatal("self-healed response body mismatch")
	}

	// And the entry is healed: a third request is a genuine cache hit.
	hitsBefore := server.metrics.CacheHits.Value()
	req3 := httptest.NewRequest("GET", "/"+pkgURL, nil)
	w3 := httptest.NewRecorder()
	server.handlePackageRequest(w3, req3, pkgURL)
	if w3.Code != http.StatusOK || !bytes.Equal(w3.Body.Bytes(), payload) {
		t.Fatal("post-heal request failed")
	}
	if server.metrics.CacheHits.Value() != hitsBefore+1 {
		t.Error("post-heal request was not a cache hit — the metadata row was not restored")
	}
}
