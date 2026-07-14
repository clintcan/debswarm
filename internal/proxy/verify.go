package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/release"
	"github.com/debswarm/debswarm/internal/sanitize"
)

// Upstream signature-verification modes (mirrors config.Verify*; kept as local
// string constants so the proxy package need not import config).
const (
	verifyOff     = "off"
	verifyWarn    = "warn"
	verifyAuto    = "auto"
	verifyEnforce = "enforce"
)

// Verification-failure reasons, surfaced in the X-Debswarm-Unverified header and
// in metrics/audit. The reasons split into two classes that drive the auto-mode
// decision: "decisive" failures mean verification was possible and the index is
// bad (a signature-verified Release exists but the index does not match it);
// "indecisive" failures mean verification could not be attempted at all.
const (
	verifyReasonNoKey        = "no-key"        // keyring empty / no trusted key — cannot verify (indecisive)
	verifyReasonNoDist       = "no-dist"       // no resolvable repository base in the URL — cannot verify (indecisive)
	verifyReasonNoRelease    = "no-release"    // no signature-verified Release for this base (dist or flat) — cannot verify (indecisive)
	verifyReasonNotListed    = "not-listed"    // Release verified but does not list this index file (decisive)
	verifyReasonHashMismatch = "hash-mismatch" // index does not match the hash in the signed Release (decisive)
)

// releaseStore caches parsed, signature-verified Release files keyed by dist base
// URL (".../dists/<suite>/"). A fresh Release request invalidates its dist so the
// next index request re-verifies against the new Release.
type releaseStore struct {
	mu     sync.RWMutex
	byDist map[string]*release.Release
}

func newReleaseStore() *releaseStore {
	return &releaseStore{byDist: make(map[string]*release.Release)}
}

func (rs *releaseStore) get(dist string) *release.Release {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.byDist[dist]
}

func (rs *releaseStore) put(dist string, r *release.Release) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.byDist[dist] = r
}

func (rs *releaseStore) invalidate(dist string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.byDist, dist)
}

// distBaseURL returns the repository URL up to and including "/dists/<suite>/",
// or "" if the URL has no such segment (a flat-layout repo). Both a Release URL
// (.../dists/bookworm/InRelease) and an index URL
// (.../dists/bookworm/main/binary-amd64/Packages.gz) map to the same base.
func distBaseURL(rawURL string) string {
	const marker = "/dists/"
	i := strings.Index(rawURL, marker)
	if i < 0 {
		return ""
	}
	rest := rawURL[i+len(marker):]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return ""
	}
	return rawURL[:i+len(marker)+slash+1]
}

// flatBaseURL returns the base URL of a flat-layout repository (one with no
// /dists/ tree, e.g. pkgs.k8s.io) for an index or Release file: the directory
// containing the file, including the trailing slash. The Release/InRelease of a
// flat repo lives in that same directory and lists index files by their bare
// names (e.g. "Packages", "Packages.gz"). For an Acquire-By-Hash URL the base is
// the directory containing the /by-hash/ segment, so a by-hash index maps to the
// same base as the plain files. Returns "" only for a URL with no path segment.
func flatBaseURL(rawURL string) string {
	if j := strings.Index(rawURL, "/by-hash/"); j >= 0 {
		return rawURL[:j+1]
	}
	i := strings.LastIndexByte(rawURL, '/')
	if i < 0 {
		return ""
	}
	return rawURL[:i+1]
}

// verificationBaseURL returns the repository base URL that a Release verifies an
// index (or another Release file) against: the dist base ("/dists/<suite>/") when
// present, otherwise the flat-repo base (the file's directory). This is the key
// under which the verified Release is stored, so an index and its Release resolve
// to the same base whether the repo is dist- or flat-layout.
func verificationBaseURL(rawURL string) string {
	if d := distBaseURL(rawURL); d != "" {
		return d
	}
	return flatBaseURL(rawURL)
}

