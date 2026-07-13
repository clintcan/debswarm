package release

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const sampleRelease = `Origin: Debian
Label: Debian
Suite: stable
Version: 12.5
Codename: bookworm
Date: Sat, 10 Feb 2024 11:11:11 UTC
Valid-Until: Sat, 17 Feb 2024 11:11:11 UTC
Acquire-By-Hash: yes
Architectures: amd64 arm64
Components: main contrib
MD5Sum:
 d41d8cd98f00b204e9800998ecf8427e 1234 main/binary-amd64/Packages
SHA256:
 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 8000 main/binary-amd64/Packages
 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 3000 main/binary-amd64/Packages.gz
 cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc 500 main/source/Sources
`

func TestParse_FieldsAndHashes(t *testing.T) {
	r, err := Parse([]byte(sampleRelease))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Origin != "Debian" || r.Suite != "stable" || r.Codename != "bookworm" {
		t.Fatalf("fields: origin=%q suite=%q codename=%q", r.Origin, r.Suite, r.Codename)
	}
	if !r.AcquireByHash {
		t.Fatal("Acquire-By-Hash should be true")
	}
	if len(r.SHA256) != 3 {
		t.Fatalf("SHA256 entries = %d, want 3", len(r.SHA256))
	}
	pkgs, ok := r.SHA256["main/binary-amd64/Packages"]
	if !ok {
		t.Fatal("missing entry for main/binary-amd64/Packages")
	}
	if pkgs.Size != 8000 || pkgs.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("Packages entry wrong: %+v", pkgs)
	}
	if gz := r.SHA256["main/binary-amd64/Packages.gz"]; gz.Size != 3000 {
		t.Fatalf("Packages.gz size = %d, want 3000", gz.Size)
	}
	// MD5Sum entries must NOT leak into the SHA256 map.
	for path, fh := range r.SHA256 {
		if len(fh.SHA256) != 64 {
			t.Fatalf("non-sha256 hash leaked for %q: %q", path, fh.SHA256)
		}
	}
}

func TestParse_ValidUntil(t *testing.T) {
	r, err := Parse([]byte(sampleRelease))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := time.Date(2024, time.February, 17, 11, 11, 11, 0, time.UTC)
	if !r.ValidUntil.Equal(want) {
		t.Fatalf("ValidUntil = %v, want %v", r.ValidUntil, want)
	}
}

func TestParse_NumericOffsetValidUntil(t *testing.T) {
	body := "Suite: x\nValid-Until: Sat, 17 Feb 2024 11:11:11 +0000\nSHA256:\n " +
		strings.Repeat("a", 64) + " 1 p\n"
	r, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.ValidUntil.IsZero() {
		t.Fatal("numeric-offset Valid-Until not parsed")
	}
}

func TestParse_NoSHA256Section(t *testing.T) {
	body := "Origin: Debian\nSuite: stable\nMD5Sum:\n d41d8cd98f00b204e9800998ecf8427e 1 p\n"
	if _, err := Parse([]byte(body)); !errors.Is(err, ErrNoSHA256) {
		t.Fatalf("want ErrNoSHA256, got %v", err)
	}
}

func TestParse_MalformedHashLineSkipped(t *testing.T) {
	// A short (2-field) line is skipped; the valid entry still parses.
	body := "Suite: x\nSHA256:\n deadbeef 100\n " + strings.Repeat("a", 64) + " 200 main/binary-amd64/Packages\n"
	r, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(r.SHA256) != 1 {
		t.Fatalf("SHA256 entries = %d, want 1 (malformed line skipped)", len(r.SHA256))
	}
}
