package index

import (
	"bytes"
	"compress/gzip"
	"testing"

	"go.uber.org/zap"
)

// sampleSourcesContent is a realistic Debian Sources index covering the shapes
// the parser must handle:
//   - hello: orig + debian (the common non-native source layout)
//   - dpkg: a native tarball (no separate orig/debian)
//   - libfoo: an .orig-<component> additional-tarball entry
//
// Each stanza carries Files: (md5) and Checksums-Sha1: (sha1) blocks that MUST be
// ignored — only the Checksums-Sha256 entries become index artifacts.
const sampleSourcesContent = `Package: hello
Binary: hello
Version: 2.10-3
Directory: pool/main/h/hello
Files:
 1111111111111111111111111111aaaa 1183 hello_2.10-3.dsc
 2222222222222222222222222222bbbb 725946 hello_2.10.orig.tar.gz
 3333333333333333333333333333cccc 12345 hello_2.10-3.debian.tar.xz
Checksums-Sha1:
 1111111111111111111111111111111111111aaa 1183 hello_2.10-3.dsc
 2222222222222222222222222222222222222bbb 725946 hello_2.10.orig.tar.gz
 3333333333333333333333333333333333333ccc 12345 hello_2.10-3.debian.tar.xz
Checksums-Sha256:
 1111111111111111111111111111111111111111111111111111111111111111 1183 hello_2.10-3.dsc
 2222222222222222222222222222222222222222222222222222222222222222 725946 hello_2.10.orig.tar.gz
 3333333333333333333333333333333333333333333333333333333333333333 12345 hello_2.10-3.debian.tar.xz

Package: dpkg
Binary: dpkg
Version: 1.21.22
Directory: pool/main/d/dpkg
Files:
 4444444444444444444444444444dddd 2500 dpkg_1.21.22.dsc
 5555555555555555555555555555eeee 4800000 dpkg_1.21.22.tar.xz
Checksums-Sha256:
 4444444444444444444444444444444444444444444444444444444444444444 2500 dpkg_1.21.22.dsc
 5555555555555555555555555555555555555555555555555555555555555555 4800000 dpkg_1.21.22.tar.xz

Package: libfoo
Binary: libfoo-dev
Version: 3.0-1
Directory: pool/main/libf/libfoo
Checksums-Sha256:
 6666666666666666666666666666666666666666666666666666666666666666 1500 libfoo_3.0-1.dsc
 7777777777777777777777777777777777777777777777777777777777777777 900000 libfoo_3.0.orig.tar.gz
 8888888888888888888888888888888888888888888888888888888888888888 40000 libfoo_3.0.orig-docs.tar.gz
`

const (
	shaHelloDsc    = "1111111111111111111111111111111111111111111111111111111111111111"
	shaHelloOrig   = "2222222222222222222222222222222222222222222222222222222222222222"
	shaHelloDebian = "3333333333333333333333333333333333333333333333333333333333333333"
	shaDpkgDsc     = "4444444444444444444444444444444444444444444444444444444444444444"
	shaDpkgNative  = "5555555555555555555555555555555555555555555555555555555555555555"
	shaFooDsc      = "6666666666666666666666666666666666666666666666666666666666666666"
	shaFooOrig     = "7777777777777777777777777777777777777777777777777777777777777777"
	shaFooOrigDocs = "8888888888888888888888888888888888888888888888888888888888888888"
)

const sourcesIndexURL = "http://deb.debian.org/debian/dists/bookworm/main/source/Sources"

func TestLoadSourcesFromData(t *testing.T) {
	idx := New("/tmp/test", testLogger())

	if err := idx.LoadSourcesFromData([]byte(sampleSourcesContent), sourcesIndexURL); err != nil {
		t.Fatalf("LoadSourcesFromData failed: %v", err)
	}

	// 3 (hello) + 2 (dpkg) + 3 (libfoo) = 8 artifacts.
	if idx.Count() != 8 {
		t.Errorf("Expected 8 source artifacts, got %d", idx.Count())
	}
	if idx.RepoCount() != 1 {
		t.Errorf("Expected 1 repo, got %d", idx.RepoCount())
	}

	// The Files: (md5) and Checksums-Sha1 blocks must not have produced entries.
	if idx.GetBySHA256("1111111111111111111111111111111111111aaa") != nil {
		t.Error("a sha1 checksum leaked into the index — only Checksums-Sha256 must be indexed")
	}
}