// byHashDigest returns the hex digest embedded in an Acquire-By-Hash URL
// (.../by-hash/SHA256/<hex>), or "" if the URL is not by-hash.
func byHashDigest(rawURL string) string {
	const marker = "/by-hash/SHA256/"
	i := strings.Index(rawURL, marker)
	if i < 0 {
		return ""
	}
	return strings.ToLower(rawURL[i+len(marker):])
}

// verificationEnabled reports whether any verification work runs (mode != off).
func (s *Server) verificationEnabled() bool {
	return s.verifyMode == verifyWarn || s.verifyMode == verifyAuto || s.verifyMode == verifyEnforce
}

// modeRefuses reports whether the current mode refuses an index that failed
// verification for the given reason (host exemption is handled by the caller):
//
//   - enforce refuses every failure, including "cannot verify" — fail-closed.
//   - auto refuses only decisive failures (the signed Release exists but the index
//     does not match it); "cannot verify" reasons fall back to warn (serve+flag).
//   - warn (and any other mode) never refuses.
func (s *Server) modeRefuses(reason string) bool {
	switch s.verifyMode {
	case verifyEnforce:
		return true
	case verifyAuto:
		return reason == verifyReasonHashMismatch || reason == verifyReasonNotListed
	default:
		return false
	}
}

// recordVerifyResult increments the upstream-verify counter, labeled by result
// only (result values use underscores; the reason constants use hyphens). Never
// labeled by repo/URL, to keep metric cardinality bounded.
func (s *Server) recordVerifyResult(result string) {
	if s.metrics == nil {
		return
	}
	s.metrics.UpstreamVerifyTotal.WithLabel(strings.ReplaceAll(result, "-", "_")).Inc()
}

