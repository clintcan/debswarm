package index

import (
	"bytes"
	"testing"

	"go.uber.org/zap"
)

func FuzzParseSourcesFile(f *testing.F) {
	// Valid stanza with orig + debian tarballs.
	f.Add([]byte(`Package: hello
Directory: pool/main/h/hello
Checksums-Sha256:
 1111111111111111111111111111111111111111111111111111111111111111 1183 hello_2.10-3.dsc
 2222222222222222222222222222222222222222222222222222222222222222 725946 hello_2.10.orig.tar.gz
 3333333333333333333333333333333333333333333333333333333333333333 12345 hello_2.10-3.debian.tar.xz

`))

	// Two stanzas, one native tarball.
	f.Add([]byte(`Package: dpkg
Directory: pool/main/d/dpkg
Checksums-Sha256:
 4444444444444444444444444444444444444444444444444444444444444444 2500 dpkg_1.21.22.dsc
 5555555555555555555555555555555555555555555555555555555555555555 4800000 dpkg_1.21.22.tar.xz

Package: curl
Directory: pool/main/c/curl
Files:
 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 100 curl_1.0.dsc
Checksums-Sha1:
 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 100 curl_1.0.dsc
Checksums-Sha256:
 6666666666666666666666666666666666666666666666666666666666666666 100 curl_1.0.dsc

`))

	// Edge cases.
	f.Add([]byte(""))
	f.Add([]byte("\n"))
	f.Add([]byte("\n\n\n"))
	f.Add([]byte("Package: test\n"))
	f.Add([]byte("Directory: pool/main/t/test\n"))
	f.Add([]byte("Checksums-Sha256:\n")) // header, no entries
	f.Add([]byte("Checksums-Sha256:\n 1111 100 x.dsc\n"))
	f.Add([]byte("Invalid line without colon\n"))
	f.Add([]byte("::::\n"))
	// Indented checksum line with too few fields.
	f.Add([]byte("Directory: p\nChecksums-Sha256:\n abc 100\n"))
	// Non-hex first field (must be skipped, not indexed).
	f.Add([]byte("Directory: p\nChecksums-Sha256:\n zzzz 100 x.dsc\n"))
	// Giant / negative size.
	f.Add([]byte("Directory: p\nChecksums-Sha256:\n 1111111111111111111111111111111111111111111111111111111111111111 999999999999999999999999999999 x.dsc\n\n"))
	f.Add([]byte("Directory: p\nChecksums-Sha256:\n 1111111111111111111111111111111111111111111111111111111111111111 -1 x.dsc\n\n"))
	// Truncated mid-block (no trailing blank line).
	f.Add([]byte("Package: t\nDirectory: p\nChecksums-Sha256:\n 1111111111111111111111111111111111111111111111111111111111111111 100 t.dsc"))
	// Checksums with no Directory (must emit nothing).
	f.Add([]byte("Checksums-Sha256:\n 1111111111111111111111111111111111111111111111111111111111111111 100 x.dsc\n\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		idx := New("", zap.NewNop())

		reader := bytes.NewReader(data)
		_ = idx.parseSourcesForRepo(reader, "fuzz-test", "fuzz-test")

		idx.mu.RLock()
		defer idx.mu.RUnlock()

		// Every packages-map key must be its package's SHA256.
		for hash, pkg := range idx.packages {
			if hash == "" {
				t.Error("empty hash key in packages map")
			}
			if pkg == nil {
				t.Error("nil package in packages map")
				continue
			}
			if pkg.SHA256 != hash {
				t.Errorf("SHA256 mismatch: key=%s pkg.SHA256=%s", hash, pkg.SHA256)
			}
			// The join key must be Directory + "/" + filename, so it never has an
			// empty filename component.
			if pkg.Filename == "" {
				t.Error("indexed source artifact with empty Filename")
			}
		}

		// byBasename entries must exist in the packages map.
		for _, pkgs := range idx.byBasename {
			for _, pkg := range pkgs {
				if pkg == nil {
					t.Error("nil package in byBasename")
					continue
				}
				if pkg.SHA256 != "" {
					if _, exists := idx.packages[pkg.SHA256]; !exists {
						t.Error("package in byBasename but not in packages map")
					}
				}
			}
		}
	})
}
