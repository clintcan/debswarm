// Package index handles parsing and caching of Debian Packages indices
package index

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ulikunitz/xz"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/httpclient"
	"github.com/debswarm/debswarm/internal/sanitize"
	"github.com/debswarm/debswarm/internal/security"
)

// PackageInfo holds information about a single package
type PackageInfo struct {
	Package      string
	Version      string
	Architecture string
	Filename     string
	Size         int64
	SHA256       string
	SHA512       string
	Description  string
	Repo         string // Repository base URL this package belongs to
}

// Index manages package index files from multiple repositories
type Index struct {
	cachePath  string
	packages   map[string]*PackageInfo            // keyed by SHA256
	byRepo     map[string]map[string]*PackageInfo // repo → path → pkg
	byBasename map[string][]*PackageInfo          // basename → packages (for O(1) lookup)
	mu         sync.RWMutex
	logger     *zap.Logger
	client     *http.Client
}

// New creates a new Index manager
func New(cachePath string, logger *zap.Logger) *Index {
	return &Index{
		cachePath:  cachePath,
		packages:   make(map[string]*PackageInfo),
		byRepo:     make(map[string]map[string]*PackageInfo),
		byBasename: make(map[string][]*PackageInfo),
		logger:     logger,
		client:     httpclient.Default(),
	}
}

// LoadFromFile loads and parses a Packages file
func (idx *Index) LoadFromFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var reader io.Reader = f

	// Handle compression
	if strings.HasSuffix(path, ".gz") {
		gzReader, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer func() { _ = gzReader.Close() }()
		reader = gzReader
	} else if strings.HasSuffix(path, ".xz") {
		xzReader, err := xz.NewReader(f)
		if err != nil {
			return fmt.Errorf("failed to create xz reader: %w", err)
		}
		reader = xzReader
	}

	// Use filename as repo identifier for local files
	return idx.parseForRepo(reader, path)
}

// LoadFromFileWithRepo loads and parses a Packages file with an explicit repo identifier.
// This is useful for APT list files where the repo should be extracted from the filename.
func (idx *Index) LoadFromFileWithRepo(path, repo string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var reader io.Reader = f

	// Handle compression based on extension
	if strings.HasSuffix(path, ".gz") {
		gzReader, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer func() { _ = gzReader.Close() }()
		reader = gzReader
	} else if strings.HasSuffix(path, ".xz") {
		xzReader, err := xz.NewReader(f)
		if err != nil {
			return fmt.Errorf("failed to create xz reader: %w", err)
		}
		reader = xzReader
	}

	return idx.parseForRepo(reader, repo)
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

	var reader io.Reader = resp.Body

	// Handle compression based on URL
	if strings.HasSuffix(url, ".gz") {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer func() { _ = gzReader.Close() }()
		reader = gzReader
	} else if strings.HasSuffix(url, ".xz") {
		xzReader, err := xz.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to create xz reader: %w", err)
		}
		reader = xzReader
	}

	// Extract repo base URL
	repo := ExtractRepoFromURL(url)
	return idx.parseForRepo(reader, repo)
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
		reader = gzReader
	} else if len(data) >= 6 && data[0] == 0xfd && data[1] == '7' && data[2] == 'z' && data[3] == 'X' && data[4] == 'Z' && data[5] == 0x00 {
		xzReader, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("failed to create xz reader: %w", err)
		}
		reader = xzReader
	}
	// Otherwise assume uncompressed

	repo := ExtractRepoFromURL(url)
	return idx.parseForRepo(reader, repo)
}

// parseForRepo parses an uncompressed Packages file for a specific repository
func (idx *Index) parseForRepo(reader io.Reader, repo string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Ensure repo map exists
	if idx.byRepo[repo] == nil {
		idx.byRepo[repo] = make(map[string]*PackageInfo)
	}

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
				if pkg.Filename != "" {
					idx.byRepo[repo][pkg.Filename] = pkg
					// Add to basename index for O(1) lookups
					basename := filepath.Base(pkg.Filename)
					idx.byBasename[basename] = append(idx.byBasename[basename], pkg)
				}
				count++
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
		case "SHA512":
			pkg.SHA512 = value
		case "Description":
			pkg.Description = value
		}
	}

	// Handle last package
	if pkg != nil && pkg.SHA256 != "" {
		pkg.Repo = repo
		idx.packages[pkg.SHA256] = pkg
		if pkg.Filename != "" {
			idx.byRepo[repo][pkg.Filename] = pkg
			// Add to basename index for O(1) lookups
			basename := filepath.Base(pkg.Filename)
			idx.byBasename[basename] = append(idx.byBasename[basename], pkg)
		}
		count++
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
func (idx *Index) GetByURLPath(url string) *PackageInfo {
	repo := ExtractRepoFromURL(url)
	path := ExtractPathFromURL(url)

	if repo == "" || path == "" {
		return nil
	}

	// Try specific repo first
	if pkg := idx.GetByRepoAndPath(repo, path); pkg != nil {
		return pkg
	}

	// Fall back to any repo with this path
	return idx.GetByPath(path)
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
}

// ClearRepo removes packages from a specific repository
func (idx *Index) ClearRepo(repo string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Remove packages from this repo from the global packages map and basename index
	if repoMap := idx.byRepo[repo]; repoMap != nil {
		for _, pkg := range repoMap {
			delete(idx.packages, pkg.SHA256)
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
func ExtractPathFromURL(url string) string {
	// Find the pool/ or dists/ part
	for _, marker := range []string{"/pool/", "/dists/"} {
		if idx := strings.Index(url, marker); idx != -1 {
			return url[idx+1:] // Include the leading part after /
		}
	}
	return ""
}

// isAllowedIndexURL validates that a URL is a legitimate Debian/Ubuntu repository
// This prevents SSRF attacks by blocking requests to internal services
func isAllowedIndexURL(url string) bool {
	return security.IsAllowedMirrorURL(url)
}