// isExemptHost reports whether the URL's host is on the operator's exempt list
// (served even when unverifiable, effective only in enforce mode).
func (s *Server) isExemptHost(rawURL string) bool {
	if len(s.verifyExempt) == 0 {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return s.verifyExempt[strings.ToLower(u.Hostname())]
}

// verifyIndex checks a Packages index body against the signed Release for its
// dist. It returns (true, "") when the index's SHA256 is vouched for by a
// signature-verified Release, else (false, reason).
func (s *Server) verifyIndex(rawURL string, data []byte) (bool, string) {
	if s.keyring == nil || s.keyring.Empty() {
		return false, verifyReasonNoKey
	}
	// dist-layout repos anchor on "/dists/<suite>/"; flat-layout repos (no dists/
	// tree, e.g. pkgs.k8s.io) anchor on the index file's own directory, where
	// their Release lives and lists index files by bare name.
	base := verificationBaseURL(rawURL)
	if base == "" {
		return false, verifyReasonNoDist // no resolvable repository base
	}
	rel := s.obtainRelease(base)
	if rel == nil {
		return false, verifyReasonNoRelease
	}

	// Acquire-By-Hash: the digest is in the URL and is immutable; verified iff the
	// signed Release lists that hash.
	if digest := byHashDigest(rawURL); digest != "" {
		if rel.HasHash(digest) {
			return true, ""
		}
		return false, verifyReasonHashMismatch
	}

	// Plain path: the Release must list this file, and its bytes must hash to the
	// listed value.
	relPath := strings.TrimPrefix(rawURL, base)
	fh, ok := rel.SHA256[relPath]
	if !ok {
		// The Release verified but does not vouch for this file — decisive (auto
		// and enforce refuse), distinct from "no verified Release available".
		return false, verifyReasonNotListed
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != fh.SHA256 {
		return false, verifyReasonHashMismatch
	}
	return true, ""
}

// obtainRelease returns the signature-verified, parsed Release for a dist,
// caching it in the store. It reads the InRelease (or Release + Release.gpg) from
// the metadata cache and verifies it against the keyring. Verification therefore
// requires the metadata cache to be enabled (the Release is cached there when APT
// fetches it, before any Packages request). Returns nil if no verified Release is
// available.
//
// A live mirror fetch of a missing Release is intentionally NOT done here (it
// would put network I/O on the index-serving path); in practice APT fetches the
// Release before the Packages, so the cache already holds it. Enforce mode
// therefore refuses only when the Release was never fetched/cached.
func (s *Server) obtainRelease(dist string) *release.Release {
	if r := s.releaseStore.get(dist); r != nil {
		return r
	}
	if s.cache == nil || !s.cache.MetadataEnabled() {
		return nil
	}

	// InRelease: clearsigned, self-contained.
	if body := s.readCachedMetadataBody(dist + "InRelease"); body != nil {
		if verified, err := s.keyring.VerifyClearsigned(body); err == nil {
			if rel, perr := release.Parse(verified); perr == nil {
				s.releaseStore.put(dist, rel)
				return rel
			}
		}
	}

	// Release + detached Release.gpg.
	if body := s.readCachedMetadataBody(dist + "Release"); body != nil {
		if sig := s.readCachedMetadataBody(dist + "Release.gpg"); sig != nil {
			if err := s.keyring.VerifyDetached(body, sig); err == nil {
				if rel, perr := release.Parse(body); perr == nil {
					s.releaseStore.put(dist, rel)
					return rel
				}
			}
		}
	}
	return nil
}

// readCachedMetadataBody returns the full cached body for a metadata URL, or nil
// if it is not cached or unreadable. The read is bounded by the entry's size.
func (s *Server) readCachedMetadataBody(rawURL string) []byte {
	entry, rc, err := s.cache.GetMetadata(rawURL)
	if err != nil {
		return nil
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, entry.Size))
	if err != nil {
		return nil
	}
	return data
}

// checkIndexVerification applies the verification policy to a Packages index body
// about to be parsed into the in-memory index (and, at the serving sites, sent to
// the client). It returns whether the index may be loaded/served:
//
//   - off, or a non-Packages URL: always allowed (no-op).
//   - verified: allowed.
//   - a refusing mode for this failure (enforce on any failure; auto on a decisive
//     failure — hash-mismatch/not-listed) and host not exempt: refused (returns
//     false); the caller responds 502 / skips the load.
//   - otherwise (warn; auto on an indecisive failure; a refusing-but-exempt host):
//     allowed, with an X-Debswarm-Unverified header set on w (when non-nil) and a
//     log line. APT's own GPG check still applies.
//
// w may be nil for non-serving callers (the startup index warm), in which case
// only the load decision matters.
func (s *Server) checkIndexVerification(w http.ResponseWriter, rawURL string, data []byte, log *zap.Logger) bool {
	if !s.verificationEnabled() || !isPackagesIndexURL(rawURL) {
		return true
	}
	verified, reason := s.verifyIndex(rawURL, data)
	if verified {
		s.recordVerifyResult("verified")
		return true
	}
	s.recordVerifyResult(reason)
	if s.modeRefuses(reason) && !s.isExemptHost(rawURL) {
		log.Warn("upstream index failed signature verification; refusing",
			zap.String("mode", s.verifyMode), zap.String("reason", reason), zap.String("url", sanitize.URL(rawURL)))
		return false
	}
	// warn (or a refusing mode that either does not refuse this reason or exempts
	// the host): serve but flag.
	if w != nil {
		w.Header().Set("X-Debswarm-Unverified", reason)
	}
	if reason == verifyReasonHashMismatch || reason == verifyReasonNotListed {
		log.Warn("upstream index does not match the signed Release (serving; APT will reject it)",
			zap.String("mode", s.verifyMode), zap.String("reason", reason), zap.String("url", sanitize.URL(rawURL)))
	} else {
		log.Debug("upstream index unverified", zap.String("reason", reason), zap.String("mode", s.verifyMode), zap.String("url", sanitize.URL(rawURL)))
	}
	return true
}
