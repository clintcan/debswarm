package proxy

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/gpg"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/release"
)

const (
	verDist   = "http://deb.debian.org/debian/dists/bookworm/"
	verPkgURL = verDist + "main/binary-amd64/Packages"
	verPkgRel = "main/binary-amd64/Packages"
)

func TestDistBaseURL(t *testing.T) {
	cases := map[string]string{
		verDist + "InRelease": verDist,
		verDist + "Release":   verDist,
		verPkgURL:             verDist,
		verDist + "main/binary-amd64/Packages.gz":            verDist,
		verDist + "main/binary-amd64/by-hash/SHA256/abc123":  verDist,
		"http://pkgs.k8s.io/core/stable/v1.30/deb/Packages":  "", // flat repo, no /dists/
		"http://deb.debian.org/debian/pool/main/h/hello.deb": "", // pool path, no /dists/
		"http://x/dists/": "", // no suite segment
	}
	for in, want := range cases {
		if got := distBaseURL(in); got != want {
			t.Errorf("distBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestByHashDigest(t *testing.T) {
	if got := byHashDigest(verDist + "main/binary-amd64/by-hash/SHA256/ABCdef"); got != "abcdef" {
		t.Errorf("byHashDigest lowercased = %q, want abcdef", got)
	}
	if got := byHashDigest(verPkgURL); got != "" {
		t.Errorf("byHashDigest(non-by-hash) = %q, want empty", got)
	}
}

func TestReleaseStore(t *testing.T) {
	rs := newReleaseStore()
	if rs.get("d") != nil {
		t.Fatal("empty store should miss")
	}
	r := &release.Release{}
	rs.put("d", r)
	if rs.get("d") != r {
		t.Fatal("put/get mismatch")
	}
	rs.invalidate("d")
	if rs.get("d") != nil {
		t.Fatal("invalidate did not remove entry")
	}
}

// genKeyAndKeyring makes a throwaway key and a Keyring holding its public half.
func genKeyAndKeyring(t *testing.T) (*openpgp.Entity, *gpg.Keyring) {
	t.Helper()
	e, err := openpgp.NewEntity("debswarm test", "", "test@example.com", nil)
	if err != nil {
		t.Fatalf("NewEntity: %v", err)
	}
	p := filepath.Join(t.TempDir(), "test.gpg")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := e.Serialize(f); err != nil { // public key
		_ = f.Close()
		t.Fatalf("Serialize: %v", err)
	}
	_ = f.Close()
	kr, err := gpg.Load(newTestLogger(), p)
	if err != nil {
		t.Fatalf("gpg.Load: %v", err)
	}
	return e, kr
}

// signedReleaseCache builds a metadata-enabled cache whose InRelease (clearsigned
// by e) lists verPkgRel with the SHA256 of pkgBody.
func signedReleaseCache(t *testing.T, e *openpgp.Entity, pkgBody []byte) *cache.Cache {
	t.Helper()
	h := sha256Hex(pkgBody)
	releaseBody := fmt.Sprintf("Origin: Debian\nSuite: bookworm\nSHA256:\n %s %d %s\n", h, len(pkgBody), verPkgRel)

	var buf bytes.Buffer
	w, err := clearsign.Encode(&buf, e.PrivateKey, nil)
	if err != nil {
		t.Fatalf("clearsign.Encode: %v", err)
	}
	if _, err := w.Write([]byte(releaseBody)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("clearsign close: %v", err)
	}

	c, err := cache.New(t.TempDir(), 100*1024*1024, newTestLogger())
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.SetMetadataMaxSize(10 * 1024 * 1024)
	mw, err := c.NewMetadataWriter(verDist+"InRelease", "", "", "")
	if err != nil {
		t.Fatalf("NewMetadataWriter: %v", err)
	}
	if _, err := mw.Write(buf.Bytes()); err != nil {
		t.Fatalf("metadata write: %v", err)
	}
	if err := mw.Commit(); err != nil {
		t.Fatalf("metadata commit: %v", err)
	}
	return c
}

func verifyTestServer(t *testing.T, e *openpgp.Entity, kr *gpg.Keyring, pkgBody []byte, mode string) *Server {
	t.Helper()
	c := signedReleaseCache(t, e, pkgBody)
	srv := serverWith(t, c, index.New(t.TempDir(), newTestLogger()))
	srv.keyring = kr
	srv.verifyMode = mode
	return srv
}

func TestVerifyIndex_SignedReleaseMatches(t *testing.T) {
	pkgBody := []byte("Package: hello\nVersion: 2.10\nArchitecture: amd64\n\n")
	e, kr := genKeyAndKeyring(t)
	srv := verifyTestServer(t, e, kr, pkgBody, verifyWarn)

	if ok, reason := srv.verifyIndex(verPkgURL, pkgBody); !ok {
		t.Fatalf("expected verified, got reason=%q", reason)
	}

	// A by-hash URL whose digest the Release vouches for verifies (data ignored).
	byHashURL := verDist + "main/binary-amd64/by-hash/SHA256/" + sha256Hex(pkgBody)
	if ok, reason := srv.verifyIndex(byHashURL, nil); !ok {
		t.Fatalf("by-hash should verify, got reason=%q", reason)
	}
}

func TestVerifyIndex_Tampered(t *testing.T) {
	pkgBody := []byte("Package: hello\nVersion: 2.10\n\n")
	e, kr := genKeyAndKeyring(t)
	srv := verifyTestServer(t, e, kr, pkgBody, verifyWarn)

	// Body that does not match the signed Release hash.
	if ok, reason := srv.verifyIndex(verPkgURL, append(pkgBody, 'X')); ok || reason != verifyReasonHashMismatch {
		t.Fatalf("tampered index: ok=%v reason=%q, want false/hash-mismatch", ok, reason)
	}
	// A by-hash digest the Release does not list.
	bad := verDist + "main/binary-amd64/by-hash/SHA256/" + sha256Hex([]byte("something else"))
	if ok, reason := srv.verifyIndex(bad, nil); ok || reason != verifyReasonHashMismatch {
		t.Fatalf("unknown by-hash: ok=%v reason=%q, want false/hash-mismatch", ok, reason)
	}
}

func TestVerifyIndex_NoReleaseAndNoKey(t *testing.T) {
	pkgBody := []byte("Package: hello\n\n")
	e, kr := genKeyAndKeyring(t)
	srv := verifyTestServer(t, e, kr, pkgBody, verifyWarn)

	// A dist with no cached InRelease.
	other := "http://deb.debian.org/debian/dists/sid/main/binary-amd64/Packages"
	if ok, reason := srv.verifyIndex(other, pkgBody); ok || reason != verifyReasonNoRelease {
		t.Fatalf("no cached release: ok=%v reason=%q, want false/no-release", ok, reason)
	}
	// A flat repo (no /dists/) → no-dist.
	if ok, reason := srv.verifyIndex("http://pkgs.k8s.io/core/deb/Packages", pkgBody); ok || reason != verifyReasonNoDist {
		t.Fatalf("flat repo: ok=%v reason=%q, want false/no-dist", ok, reason)
	}
	// Nil keyring → no-key.
	srv.keyring = nil
	if ok, reason := srv.verifyIndex(verPkgURL, pkgBody); ok || reason != verifyReasonNoKey {
		t.Fatalf("nil keyring: ok=%v reason=%q, want false/no-key", ok, reason)
	}
}

func TestCheckIndexVerification_ModePolicy(t *testing.T) {
	pkgBody := []byte("Package: hello\n\n")
	e, kr := genKeyAndKeyring(t)
	tampered := append([]byte{}, pkgBody...)
	tampered = append(tampered, 'X')

	// off: never gates, even a tampered index passes (verification disabled).
	off := verifyTestServer(t, e, kr, pkgBody, verifyOff)
	if !off.checkIndexVerification(nil, verPkgURL, tampered, newTestLogger()) {
		t.Fatal("off mode must allow everything")
	}

	// warn: serves a tampered index but flags it with the header.
	warn := verifyTestServer(t, e, kr, pkgBody, verifyWarn)
	w := httptest.NewRecorder()
	if !warn.checkIndexVerification(w, verPkgURL, tampered, newTestLogger()) {
		t.Fatal("warn mode must serve")
	}
	if got := w.Header().Get("X-Debswarm-Unverified"); got != verifyReasonHashMismatch {
		t.Fatalf("warn header = %q, want hash-mismatch", got)
	}
	// A verified index in warn sets no header.
	w2 := httptest.NewRecorder()
	if !warn.checkIndexVerification(w2, verPkgURL, pkgBody, newTestLogger()) {
		t.Fatal("warn mode must serve a verified index")
	}
	if got := w2.Header().Get("X-Debswarm-Unverified"); got != "" {
		t.Fatalf("verified index should set no header, got %q", got)
	}

	// enforce: refuses a tampered index.
	enforce := verifyTestServer(t, e, kr, pkgBody, verifyEnforce)
	if enforce.checkIndexVerification(httptest.NewRecorder(), verPkgURL, tampered, newTestLogger()) {
		t.Fatal("enforce mode must refuse a mismatched index")
	}
	// enforce: allows a verified index.
	if !enforce.checkIndexVerification(httptest.NewRecorder(), verPkgURL, pkgBody, newTestLogger()) {
		t.Fatal("enforce mode must allow a verified index")
	}
}

func TestCheckIndexVerification_RecordsMetric(t *testing.T) {
	pkgBody := []byte("Package: hello\n\n")
	e, kr := genKeyAndKeyring(t)
	srv := verifyTestServer(t, e, kr, pkgBody, verifyWarn)

	srv.checkIndexVerification(httptest.NewRecorder(), verPkgURL, pkgBody, newTestLogger()) // verified
	tampered := append(append([]byte{}, pkgBody...), 'X')
	srv.checkIndexVerification(httptest.NewRecorder(), verPkgURL, tampered, newTestLogger()) // hash_mismatch

	vals := srv.metrics.UpstreamVerifyTotal.Values()
	if vals["verified"] != 1 {
		t.Errorf("verified count = %d, want 1 (%v)", vals["verified"], vals)
	}
	if vals["hash_mismatch"] != 1 { // reason "hash-mismatch" recorded with underscores
		t.Errorf("hash_mismatch count = %d, want 1 (%v)", vals["hash_mismatch"], vals)
	}
}

func TestCheckIndexVerification_ExemptHost(t *testing.T) {
	pkgBody := []byte("Package: hello\n\n")
	e, kr := genKeyAndKeyring(t)
	srv := verifyTestServer(t, e, kr, pkgBody, verifyEnforce)
	srv.verifyExempt = map[string]bool{"deb.debian.org": true}

	// Even a tampered index from an exempt host is served in enforce mode.
	tampered := append(append([]byte{}, pkgBody...), 'X')
	if !srv.checkIndexVerification(httptest.NewRecorder(), verPkgURL, tampered, newTestLogger()) {
		t.Fatal("enforce must allow an exempt host")
	}
}
