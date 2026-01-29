package index

import (
	"bytes"
	"testing"

	"go.uber.org/zap"
)

func FuzzParsePackagesFile(f *testing.F) {
	// Seed corpus with valid Packages file entries
	f.Add([]byte(`Package: curl
Version: 7.88.1-10+deb12u5
Architecture: amd64
Filename: pool/main/c/curl/curl_7.88.1-10+deb12u5_amd64.deb
Size: 123456
SHA256: abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890
Description: command line tool for transferring data with URL syntax

`))

	f.Add([]byte(`Package: libssl3
Version: 3.0.11-1~deb12u2
Architecture: amd64
Filename: pool/main/o/openssl/libssl3_3.0.11-1~deb12u2_amd64.deb
Size: 2000000
SHA256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
Description: Secure Sockets Layer toolkit

Package: openssl
Version: 3.0.11-1~deb12u2
Architecture: amd64
Filename: pool/main/o/openssl/openssl_3.0.11-1~deb12u2_amd64.deb
Size: 1500000
SHA256: fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210
Description: Secure Sockets Layer toolkit - cryptographic utility

`))

	// Edge cases
	f.Add([]byte(""))
	f.Add([]byte("\n"))
	f.Add([]byte("\n\n\n"))
	f.Add([]byte("Package: test\n"))
	f.Add([]byte("Invalid line without colon\n"))
	f.Add([]byte("::::\n"))
	f.Add([]byte("Package:\n"))
	f.Add([]byte("Size: not-a-number\n"))
	f.Add([]byte("Size: -1\n"))
	f.Add([]byte("Size: 999999999999999999999999999999\n"))

	// Malformed entries
	f.Add([]byte("Package: test\nSHA256: hash\n\nPackage: "))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Create index with nop logger
		idx := New("", zap.NewNop())

		// Parse the data - should not panic
		reader := bytes.NewReader(data)
		_ = idx.parseForRepo(reader, "fuzz-test")

		// Verify internal state is consistent
		idx.mu.RLock()
		defer idx.mu.RUnlock()

		// All packages in the hash map should have SHA256 keys
		for hash, pkg := range idx.packages {
			if hash == "" {
				t.Error("empty hash key in packages map")
			}
			if pkg == nil {
				t.Error("nil package in packages map")
			}
			if pkg != nil && pkg.SHA256 != hash {
				t.Errorf("SHA256 mismatch: key=%s pkg.SHA256=%s", hash, pkg.SHA256)
			}
		}

		// All packages in byBasename should exist in packages map
		for _, pkgs := range idx.byBasename {
			for _, pkg := range pkgs {
				if pkg == nil {
					t.Error("nil package in byBasename")
				}
				if pkg != nil && pkg.SHA256 != "" {
					if _, exists := idx.packages[pkg.SHA256]; !exists {
						t.Error("package in byBasename but not in packages map")
					}
				}
			}
		}
	})
}

func FuzzExtractRepoFromURL(f *testing.F) {
	// Seed corpus with typical URLs
	f.Add("http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.xz")
	f.Add("http://archive.ubuntu.com/ubuntu/dists/jammy/main/binary-amd64/Packages.gz")
	f.Add("http://security.debian.org/debian-security/dists/bookworm-security/main/binary-amd64/Packages")
	f.Add("https://example.com/repo/Packages")

	// Edge cases
	f.Add("")
	f.Add("not-a-url")
	f.Add("http://")
	f.Add("http://host")
	f.Add("http://host/")
	f.Add("://missing-scheme")

	f.Fuzz(func(t *testing.T, url string) {
		// Should not panic
		result := ExtractRepoFromURL(url)

		// Result should never be longer than input
		if len(result) > len(url) {
			t.Errorf("result longer than input: %d > %d", len(result), len(url))
		}
	})
}

func FuzzExtractPathFromURL(f *testing.F) {
	// Seed corpus
	f.Add("http://deb.debian.org/debian/pool/main/c/curl/curl_7.88.1_amd64.deb")
	f.Add("http://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello_2.10-2_amd64.deb")

	// Edge cases
	f.Add("")
	f.Add("/")
	f.Add("http://")
	f.Add("no-slash")

	f.Fuzz(func(t *testing.T, url string) {
		// Should not panic
		_ = ExtractPathFromURL(url)
	})
}
