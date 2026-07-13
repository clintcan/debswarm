// Package index handles parsing and caching of Debian Packages indices
package index

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/httpclient"
	"github.com/debswarm/debswarm/internal/sanitize"
	"github.com/debswarm/debswarm/internal/security"
)

const (
	// maxDecompressedBytes limits decompressed Packages file size to prevent
	// decompression bombs. 512MB is generous for even the largest repos
	// (Debian main has ~60K packages, ~50MB uncompressed).
	maxDecompressedBytes = 512 * 1024 * 1024

	// maxPackagesPerRepo limits how many packages can be indexed from a single
	// repo to prevent unbounded memory growth from malicious Packages files.
	maxPackagesPerRepo = 500_000
)

// PackageInfo holds information about a single package.
// Only fields the proxy actually consumes are retained: parsing and storing
// unused Packages-file fields (Description, SHA512) cost tens of MB of
// resident strings across a full Debian index.
type PackageInfo struct {
	Package      string
	Version      string
	Architecture string
	Filename     string
	Size         int64
	SHA256       string
	Repo         string // Repository base URL this package belongs to
}

// Index manages package index files from multiple repositories
type Index struct {
	cachePath  string
	packages   map[string]*PackageInfo            // keyed by SHA256
	byRepo     map[string]map[string]*PackageInfo // repo → path → pkg
	byBasename map[string][]*PackageInfo          // basename → packages (for O(1) lookup)
	// byIndexFile tracks which entries each logical index file (see
	// indexFileKey) contributed, so a re-parse replaces exactly its own
	// previous generation. A repo key is too coarse for this: multiple dists,
	// components, and architectures of one repository share a repo key, and
	// clearing per repo would wipe bookworm/main when bookworm-updates parses.
	byIndexFile map[string][]*PackageInfo
	mu          sync.RWMutex
	logger      *zap.Logger
	client      *http.Client
}

// New creates a new Index manager
func New(cachePath string, logger *zap.Logger) *Index {
	return &Index{
		cachePath:   cachePath,
		packages:    make(map[string]*PackageInfo),
		byRepo:      make(map[string]map[string]*PackageInfo),
		byBasename:  make(map[string][]*PackageInfo),
		byIndexFile: make(map[string][]*PackageInfo),
		logger:      logger,
		client:      httpclient.Default(),
	}
}

// HasIndexFile reports whether a parse of the given index URL (or path) is
// currently loaded. The proxy uses this to decide whether it may forward a
// client's revalidation headers upstream: a 304 is only safe to relay when the
// in-memory index already holds this file's entries (e.g. NOT right after a
// daemon restart, when the client's cache is warm but ours is empty).
func (idx *Index) HasIndexFile(urlOrPath string) bool {
	key := indexFileKey(urlOrPath)
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.byIndexFile[key]) > 0
}

// indexFileKey derives a stable identity for a Packages index file from its
// URL or filesystem path. By-hash digests change on every repository update,
// so they collapse to the index directory; compression extensions are ignored
// so compressed variants of the same index share one key.
func indexFileKey(s string) string {
	if i := strings.Index(s, "/by-hash/"); i >= 0 {
		return s[:i]
	}
	for _, ext := range []string{".gz", ".xz", ".lz4", ".bz2", ".zst"} {
		s = strings.TrimSuffix(s, ext)
	}
	return s
}