func TestLoadSourcesFromDataGzip(t *testing.T) {
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	if _, err := gzWriter.Write([]byte(sampleSourcesContent)); err != nil {
		t.Fatalf("Failed to write gzip: %v", err)
	}
	gzWriter.Close()

	idx := New("/tmp/test", testLogger())
	if err := idx.LoadSourcesFromData(buf.Bytes(), sourcesIndexURL+".gz"); err != nil {
		t.Fatalf("LoadSourcesFromData with gzip failed: %v", err)
	}
	if idx.Count() != 8 {
		t.Errorf("Expected 8 artifacts, got %d", idx.Count())
	}
}

// The by-hash form has no extension; compression is detected by magic bytes.
func TestLoadSourcesFromData_ByHashGzip(t *testing.T) {
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	if _, err := gzWriter.Write([]byte(sampleSourcesContent)); err != nil {
		t.Fatalf("Failed to write gzip: %v", err)
	}
	gzWriter.Close()

	idx := New("/tmp/test", testLogger())
	byHash := "http://deb.debian.org/debian/dists/bookworm/main/source/by-hash/SHA256/deadbeef"
	if err := idx.LoadSourcesFromData(buf.Bytes(), byHash); err != nil {
		t.Fatalf("LoadSourcesFromData by-hash gzip failed: %v", err)
	}
	if idx.Count() != 8 {
		t.Errorf("Expected 8 artifacts, got %d", idx.Count())
	}
}

// GetByURLPath must resolve each source artifact from its request URL to the
// SHA256 the Sources index listed — the join key is Directory + "/" + filename,
// exactly what ExtractPathFromURL derives by slicing the URL at /pool/.
func TestSources_GetByURLPath(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	if err := idx.LoadSourcesFromData([]byte(sampleSourcesContent), sourcesIndexURL); err != nil {
		t.Fatalf("LoadSourcesFromData: %v", err)
	}

	cases := []struct {
		name     string
		url      string
		wantSHA  string
		wantSize int64
		wantPkg  string
	}{
		{"dsc", "http://deb.debian.org/debian/pool/main/h/hello/hello_2.10-3.dsc", shaHelloDsc, 1183, "hello"},
		{"orig", "http://deb.debian.org/debian/pool/main/h/hello/hello_2.10.orig.tar.gz", shaHelloOrig, 725946, "hello"},
		{"debian", "http://deb.debian.org/debian/pool/main/h/hello/hello_2.10-3.debian.tar.xz", shaHelloDebian, 12345, "hello"},
		{"native", "http://deb.debian.org/debian/pool/main/d/dpkg/dpkg_1.21.22.tar.xz", shaDpkgNative, 4800000, "dpkg"},
		{"orig-component", "http://deb.debian.org/debian/pool/main/libf/libfoo/libfoo_3.0.orig-docs.tar.gz", shaFooOrigDocs, 40000, "libfoo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pkg := idx.GetByURLPath(tc.url)
			if pkg == nil {
				t.Fatalf("GetByURLPath returned nil for %s (hash unreachable → uncached/unverified/no P2P)", tc.url)
			}
			if pkg.SHA256 != tc.wantSHA {
				t.Errorf("SHA256 = %q, want %q", pkg.SHA256, tc.wantSHA)
			}
			if pkg.Size != tc.wantSize {
				t.Errorf("Size = %d, want %d", pkg.Size, tc.wantSize)
			}
			if pkg.Package != tc.wantPkg {
				t.Errorf("Package = %q, want %q", pkg.Package, tc.wantPkg)
			}
		})
	}
}

// A stanza with no Directory: must contribute nothing (the join key would be
// meaningless) rather than emitting bare-filename entries.
func TestLoadSourcesFromData_NoDirectorySkipped(t *testing.T) {
	const noDir = `Package: orphan
Checksums-Sha256:
 9999999999999999999999999999999999999999999999999999999999999999 100 orphan_1.0.dsc

`
	idx := New("/tmp/test", testLogger())
	if err := idx.LoadSourcesFromData([]byte(noDir), sourcesIndexURL); err != nil {
		t.Fatalf("LoadSourcesFromData: %v", err)
	}
	if idx.Count() != 0 {
		t.Errorf("Expected 0 artifacts for a Directory-less stanza, got %d", idx.Count())
	}
}

