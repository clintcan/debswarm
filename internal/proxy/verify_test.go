package proxy

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
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
	verSrcURL = verDist + "main/source/Sources"
	verSrcRel = "main/source/Sources"
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
	return signedReleaseCacheMulti(t, e, map[string][]byte{verPkgRel: pkgBody})
}

// signedReleaseCacheMulti is signedReleaseCache generalized to a signed InRelease
// listing several index files (relPath → body), so a single verified Release can
// vouch for both a Packages and a Sources index.
func signedReleaseCacheMulti(t *testing.T, e *openpgp.Entity, entries map[string][]byte) *cache.Cache {
	t.Helper()
	var rb bytes.Buffer
	rb.WriteString("Origin: Debian\nSuite: bookworm\nSHA256:\n")
	for rel, body := range entries {
		fmt.Fprintf(&rb, " %s %d %s\n", sha256Hex(body), len(body), rel)
	}
	releaseBody := rb.String()

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

// A Sources index verifies against the same signature-verified Release as its
// sibling Packages index — verifyIndex is path-generic, so a main/source/Sources
// entry the Release lists is vouched for identically (plain and by-hash forms).
func TestVerifyIndex_SourcesSignedReleaseMatches(t *testing.T) {
	pkgBody := []byte("Package: hello\nVersion: 2.10\nArchitecture: amd64\n\n")
	srcBody := []byte("Package: hello\nDirectory: pool/main/h/hello\n\n")
	e, kr := genKeyAndKeyring(t)
	c := signedReleaseCacheMulti(t, e, map[string][]byte{verPkgRel: pkgBody, verSrcRel: srcBody})
	srv := serverWith(t, c, index.New(t.TempDir(), newTestLogger()))
	srv.keyring = kr
	srv.verifyMode = verifyWarn

	if ok, reason := srv.verifyIndex(verSrcURL, srcBody); !ok {
		t.Fatalf("expected Sources index verified, got reason=%q", reason)
	}
	// The source by-hash form resolves via the digest the Release lists.
	byHashURL := verDist + "main/source/by-hash/SHA256/" + sha256Hex(srcBody)
	if ok, reason := srv.verifyIndex(byHashURL, nil); !ok {
		t.Fatalf("Sources by-hash should verify, got reason=%q", reason)
	}
}

// A tampered Sources index reports hash-mismatch and is refused under enforce,
// exactly like a tampered Packages index.
func TestVerifyIndex_SourcesTamperedEnforce(t *testing.T) {
	srcBody := []byte("Package: hello\nDirectory: pool/main/h/hello\n\n")
	e, kr := genKeyAndKeyring(t)
	c := signedReleaseCacheMulti(t, e, map[string][]byte{verSrcRel: srcBody})
	srv := serverWith(t, c, index.New(t.TempDir(), newTestLogger()))
	srv.keyring = kr
	srv.verifyMode = verifyEnforce

	ok, reason := srv.verifyIndex(verSrcURL, append(srcBody, 'X'))
	if ok || reason != verifyReasonHashMismatch {
		t.Fatalf("tampered Sources: ok=%v reason=%q, want false/hash-mismatch", ok, reason)
	}
	if !srv.modeRefuses(reason) {
		t.Fatal("enforce must refuse a hash-mismatch Sources index")
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
	// A flat repo (no /dists/) with no cached Release → no-release: a flat base
	// resolves (the file's directory), but nothing is cached there to verify against.
	if ok, reason := srv.verifyIndex("http://pkgs.k8s.io/core/deb/Packages", pkgBody); ok || reason != verifyReasonNoRelease {
		t.Fatalf("flat repo, no release: ok=%v reason=%q, want false/no-release", ok, reason)
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

// TestVerifyIndex_NotListed covers the reason split that auto mode depends on: a
// verified Release for the dist that does not list the requested index file is
// "not-listed" (decisive), distinct from "no-release" (no verified Release at all).
func TestVerifyIndex_NotListed(t *testing.T) {
	pkgBody := []byte("Package: hello\n\n")
	e, kr := genKeyAndKeyring(t)
	srv := verifyTestServer(t, e, kr, pkgBody, verifyWarn)

	// Same dist (its Release verifies), but an index path the Release omits.
	notListed := verDist + "main/binary-arm64/Packages"
	if ok, reason := srv.verifyIndex(notListed, pkgBody); ok || reason != verifyReasonNotListed {
		t.Fatalf("unlisted index: ok=%v reason=%q, want false/not-listed", ok, reason)
	}
}

// TestModeRefuses pins the per-mode refusal classification independent of the
// serving path: enforce refuses every failure, auto refuses only decisive ones,
// warn/off never refuse.
func TestModeRefuses(t *testing.T) {
	reasons := []string{
		verifyReasonNoKey, verifyReasonNoDist, verifyReasonNoRelease,
		verifyReasonNotListed, verifyReasonHashMismatch,
	}
	decisive := map[string]bool{verifyReasonNotListed: true, verifyReasonHashMismatch: true}
	for _, mode := range []string{verifyOff, verifyWarn, verifyAuto, verifyEnforce} {
		s := &Server{verifyMode: mode}
		for _, r := range reasons {
			want := false
			switch mode {
			case verifyEnforce:
				want = true
			case verifyAuto:
				want = decisive[r]
			}
			if got := s.modeRefuses(r); got != want {
				t.Errorf("modeRefuses(mode=%q, reason=%q) = %v, want %v", mode, r, got, want)
			}
		}
	}
}

// TestCheckIndexVerification_AutoPolicy is the end-to-end auto behavior: refuse a
// decisive failure (the signed Release exists but the index is bad), serve+flag an
// indecisive one (cannot verify), serve a verified index, and honor host exemption.
func TestCheckIndexVerification_AutoPolicy(t *testing.T) {
	pkgBody := []byte("Package: hello\n\n")
	e, kr := genKeyAndKeyring(t)
	tampered := append(append([]byte{}, pkgBody...), 'X')
	newAuto := func() *Server { return verifyTestServer(t, e, kr, pkgBody, verifyAuto) }

	// Decisive failures → refuse (like enforce).
	if newAuto().checkIndexVerification(httptest.NewRecorder(), verPkgURL, tampered, newTestLogger()) {
		t.Fatal("auto must refuse a hash-mismatched index")
	}
	notListed := verDist + "main/binary-arm64/Packages"
	if newAuto().checkIndexVerification(httptest.NewRecorder(), notListed, pkgBody, newTestLogger()) {
		t.Fatal("auto must refuse an index the signed Release does not list")
	}

	// Verified → serve, no header.
	w := httptest.NewRecorder()
	if !newAuto().checkIndexVerification(w, verPkgURL, pkgBody, newTestLogger()) {
		t.Fatal("auto must serve a verified index")
	}
	if got := w.Header().Get("X-Debswarm-Unverified"); got != "" {
		t.Fatalf("verified index set header %q, want none", got)
	}

	// Indecisive failures (cannot verify) → serve like warn, with a header.
	indecisive := map[string]string{
		"http://deb.debian.org/debian/dists/sid/main/binary-amd64/Packages": verifyReasonNoRelease, // no cached dist Release
		"http://pkgs.k8s.io/core/deb/Packages":                              verifyReasonNoRelease, // flat repo, no cached Release
	}
	for u, wantReason := range indecisive {
		rec := httptest.NewRecorder()
		if !newAuto().checkIndexVerification(rec, u, pkgBody, newTestLogger()) {
			t.Fatalf("auto must serve when it cannot verify (%s)", u)
		}
		if got := rec.Header().Get("X-Debswarm-Unverified"); got != wantReason {
			t.Fatalf("auto header for %s = %q, want %q", u, got, wantReason)
		}
	}

	// No key at all is indecisive → serve.
	nokey := newAuto()
	nokey.keyring = nil
	wk := httptest.NewRecorder()
	if !nokey.checkIndexVerification(wk, verPkgURL, tampered, newTestLogger()) {
		t.Fatal("auto must serve when there is no key to verify with")
	}
	if got := wk.Header().Get("X-Debswarm-Unverified"); got != verifyReasonNoKey {
		t.Fatalf("auto no-key header = %q, want no-key", got)
	}

	// An exempt host is served even on a decisive failure.
	exempt := newAuto()
	exempt.verifyExempt = map[string]bool{"deb.debian.org": true}
	if !exempt.checkIndexVerification(httptest.NewRecorder(), verPkgURL, tampered, newTestLogger()) {
		t.Fatal("auto must serve an exempt host even on a decisive failure")
	}
}

// --- Flat-layout repositories (no /dists/ tree, e.g. pkgs.k8s.io) ---

const (
	// A flat repo's index files and its Release live in the same directory; the
	// Release lists index files by bare name.
	verFlatBase   = "https://pkgs.k8s.io/core:/stable:/v1.31/deb/"
	verFlatPkgURL = verFlatBase + "Packages"
	verFlatPkgRel = "Packages"
)

func TestFlatBaseURL(t *testing.T) {
	cases := map[string]string{
		verFlatPkgURL:               verFlatBase,
		verFlatBase + "InRelease":   verFlatBase,
		verFlatBase + "Release":     verFlatBase,
		verFlatBase + "Packages.gz": verFlatBase,
		// A by-hash index maps to the same base as the plain files.
		verFlatBase + "by-hash/SHA256/abc123": verFlatBase,
		"http://host/only":                    "http://host/",
	}
	for in, want := range cases {
		if got := flatBaseURL(in); got != want {
			t.Errorf("flatBaseURL(%q) = %q, want %q", in, got, want)
		}
	}

	// verificationBaseURL prefers the dist base when present, else the flat base.
	if got := verificationBaseURL(verPkgURL); got != verDist {
		t.Errorf("verificationBaseURL(dist) = %q, want %q", got, verDist)
	}
	if got := verificationBaseURL(verFlatPkgURL); got != verFlatBase {
		t.Errorf("verificationBaseURL(flat) = %q, want %q", got, verFlatBase)
	}
}

// signedFlatReleaseCache builds a metadata-enabled cache whose InRelease (at the
// flat base, clearsigned by e) lists verFlatPkgRel with the SHA256 of pkgBody.
func signedFlatReleaseCache(t *testing.T, e *openpgp.Entity, pkgBody []byte) *cache.Cache {
	t.Helper()
	h := sha256Hex(pkgBody)
	releaseBody := fmt.Sprintf("Origin: Kubernetes\nSuite: stable\nSHA256:\n %s %d %s\n", h, len(pkgBody), verFlatPkgRel)

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
	mw, err := c.NewMetadataWriter(verFlatBase+"InRelease", "", "", "")
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

func TestVerifyIndex_FlatRepo(t *testing.T) {
	pkgBody := []byte("Package: kubectl\nVersion: 1.31.0-1.1\nArchitecture: amd64\n\n")
	e, kr := genKeyAndKeyring(t)
	c := signedFlatReleaseCache(t, e, pkgBody)
	srv := serverWith(t, c, index.New(t.TempDir(), newTestLogger()))
	srv.keyring = kr
	srv.verifyMode = verifyWarn

	// The flat Packages verifies against the flat-base InRelease.
	if ok, reason := srv.verifyIndex(verFlatPkgURL, pkgBody); !ok {
		t.Fatalf("flat index should verify, got reason=%q", reason)
	}

	// Tampered bytes → decisive hash-mismatch.
	if ok, reason := srv.verifyIndex(verFlatPkgURL, append(append([]byte{}, pkgBody...), 'X')); ok || reason != verifyReasonHashMismatch {
		t.Fatalf("tampered flat index: ok=%v reason=%q, want false/hash-mismatch", ok, reason)
	}

	// A file the flat Release does not list → decisive not-listed.
	if ok, reason := srv.verifyIndex(verFlatBase+"Packages.gz", pkgBody); ok || reason != verifyReasonNotListed {
		t.Fatalf("unlisted flat file: ok=%v reason=%q, want false/not-listed", ok, reason)
	}

	// A by-hash flat URL whose digest the Release vouches for verifies.
	byHash := verFlatBase + "by-hash/SHA256/" + sha256Hex(pkgBody)
	if ok, reason := srv.verifyIndex(byHash, nil); !ok {
		t.Fatalf("flat by-hash should verify, got reason=%q", reason)
	}
}

// --- On-demand Release fetch (enforce mode) ---

// clearsignBody clearsigns a Release body with e's private key.
func clearsignBody(t *testing.T, e *openpgp.Entity, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := clearsign.Encode(&buf, e.PrivateKey, nil)
	if err != nil {
		t.Fatalf("clearsign.Encode: %v", err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("clearsign close: %v", err)
	}
	return buf.Bytes()
}

// freshMetaCache returns a metadata-enabled cache with nothing cached.
func freshMetaCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.New(t.TempDir(), 100*1024*1024, newTestLogger())
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.SetMetadataMaxSize(10 * 1024 * 1024)
	return c
}

func TestObtainRelease_OnDemandFetch(t *testing.T) {
	pkgBody := []byte("Package: hello\nVersion: 2.10\nArchitecture: amd64\n\n")
	e, kr := genKeyAndKeyring(t)
	inrel := clearsignBody(t, e, fmt.Sprintf("Origin: Test\nSuite: stable\nSHA256:\n %s %d Packages\n", sha256Hex(pkgBody), len(pkgBody)))

	var served int32
	mux := http.NewServeMux()
	mux.HandleFunc("/deb/InRelease", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&served, 1)
		_, _ = w.Write(inrel)
	})
	// /deb/Release and /deb/Release.gpg default to 404 (no handler).
	up := httptest.NewServer(mux)
	defer up.Close()

	base := up.URL + "/deb/"
	pkgURL := base + "Packages"

	// enforce with an empty metadata cache: the signed Release is fetched on demand.
	s := serverWith(t, freshMetaCache(t), index.New(t.TempDir(), newTestLogger()))
	s.keyring = kr
	s.verifyMode = verifyEnforce
	if ok, reason := s.verifyIndex(pkgURL, pkgBody); !ok {
		t.Fatalf("enforce should on-demand fetch + verify the Release, got reason=%q", reason)
	}
	if got := atomic.LoadInt32(&served); got != 1 {
		t.Fatalf("expected exactly one InRelease fetch, got %d", got)
	}
	// The verified Release is now cached in the store: no second fetch.
	if ok, _ := s.verifyIndex(pkgURL, pkgBody); !ok {
		t.Fatal("second verify should hit the release store")
	}
	if got := atomic.LoadInt32(&served); got != 1 {
		t.Fatalf("second verify re-fetched the Release (served=%d, want 1)", got)
	}

	// auto/warn must NOT fetch on demand — the extra network I/O is enforce-only.
	// A fresh auto server sees the same reachable InRelease but still reports
	// no-release (and serves, since no-release is indecisive).
	before := atomic.LoadInt32(&served)
	sa := serverWith(t, freshMetaCache(t), index.New(t.TempDir(), newTestLogger()))
	sa.keyring = kr
	sa.verifyMode = verifyAuto
	if ok, reason := sa.verifyIndex(pkgURL, pkgBody); ok || reason != verifyReasonNoRelease {
		t.Fatalf("auto must not on-demand fetch: ok=%v reason=%q, want no-release", ok, reason)
	}
	if got := atomic.LoadInt32(&served); got != before {
		t.Fatalf("auto fetched the Release on demand (served %d -> %d)", before, got)
	}
}

func TestObtainRelease_OnDemandFetchUnreachable(t *testing.T) {
	pkgBody := []byte("Package: hello\n\n")
	_, kr := genKeyAndKeyring(t)

	// An upstream that 404s every metadata file.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer up.Close()

	s := serverWith(t, freshMetaCache(t), index.New(t.TempDir(), newTestLogger()))
	s.keyring = kr
	s.verifyMode = verifyEnforce
	// No obtainable Release -> no-release, and the failure is negative-cached
	// (a second call must not panic or hang).
	if ok, reason := s.verifyIndex(up.URL+"/deb/Packages", pkgBody); ok || reason != verifyReasonNoRelease {
		t.Fatalf("unreachable Release: ok=%v reason=%q, want no-release", ok, reason)
	}
	if ok, reason := s.verifyIndex(up.URL+"/deb/Packages", pkgBody); ok || reason != verifyReasonNoRelease {
		t.Fatalf("second attempt: ok=%v reason=%q, want no-release", ok, reason)
	}
}