// decompressByName wraps r with the decompressor matching the file/URL name's
// extension, applying the decompression-bomb size limit. APT writes lists as
// .gz, .xz, .lz4 (Ubuntu minimized/cloud images default to lz4 via
// Acquire::GzipIndexes), .bz2, or .zst — an unsupported one used to be
// scanned as raw binary, silently contributing zero index entries.
func decompressByName(r io.Reader, name string) (io.Reader, error) {
	switch {
	case strings.HasSuffix(name, ".gz"):
		gzReader, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		return io.LimitReader(gzReader, maxDecompressedBytes), nil
	case strings.HasSuffix(name, ".xz"):
		xzReader, err := xz.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("failed to create xz reader: %w", err)
		}
		return io.LimitReader(xzReader, maxDecompressedBytes), nil
	case strings.HasSuffix(name, ".lz4"):
		return io.LimitReader(lz4.NewReader(r), maxDecompressedBytes), nil
	case strings.HasSuffix(name, ".bz2"):
		return io.LimitReader(bzip2.NewReader(r), maxDecompressedBytes), nil
	case strings.HasSuffix(name, ".zst"):
		zstReader, err := zstd.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("failed to create zstd reader: %w", err)
		}
		return io.LimitReader(zstReader.IOReadCloser(), maxDecompressedBytes), nil
	default:
		return r, nil
	}
}

// LoadFromFile loads and parses a Packages file
func (idx *Index) LoadFromFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Handle compression (limit decompressed size to prevent decompression bombs)
	reader, err := decompressByName(f, path)
	if err != nil {
		return err
	}

	// Use filename as repo identifier for local files
	return idx.parseForRepo(reader, path, indexFileKey(path))
}

// LoadFromFileWithRepo loads and parses a Packages file with an explicit repo identifier.
// This is useful for APT list files where the repo should be extracted from the filename.
func (idx *Index) LoadFromFileWithRepo(path, repo string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Handle compression based on extension (limit decompressed size)
	reader, err := decompressByName(f, path)
	if err != nil {
		return err
	}

	return idx.parseForRepo(reader, repo, indexFileKey(path))
}

// LoadFromURL downloads and parses a Packages file from a URL
func (idx *Index) LoadFromURL(url string) error {
	// SECURITY: Validate URL to prevent SSRF attacks
	if !isAllowedIndexURL(url) {
		return fmt.Errorf("blocked request to non-allowed URL: %s", url)
	}

	idx.logger.Debug("Fetching package index", zap.String("url", sanitize.URL(url)))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := idx.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d: %s", resp.StatusCode, resp.Status)
	}

	// Handle compression based on URL (limit decompressed size)
	reader, err := decompressByName(resp.Body, url)
	if err != nil {
		return err
	}

	// Extract repo base URL
	repo := ExtractRepoFromURL(url)
	return idx.parseForRepo(reader, repo, indexFileKey(url))
}

// LoadFromData parses Packages data for a specific repository
func (idx *Index) LoadFromData(data []byte, url string) error {
	var reader io.Reader = bytes.NewReader(data)

	// Detect compression from magic bytes (for by-hash URLs that lack file extensions)
	// gzip magic: 0x1f 0x8b
	// xz magic: 0xfd '7' 'z' 'X' 'Z' 0x00
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gzReader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer func() { _ = gzReader.Close() }()
		reader = io.LimitReader(gzReader, maxDecompressedBytes)
	} else if len(data) >= 6 && data[0] == 0xfd && data[1] == '7' && data[2] == 'z' && data[3] == 'X' && data[4] == 'Z' && data[5] == 0x00 {
		xzReader, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("failed to create xz reader: %w", err)
		}
		reader = io.LimitReader(xzReader, maxDecompressedBytes)
	} else if len(data) >= 4 && data[0] == 0x28 && data[1] == 0xb5 && data[2] == 0x2f && data[3] == 0xfd {
		// zstd magic
		zstReader, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("failed to create zstd reader: %w", err)
		}
		reader = io.LimitReader(zstReader.IOReadCloser(), maxDecompressedBytes)
	} else if len(data) >= 4 && data[0] == 0x04 && data[1] == 0x22 && data[2] == 0x4d && data[3] == 0x18 {
		// lz4 frame magic
		reader = io.LimitReader(lz4.NewReader(bytes.NewReader(data)), maxDecompressedBytes)
	}
	// Otherwise assume uncompressed

	repo := ExtractRepoFromURL(url)
	return idx.parseForRepo(reader, repo, indexFileKey(url))
}

