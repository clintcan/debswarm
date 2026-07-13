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
	verifyEnforce = "enforce"
)

// Verification-failure reasons, surfaced in the X-Debswarm-Unverified header and
// (phase 5) in metrics/audit.
const (
	verifyReasonNoKey        = "no-key"        // keyring empty — nothing can verify
	verifyReasonNoDist       = "no-dist"       // URL has no /dists/ segment (flat repo)
	verifyReasonNoRelease    = "no-release"    // no verified Release for this dist / file not listed
	verifyReasonHashMismatch = "hash-mismatch" // index does not match the signed Release
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
	return s.verifyMode == verifyWarn || s.verifyMode == verifyEnforce
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
	dist := distBaseURL(rawURL)
	if dist == "" {
		return false, verifyReasonNoDist // flat-layout repo (no dists/ tree)
	}
	rel := s.obtainRelease(dist)
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
	relPath := strings.TrimPrefix(rawURL, dist)
	fh, ok := rel.SHA256[relPath]
	if !ok {
		return false, verifyReasonNoRelease // Release does not list this index file
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
//   - enforce + unverified (and host not exempt): refused (returns false); the
//     caller responds 502 / skips the load.
//   - warn (or enforce-exempt): allowed, with an X-Debswarm-Unverified header set
//     on w (when non-nil) and a log line. APT's own GPG check still applies.
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
	if s.verifyMode == verifyEnforce && !s.isExemptHost(rawURL) {
		log.Warn("upstream index failed signature verification; refusing (enforce mode)",
			zap.String("url", sanitize.URL(rawURL)), zap.String("reason", reason))
		return false
	}
	// warn, or enforce with an exempt host: serve but flag.
	if w != nil {
		w.Header().Set("X-Debswarm-Unverified", reason)
	}
	if reason == verifyReasonHashMismatch {
		log.Warn("upstream index does not match the signed Release (serving in warn mode; APT will reject it)",
			zap.String("url", sanitize.URL(rawURL)))
	} else {
		log.Debug("upstream index unverified", zap.String("reason", reason), zap.String("url", sanitize.URL(rawURL)))
	}
	return true
}
