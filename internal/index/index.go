// Package index handles parsing and caching of Debian Packages indices
package index

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ulikunitz/xz"
	"go.uber.org/zap"
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
}

// Index manages package index files
type Index struct {
	cachePath string
	packages  map[string]*PackageInfo // keyed by SHA256
	byPath    map[string]*PackageInfo // keyed by Filename
	mu        sync.RWMutex
	logger    *zap.Logger
}

// New creates a new Index manager
func New(cachePath string, logger *zap.Logger) *Index {
	return &Index{
		cachePath: cachePath,
		packages:  make(map[string]*PackageInfo),
		byPath:    make(map[string]*PackageInfo),
		logger:    logger,
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
		defer gzReader.Close()
		reader = gzReader
	} else if strings.HasSuffix(path, ".xz") {
		xzReader, err := xz.NewReader(f)
		if err != nil {
			return fmt.Errorf("failed to create xz reader: %w", err)
		}
		reader = xzReader
	}

	return idx.parse(reader)
}

// LoadFromURL downloads and parses a Packages file from a URL
func (idx *Index) LoadFromURL(url string) error {
	idx.logger.Debug("Fetching package index", zap.String("url", url))

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	var reader io.Reader = resp.Body

	// Handle compression based on URL
	if strings.HasSuffix(url, ".gz") {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	} else if strings.HasSuffix(url, ".xz") {
		xzReader, err := xz.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to create xz reader: %w", err)
		}
		reader = xzReader
	}

	return idx.parse(reader)
}

// parse parses an uncompressed Packages file
func (idx *Index) parse(reader io.Reader) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

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
				idx.packages[pkg.SHA256] = pkg
				if pkg.Filename != "" {
					idx.byPath[pkg.Filename] = pkg
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
			pkg.Size, _ = strconv.ParseInt(value, 10, 64)
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
		idx.packages[pkg.SHA256] = pkg
		if pkg.Filename != "" {
			idx.byPath[pkg.Filename] = pkg
		}
		count++
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	idx.logger.Debug("Parsed package index", zap.Int("packages", count))
	return nil
}

// GetBySHA256 returns package info by SHA256 hash
func (idx *Index) GetBySHA256(sha256 string) *PackageInfo {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.packages[sha256]
}

// GetByPath returns package info by filename/path
func (idx *Index) GetByPath(path string) *PackageInfo {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// Try exact match first
	if pkg := idx.byPath[path]; pkg != nil {
		return pkg
	}

	// Try matching just the filename part
	base := filepath.Base(path)
	for filename, pkg := range idx.byPath {
		if filepath.Base(filename) == base {
			return pkg
		}
	}

	return nil
}

// GetByPathSuffix returns package info by path suffix (for URL matching)
func (idx *Index) GetByPathSuffix(suffix string) *PackageInfo {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	for filename, pkg := range idx.byPath {
		if strings.HasSuffix(filename, suffix) || strings.HasSuffix(suffix, filename) {
			return pkg
		}
	}
	return nil
}

// Count returns the number of indexed packages
func (idx *Index) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.packages)
}

// Clear removes all indexed packages
func (idx *Index) Clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.packages = make(map[string]*PackageInfo)
	idx.byPath = make(map[string]*PackageInfo)
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