// parseForRepo parses an uncompressed Packages file for a specific repository.
// fileKey identifies the logical index file (see indexFileKey); a re-parse of
// the same file replaces its previous generation of entries instead of
// accumulating them.
func (idx *Index) parseForRepo(reader io.Reader, repo, fileKey string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Drop the previous generation of this index file's entries, then rebuild.
	idx.clearIndexFileLocked(fileKey)

	// Ensure repo map exists
	if idx.byRepo[repo] == nil {
		idx.byRepo[repo] = make(map[string]*PackageInfo)
	}

	var generation []*PackageInfo

	scanner := bufio.NewScanner(reader)
	// Increase buffer size for long lines (descriptions can be long)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var pkg *PackageInfo
	count := 0

	for scanner.Scan() {
		line := scanner.Text()

		// Empty line marks end of package entry
		if line == "" {
			if pkg != nil && pkg.SHA256 != "" {
				pkg.Repo = repo
				idx.packages[pkg.SHA256] = pkg
				generation = append(generation, pkg)
				if pkg.Filename != "" {
					idx.byRepo[repo][pkg.Filename] = pkg
					// Add to basename index for O(1) lookups
					basename := filepath.Base(pkg.Filename)
					idx.byBasename[basename] = append(idx.byBasename[basename], pkg)
				}
				count++
				if count >= maxPackagesPerRepo {
					idx.logger.Warn("Package index limit reached, truncating",
						zap.String("repo", repo),
						zap.Int("limit", maxPackagesPerRepo))
					break
				}
			}
			pkg = nil
			continue
		}

		// Start new package if needed
		if pkg == nil {
			pkg = &PackageInfo{}
		}

		// Parse field
		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue // Continuation line or invalid
		}

		field := line[:colonIdx]
		value := strings.TrimSpace(line[colonIdx+1:])

		switch field {
		case "Package":
			pkg.Package = value
		case "Version":
			pkg.Version = value
		case "Architecture":
			pkg.Architecture = value
		case "Filename":
			pkg.Filename = value
		case "Size":
			// Ignore parse error; size defaults to 0 if invalid
			size, err := strconv.ParseInt(value, 10, 64)
			if err == nil {
				pkg.Size = size
			}
		case "SHA256":
			pkg.SHA256 = value
		}
	}

	// Handle last package
	if pkg != nil && pkg.SHA256 != "" {
		pkg.Repo = repo
		idx.packages[pkg.SHA256] = pkg
		generation = append(generation, pkg)
		if pkg.Filename != "" {
			idx.byRepo[repo][pkg.Filename] = pkg
			// Add to basename index for O(1) lookups
			basename := filepath.Base(pkg.Filename)
			idx.byBasename[basename] = append(idx.byBasename[basename], pkg)
		}
		count++
	}

	if len(generation) > 0 {
		idx.byIndexFile[fileKey] = generation
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	idx.logger.Debug("Parsed package index",
		zap.String("repo", repo),
		zap.Int("packages", count),
		zap.Int("totalRepos", len(idx.byRepo)))
	return nil
}

// GetBySHA256 returns package info by SHA256 hash
func (idx *Index) GetBySHA256(sha256 string) *PackageInfo {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.packages[sha256]
}

// GetByRepoAndPath returns package info for a specific repo and path
func (idx *Index) GetByRepoAndPath(repo, path string) *PackageInfo {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if repoMap := idx.byRepo[repo]; repoMap != nil {
		if pkg := repoMap[path]; pkg != nil {
			return pkg
		}
	}
	return nil
}

// GetByPath returns package info by filename/path, searching all repositories
// If multiple repos have the same path, returns any match (use GetByRepoAndPath for specific repo)
func (idx *Index) GetByPath(path string) *PackageInfo {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// Search all repos for exact match
	for _, repoMap := range idx.byRepo {
		if pkg := repoMap[path]; pkg != nil {
			return pkg
		}
	}

	// O(1) lookup by basename using the secondary index
	base := filepath.Base(path)
	if packages := idx.byBasename[base]; len(packages) > 0 {
		return packages[0] // Return first match
	}

	return nil
}

