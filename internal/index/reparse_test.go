package index

import (
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"
)

const reparseHashA = "aaaa567890123456789012345678901234567890123456789012345678901234"
const reparseHashB = "bbbb567890123456789012345678901234567890123456789012345678901234"

func packagesEntry(name, filename, hash string) string {
	return fmt.Sprintf("Package: %s\nVersion: 1.0\nArchitecture: amd64\nFilename: %s\nSize: 100\nSHA256: %s\n\n",
		name, filename, hash)
}

// TestReparse_DoesNotLeakOldGenerations verifies that re-parsing a repo's
// Packages file replaces the previous entries instead of accumulating them.
// Before this was fixed, byBasename appended a new generation on every
// apt-get update while keeping the old one reachable — unbounded memory
// growth in a long-running daemon, and a basename lookup list that grew by
// the full package count each re-parse.
func TestReparse_DoesNotLeakOldGenerations(t *testing.T) {
	idx := New(t.TempDir(), zap.NewNop())
	repoURL := "http://deb.example.org/debian/dists/stable/main/binary-amd64/Packages"
	data := packagesEntry("hello", "pool/main/h/hello/hello_1.0_amd64.deb", reparseHashA)

	for i := range 5 {
		if err := idx.LoadFromData([]byte(data), repoURL); err != nil {
			t.Fatalf("LoadFromData (round %d): %v", i, err)
		}
	}

	idx.mu.RLock()
	entries := len(idx.byBasename["hello_1.0_amd64.deb"])
	total := len(idx.packages)
	idx.mu.RUnlock()

	if entries != 1 {
		t.Errorf("byBasename entries after 5 re-parses = %d, want 1 (old generations must be dropped)", entries)
	}
	if total != 1 {
		t.Errorf("packages map size = %d, want 1", total)
	}
	if idx.GetBySHA256(reparseHashA) == nil {
		t.Error("package lookup broken after re-parse")
	}
}

// TestReparse_DropsRemovedPackages verifies that a package no longer present
// in the repo's index disappears from lookups after a re-parse.
func TestReparse_DropsRemovedPackages(t *testing.T) {
	idx := New(t.TempDir(), zap.NewNop())
	repoURL := "http://deb.example.org/debian/dists/stable/main/binary-amd64/Packages"

	both := packagesEntry("aaa", "pool/main/a/aaa/aaa_1.0_amd64.deb", reparseHashA) +
		packagesEntry("bbb", "pool/main/b/bbb/bbb_1.0_amd64.deb", reparseHashB)
	if err := idx.LoadFromData([]byte(both), repoURL); err != nil {
		t.Fatalf("LoadFromData: %v", err)
	}
	if idx.GetBySHA256(reparseHashA) == nil || idx.GetBySHA256(reparseHashB) == nil {
		t.Fatal("initial parse incomplete")
	}

	onlyB := packagesEntry("bbb", "pool/main/b/bbb/bbb_1.0_amd64.deb", reparseHashB)
	if err := idx.LoadFromData([]byte(onlyB), repoURL); err != nil {
		t.Fatalf("LoadFromData (re-parse): %v", err)
	}

	if idx.GetBySHA256(reparseHashA) != nil {
		t.Error("package removed from the repo index is still resolvable")
	}
	if idx.GetBySHA256(reparseHashB) == nil {
		t.Error("package still in the repo index was lost")
	}
}

// TestReparse_PreservesOtherRepoOwnership verifies the pointer-identity guard:
// when two repos list the same package (same SHA256) and one repo is
// re-parsed, the global lookup entry now owned by the other repo must survive.
func TestReparse_PreservesOtherRepoOwnership(t *testing.T) {
	idx := New(t.TempDir(), zap.NewNop())
	repo1 := "http://deb.example.org/debian/dists/stable/main/binary-amd64/Packages"
	repo2 := "http://mirror.example.org/debian/dists/stable/main/binary-amd64/Packages"
	entry := packagesEntry("shared", "pool/main/s/shared/shared_1.0_amd64.deb", reparseHashA)

	if err := idx.LoadFromData([]byte(entry), repo1); err != nil {
		t.Fatalf("LoadFromData repo1: %v", err)
	}
	// repo2's parse overwrites packages[sha] — repo2 now owns the global entry.
	if err := idx.LoadFromData([]byte(entry), repo2); err != nil {
		t.Fatalf("LoadFromData repo2: %v", err)
	}

	// Re-parsing repo1 with unrelated content must not delete repo2's entry.
	other := packagesEntry("other", "pool/main/o/other/other_1.0_amd64.deb", reparseHashB)
	if err := idx.LoadFromData([]byte(other), repo1); err != nil {
		t.Fatalf("LoadFromData repo1 re-parse: %v", err)
	}

	pkg := idx.GetBySHA256(reparseHashA)
	if pkg == nil {
		t.Fatal("shared package lost after the other repo re-parsed")
	}
	if !strings.Contains(repo2, pkg.Repo) {
		t.Errorf("surviving entry owned by %q, want the repo2 entry", pkg.Repo)
	}
}