// Re-parsing the same Sources index must replace the previous generation, not
// accumulate it (mirrors TestReparse_DoesNotLeakOldGenerations for Packages).
func TestReparseSources_DoesNotLeak(t *testing.T) {
	idx := New(t.TempDir(), zap.NewNop())

	for i := range 5 {
		if err := idx.LoadSourcesFromData([]byte(sampleSourcesContent), sourcesIndexURL); err != nil {
			t.Fatalf("LoadSourcesFromData (round %d): %v", i, err)
		}
	}

	if idx.Count() != 8 {
		t.Errorf("Count after 5 re-parses = %d, want 8 (old generations must be dropped)", idx.Count())
	}
	idx.mu.RLock()
	entries := len(idx.byBasename["hello_2.10-3.dsc"])
	idx.mu.RUnlock()
	if entries != 1 {
		t.Errorf("byBasename entries after 5 re-parses = %d, want 1", entries)
	}
}

// A Sources index and its sibling Packages index share one repo key but distinct
// index-file keys, so parsing one must never evict the other's entries.
func TestSources_SiblingPackagesSurvive(t *testing.T) {
	idx := New(t.TempDir(), zap.NewNop())
	packagesURL := "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages"
	binData := packagesEntry("hello", "pool/main/h/hello/hello_2.10-3_amd64.deb", reparseHashA)

	if err := idx.LoadFromData([]byte(binData), packagesURL); err != nil {
		t.Fatalf("LoadFromData (binary): %v", err)
	}
	if err := idx.LoadSourcesFromData([]byte(sampleSourcesContent), sourcesIndexURL); err != nil {
		t.Fatalf("LoadSourcesFromData: %v", err)
	}

	if idx.GetBySHA256(reparseHashA) == nil {
		t.Fatal("binary package was wiped when the sibling Sources index parsed")
	}
	if idx.GetBySHA256(shaHelloOrig) == nil {
		t.Fatal("source artifact missing after parse")
	}

	// Re-parsing the Sources index must not touch the binary entry.
	if err := idx.LoadSourcesFromData([]byte(sampleSourcesContent), sourcesIndexURL); err != nil {
		t.Fatalf("LoadSourcesFromData re-parse: %v", err)
	}
	if idx.GetBySHA256(reparseHashA) == nil {
		t.Fatal("binary package lost on Sources re-parse")
	}
}

// by-hash Sources URLs, whose digest changes every update, must collapse to one
// generation key so re-parses replace instead of leak.
func TestReparseSources_ByHashSharesGeneration(t *testing.T) {
	idx := New(t.TempDir(), zap.NewNop())
	v1 := "http://deb.debian.org/debian/dists/stable/main/source/by-hash/SHA256/aaaa1111"
	v2 := "http://deb.debian.org/debian/dists/stable/main/source/by-hash/SHA256/bbbb2222"

	oldGen := "Package: hashsrc\nDirectory: pool/main/h/hashsrc\nChecksums-Sha256:\n " +
		reparseHashA + " 100 hashsrc_1.0.dsc\n\n"
	newGen := "Package: hashsrc\nDirectory: pool/main/h/hashsrc\nChecksums-Sha256:\n " +
		reparseHashB + " 100 hashsrc_2.0.dsc\n\n"

	if err := idx.LoadSourcesFromData([]byte(oldGen), v1); err != nil {
		t.Fatalf("LoadSourcesFromData v1: %v", err)
	}
	if err := idx.LoadSourcesFromData([]byte(newGen), v2); err != nil {
		t.Fatalf("LoadSourcesFromData v2: %v", err)
	}

	if idx.GetBySHA256(reparseHashA) != nil {
		t.Error("old by-hash generation still resolvable — the leak is back for by-hash Sources")
	}
	if idx.GetBySHA256(reparseHashB) == nil {
		t.Error("new by-hash generation missing")
	}
}