// GetByURLPath extracts repo and path from a URL and looks up the package
func (idx *Index) GetByURLPath(rawURL string) *PackageInfo {
	repo := ExtractRepoFromURL(rawURL)
	if repo == "" {
		return nil
	}

	pathKey := ExtractPathFromURL(rawURL)
	if pathKey == "" {
		// Flat-layout repositories (e.g. pkgs.k8s.io) serve packages directly with
		// no dists/pool tree, so ExtractPathFromURL yields no path. Match on the
		// package basename instead, preferring the same repo to avoid cross-repo
		// basename collisions.
		if base := basenameFromURL(rawURL); base != "" {
			return idx.GetByBasename(base, repo)
		}
		return nil
	}

	// Try specific repo first
	if pkg := idx.GetByRepoAndPath(repo, pathKey); pkg != nil {
		return pkg
	}

	// Fall back to any repo with this path
	return idx.GetByPath(pathKey)
}

// GetByBasename returns package info by file basename, preferring a match from
// preferRepo when several repositories share the basename. Used for flat-layout
// repositories whose URLs carry no dists/pool path.
func (idx *Index) GetByBasename(basename, preferRepo string) *PackageInfo {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	packages := idx.byBasename[basename]
	if len(packages) == 0 {
		return nil
	}
	if preferRepo != "" {
		for _, pkg := range packages {
			if pkg.Repo == preferRepo {
				return pkg
			}
		}
	}
	return packages[0]
}

// basenameFromURL returns the final path segment of a URL (its filename),
// ignoring any query string. Returns "" if the URL cannot be parsed.
func basenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" {
		return ""
	}
	return base
}

// GetByPathSuffix returns package info by path suffix (for URL matching)
func (idx *Index) GetByPathSuffix(suffix string) *PackageInfo {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	for _, repoMap := range idx.byRepo {
		for filename, pkg := range repoMap {
			if strings.HasSuffix(filename, suffix) || strings.HasSuffix(suffix, filename) {
				return pkg
			}
		}
	}
	return nil
}

// Count returns the number of indexed packages (unique by SHA256)
func (idx *Index) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.packages)
}

// RepoCount returns the number of indexed repositories
func (idx *Index) RepoCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.byRepo)
}

// Clear removes all indexed packages
func (idx *Index) Clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.packages = make(map[string]*PackageInfo)
	idx.byRepo = make(map[string]map[string]*PackageInfo)
	idx.byBasename = make(map[string][]*PackageInfo)
	idx.byIndexFile = make(map[string][]*PackageInfo)
}

// ClearRepo removes packages from a specific repository
func (idx *Index) ClearRepo(repo string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.clearRepoLocked(repo)
}

