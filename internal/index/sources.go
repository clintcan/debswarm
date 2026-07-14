package index

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz"
	"go.uber.org/zap"
)

// decompressByMagic wraps data with the decompressor matching its leading magic
// bytes (for by-hash URLs that lack a file extension), applying the
// decompression-bomb size limit. Mirrors the detection in LoadFromData. Like
// that path it has no bz2 case — bz2 is only detected by extension
// (decompressByName), which is fine because by-hash Sources blobs are gz/xz.
func decompressByMagic(data []byte) (io.Reader, error) {
	switch {
	case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		return io.LimitReader(gz, maxDecompressedBytes), nil
	case len(data) >= 6 && data[0] == 0xfd && data[1] == '7' && data[2] == 'z' && data[3] == 'X' && data[4] == 'Z' && data[5] == 0x00:
		xzr, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("failed to create xz reader: %w", err)
		}
		return io.LimitReader(xzr, maxDecompressedBytes), nil
	case len(data) >= 4 && data[0] == 0x28 && data[1] == 0xb5 && data[2] == 0x2f && data[3] == 0xfd:
		zr, err := zstd.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("failed to create zstd reader: %w", err)
		}
		return io.LimitReader(zr.IOReadCloser(), maxDecompressedBytes), nil
	case len(data) >= 4 && data[0] == 0x04 && data[1] == 0x22 && data[2] == 0x4d && data[3] == 0x18:
		return io.LimitReader(lz4.NewReader(bytes.NewReader(data)), maxDecompressedBytes), nil
	default:
		return bytes.NewReader(data), nil
	}
}

// LoadSourcesFromData parses a Debian Sources index for a specific repository,
// recording every source artifact (.dsc / .orig.tar.* / .debian.tar.* / .diff.gz
// / native tarball) listed in its Checksums-Sha256 fields so those artifacts can
// be cached, verified against their SHA256, and P2P-shared exactly like binary
// .debs. url derives the repo base and the index-file key. Compression handling
// matches LoadFromData (gzip/xz/zstd/lz4 by magic bytes).
func (idx *Index) LoadSourcesFromData(data []byte, url string) error {
	reader, err := decompressByMagic(data)
	if err != nil {
		return err
	}
	return idx.parseSourcesForRepo(reader, ExtractRepoFromURL(url), indexFileKey(url))
}

// LoadSourcesFromFileWithRepo loads and parses a Sources file with an explicit
// repo identifier. Used by the apt-lists watcher for deb-src indices.
func (idx *Index) LoadSourcesFromFileWithRepo(path, repo string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	reader, err := decompressByName(f, path)
	if err != nil {
		return err
	}
	return idx.parseSourcesForRepo(reader, repo, indexFileKey(path))
}

// isHex64 reports whether s is a 64-character hex string (a SHA256 digest).
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// parseSourcesForRepo parses an uncompressed Debian Sources file. Each stanza has
// a Directory: field and a multi-line Checksums-Sha256: block whose indented
// lines are "<sha256> <size> <filename>" for every artifact of that source
// package. Each artifact becomes one PackageInfo keyed by "<Directory>/<filename>"
// — the same pool path GetByURLPath derives from a request URL — so source
// artifacts resolve through the identical cache/verify/P2P path as binary
// packages. fileKey identifies this index file for re-parse eviction; the Sources
// index lives under …/source/ (distinct from its sibling …/binary-<arch>/
// Packages index), so its generation never collides with binary entries.
func (idx *Index) parseSourcesForRepo(reader io.Reader, repo, fileKey string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Drop the previous generation of this index file's entries, then rebuild.
	idx.clearIndexFileLocked(fileKey)

	if idx.byRepo[repo] == nil {
		idx.byRepo[repo] = make(map[string]*PackageInfo)
	}

	var generation []*PackageInfo
	count := 0

	// Per-stanza accumulators.
	var srcName, directory string
	type fileEntry struct {
		sha256 string
		size   int64
		name   string
	}
	var files []fileEntry
	inChecksums := false

	// emit flushes the current stanza's artifacts into the index maps and resets
	// the accumulators. It returns false if the per-repo limit was reached, in
	// which case the caller stops parsing.
	emit := func() bool {
		hitLimit := false
		if directory != "" {
			for _, fe := range files {
				if fe.sha256 == "" || fe.name == "" {
					continue
				}
				filename := directory + "/" + fe.name
				pkg := &PackageInfo{
					Package:  srcName,
					Filename: filename,
					Size:     fe.size,
					SHA256:   fe.sha256,
					Repo:     repo,
				}
				idx.packages[fe.sha256] = pkg
				generation = append(generation, pkg)
				idx.byRepo[repo][filename] = pkg
				basename := filepath.Base(filename)
				idx.byBasename[basename] = append(idx.byBasename[basename], pkg)
				count++
				if count >= maxPackagesPerRepo {
					idx.logger.Warn("Source index limit reached, truncating",
						zap.String("repo", repo),
						zap.Int("limit", maxPackagesPerRepo))
					hitLimit = true
					break
				}
			}
		}
		srcName, directory = "", ""
		files = files[:0]
		inChecksums = false
		return !hitLimit
	}

	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	truncated := false
	for scanner.Scan() {
		line := scanner.Text()

		// Blank line marks end of a source stanza.
		if line == "" {
			if !emit() {
				truncated = true
				break
			}
			continue
		}

		// Inside a Checksums-Sha256 block, indented lines are file entries.
		if inChecksums && (line[0] == ' ' || line[0] == '\t') {
			f := strings.Fields(line)
			if len(f) >= 3 && isHex64(f[0]) {
				size, _ := strconv.ParseInt(f[1], 10, 64)
				files = append(files, fileEntry{sha256: strings.ToLower(f[0]), size: size, name: f[2]})
			}
			continue
		}
		// Any non-indented line closes an open checksum block (this also makes the
		// Files:/Checksums-Sha1:/Checksums-Sha512: blocks be ignored).
		inChecksums = false

		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue // continuation line of a field we don't collect
		}
		field := line[:colonIdx]
		value := strings.TrimSpace(line[colonIdx+1:])

		switch field {
		case "Package":
			srcName = value
		case "Directory":
			directory = strings.TrimRight(value, "/")
		case "Checksums-Sha256":
			inChecksums = true
		}
	}

	// Emit the final stanza (Sources files need not end with a blank line),
	// unless we already stopped on the per-repo limit.
	if !truncated {
		emit()
	}

	if len(generation) > 0 {
		idx.byIndexFile[fileKey] = generation
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	idx.logger.Debug("Parsed source index",
		zap.String("repo", repo),
		zap.Int("artifacts", count),
		zap.Int("totalRepos", len(idx.byRepo)))
	return nil
}