// clearRepoLocked removes a repo's packages from all lookup maps. The caller
// must hold idx.mu.
func (idx *Index) clearRepoLocked(repo string) {
	if repoMap := idx.byRepo[repo]; repoMap != nil {
		for _, pkg := range repoMap {
			// Only drop the global entry if this repo's parse still owns it — a
			// later parse of another repo listing the same package (same SHA256)
			// overwrites packages[sha], and that entry must survive.
			if cur, ok := idx.packages[pkg.SHA256]; ok && cur == pkg {
				delete(idx.packages, pkg.SHA256)
			}
			// Remove from basename index
			if pkg.Filename != "" {
				basename := filepath.Base(pkg.Filename)
				if packages := idx.byBasename[basename]; len(packages) > 0 {
					// Filter out packages from this repo
					filtered := packages[:0]
					for _, p := range packages {
						if p.Repo != repo {
							filtered = append(filtered, p)
						}
					}
					if len(filtered) == 0 {
						delete(idx.byBasename, basename)
					} else {
						idx.byBasename[basename] = filtered
					}
				}
			}
		}
	}
	delete(idx.byRepo, repo)

	// Keep the per-file generation lists consistent
	for key, gen := range idx.byIndexFile {
		filtered := gen[:0]
		for _, p := range gen {
			if p.Repo != repo {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			delete(idx.byIndexFile, key)
		} else {
			idx.byIndexFile[key] = filtered
		}
	}
}

// clearIndexFileLocked removes the entries a previous parse of the same
// logical index file contributed. The caller must hold idx.mu. parseForRepo
// runs this before inserting a fresh parse: without it, byBasename appended a
// new generation of entries on every re-parse while keeping the old one
// reachable — unbounded memory growth in a long-running daemon (roughly
// 20-30MB per re-parse of Debian main). Removal is by pointer identity, so an
// entry that a different index file's parse now owns is never touched.
func (idx *Index) clearIndexFileLocked(fileKey string) {
	gen := idx.byIndexFile[fileKey]
	if len(gen) == 0 {
		return
	}
	old := make(map[*PackageInfo]struct{}, len(gen))
	for _, pkg := range gen {
		old[pkg] = struct{}{}
	}

	for _, pkg := range gen {
		if cur, ok := idx.packages[pkg.SHA256]; ok && cur == pkg {
			delete(idx.packages, pkg.SHA256)
		}
		if repoMap := idx.byRepo[pkg.Repo]; repoMap != nil {
			if cur, ok := repoMap[pkg.Filename]; ok && cur == pkg {
				delete(repoMap, pkg.Filename)
			}
			if len(repoMap) == 0 {
				delete(idx.byRepo, pkg.Repo)
			}
		}
		if pkg.Filename != "" {
			basename := filepath.Base(pkg.Filename)
			if packages := idx.byBasename[basename]; len(packages) > 0 {
				filtered := packages[:0]
				for _, p := range packages {
					if _, isOld := old[p]; !isOld {
						filtered = append(filtered, p)
					}
				}
				if len(filtered) == 0 {
					delete(idx.byBasename, basename)
				} else {
					idx.byBasename[basename] = filtered
				}
			}
		}
	}
	delete(idx.byIndexFile, fileKey)
}

// ExtractRepoFromURL extracts the repository base URL from a full URL
// e.g., "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.gz"
//
//	-> "deb.debian.org/debian"
func ExtractRepoFromURL(url string) string {
	// Remove protocol
	s := url
	if strings.HasPrefix(s, "https://") {
		s = s[8:]
	} else if strings.HasPrefix(s, "http://") {
		s = s[7:]
	}

	// Find dists/ or pool/ and take everything before it
	for _, marker := range []string{"/dists/", "/pool/"} {
		if idx := strings.Index(s, marker); idx != -1 {
			return s[:idx]
		}
	}

	// Fallback: return host only
	if idx := strings.Index(s, "/"); idx != -1 {
		return s[:idx]
	}
	return s
}

// ExtractPathFromURL extracts the package path from a full URL
// e.g., "http://deb.debian.org/debian/pool/main/v/vim/vim_9.0.deb" -> "pool/main/v/vim/vim_9.0.deb"
func ExtractPathFromURL(rawURL string) string {
	// Find the pool/ or dists/ part
	for _, marker := range []string{"/pool/", "/dists/"} {
		if idx := strings.Index(rawURL, marker); idx != -1 {
			p := rawURL[idx+1:] // Include the leading part after /
			// APT percent-encodes characters in package URLs — notably '+' as
			// %2B, which is extremely common in Debian versions (e.g. the
			// "+deb12u2" / "+dfsg" / "+b1" suffixes). The index is keyed by the
			// Packages "Filename:" field, which is not encoded, so decode here or
			// the lookup silently misses and the package skips verification,
			// caching, and P2P. Fall back to the raw slice if decoding fails.
			if decoded, err := url.PathUnescape(p); err == nil {
				return decoded
			}
			return p
		}
	}
	return ""
}

// isAllowedIndexURL validates that a URL is a legitimate Debian/Ubuntu repository
// This prevents SSRF attacks by blocking requests to internal services
func isAllowedIndexURL(url string) bool {
	return security.IsAllowedMirrorURL(url)
}
