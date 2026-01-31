package index

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	return zap.NewNop()
}

// Sample Packages file content for testing
const samplePackagesContent = `Package: vim
Version: 9.0.1378-2
Architecture: amd64
Filename: pool/main/v/vim/vim_9.0.1378-2_amd64.deb
Size: 1234567
SHA256: abc123def456789012345678901234567890123456789012345678901234abcd
Description: Vi IMproved - enhanced vi editor

Package: curl
Version: 7.88.1-10
Architecture: amd64
Filename: pool/main/c/curl/curl_7.88.1-10_amd64.deb
Size: 234567
SHA256: def456abc789012345678901234567890123456789012345678901234567efgh
SHA512: 1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef
Description: command line tool for transferring data with URL syntax

Package: wget
Version: 1.21.3-1
Architecture: amd64
Filename: pool/main/w/wget/wget_1.21.3-1_amd64.deb
Size: 345678
SHA256: 789012345678901234567890123456789012345678901234567890123456ijkl
Description: retrieves files from the web
`

func TestNew(t *testing.T) {
	idx := New("/tmp/test", testLogger())

	if idx == nil {
		t.Fatal("New returned nil")
	}
	if idx.Count() != 0 {
		t.Errorf("Expected 0 packages, got %d", idx.Count())
	}
	if idx.RepoCount() != 0 {
		t.Errorf("Expected 0 repos, got %d", idx.RepoCount())
	}
}

func TestLoadFromData(t *testing.T) {
	idx := New("/tmp/test", testLogger())

	err := idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")
	if err != nil {
		t.Fatalf("LoadFromData failed: %v", err)
	}

	if idx.Count() != 3 {
		t.Errorf("Expected 3 packages, got %d", idx.Count())
	}
	if idx.RepoCount() != 1 {
		t.Errorf("Expected 1 repo, got %d", idx.RepoCount())
	}
}

func TestLoadFromDataGzip(t *testing.T) {
	// Compress the content
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	_, err := gzWriter.Write([]byte(samplePackagesContent))
	if err != nil {
		t.Fatalf("Failed to write gzip: %v", err)
	}
	gzWriter.Close()

	idx := New("/tmp/test", testLogger())
	err = idx.LoadFromData(buf.Bytes(), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.gz")
	if err != nil {
		t.Fatalf("LoadFromData with gzip failed: %v", err)
	}

	if idx.Count() != 3 {
		t.Errorf("Expected 3 packages, got %d", idx.Count())
	}
}

func TestLoadFromDataGzip_ByHashURL(t *testing.T) {
	// Test by-hash URLs where URL has no extension but data is gzip compressed
	// This is the format APT uses: /by-hash/SHA256/<hash>
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	_, err := gzWriter.Write([]byte(samplePackagesContent))
	if err != nil {
		t.Fatalf("Failed to write gzip: %v", err)
	}
	gzWriter.Close()

	idx := New("/tmp/test", testLogger())
	// URL has no .gz extension - compression must be detected from magic bytes
	err = idx.LoadFromData(buf.Bytes(), "http://archive.ubuntu.com/ubuntu/dists/jammy/main/binary-amd64/by-hash/SHA256/37cb57f1554cbfa71c5a29ee9ffee18a9a8c1782bb0568e0874b7ff4ce8f9c11")
	if err != nil {
		t.Fatalf("LoadFromData with by-hash URL failed: %v", err)
	}

	if idx.Count() != 3 {
		t.Errorf("Expected 3 packages, got %d", idx.Count())
	}
}

func TestGetBySHA256(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	pkg := idx.GetBySHA256("abc123def456789012345678901234567890123456789012345678901234abcd")
	if pkg == nil {
		t.Fatal("GetBySHA256 returned nil")
	}
	if pkg.Package != "vim" {
		t.Errorf("Expected package 'vim', got '%s'", pkg.Package)
	}
	if pkg.Version != "9.0.1378-2" {
		t.Errorf("Expected version '9.0.1378-2', got '%s'", pkg.Version)
	}
	if pkg.Size != 1234567 {
		t.Errorf("Expected size 1234567, got %d", pkg.Size)
	}
}

func TestGetBySHA256NotFound(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	pkg := idx.GetBySHA256("nonexistent")
	if pkg != nil {
		t.Error("Expected nil for nonexistent SHA256")
	}
}

func TestGetByPath(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	pkg := idx.GetByPath("pool/main/v/vim/vim_9.0.1378-2_amd64.deb")
	if pkg == nil {
		t.Fatal("GetByPath returned nil")
	}
	if pkg.Package != "vim" {
		t.Errorf("Expected package 'vim', got '%s'", pkg.Package)
	}
}

func TestGetByPathBasename(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	// Should find by basename via secondary index
	pkg := idx.GetByPath("vim_9.0.1378-2_amd64.deb")
	if pkg == nil {
		t.Fatal("GetByPath with basename returned nil")
	}
	if pkg.Package != "vim" {
		t.Errorf("Expected package 'vim', got '%s'", pkg.Package)
	}
}

func TestGetByRepoAndPath(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	pkg := idx.GetByRepoAndPath("deb.debian.org/debian", "pool/main/v/vim/vim_9.0.1378-2_amd64.deb")
	if pkg == nil {
		t.Fatal("GetByRepoAndPath returned nil")
	}
	if pkg.Package != "vim" {
		t.Errorf("Expected package 'vim', got '%s'", pkg.Package)
	}
}

func TestGetByRepoAndPathNotFound(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	// Wrong repo
	pkg := idx.GetByRepoAndPath("archive.ubuntu.com/ubuntu", "pool/main/v/vim/vim_9.0.1378-2_amd64.deb")
	if pkg != nil {
		t.Error("Expected nil for wrong repo")
	}
}

func TestGetByURLPath(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	pkg := idx.GetByURLPath("http://deb.debian.org/debian/pool/main/v/vim/vim_9.0.1378-2_amd64.deb")
	if pkg == nil {
		t.Fatal("GetByURLPath returned nil")
	}
	if pkg.Package != "vim" {
		t.Errorf("Expected package 'vim', got '%s'", pkg.Package)
	}
}

func TestGetByPathSuffix(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	pkg := idx.GetByPathSuffix("vim/vim_9.0.1378-2_amd64.deb")
	if pkg == nil {
		t.Fatal("GetByPathSuffix returned nil")
	}
	if pkg.Package != "vim" {
		t.Errorf("Expected package 'vim', got '%s'", pkg.Package)
	}
}

func TestClear(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	if idx.Count() == 0 {
		t.Fatal("Expected packages after load")
	}

	idx.Clear()

	if idx.Count() != 0 {
		t.Errorf("Expected 0 packages after clear, got %d", idx.Count())
	}
	if idx.RepoCount() != 0 {
		t.Errorf("Expected 0 repos after clear, got %d", idx.RepoCount())
	}
}

func TestClearRepo(t *testing.T) {
	idx := New("/tmp/test", testLogger())

	// Load two repos
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")
	_ = idx.LoadFromData([]byte(`Package: hello
Version: 1.0
Architecture: amd64
Filename: pool/main/h/hello/hello_1.0_amd64.deb
Size: 12345
SHA256: aaaabbbbccccddddeeeeffffgggghhhhiiiijjjjkkkkllllmmmmnnnnoooopppp
Description: Hello world
`), "http://archive.ubuntu.com/ubuntu/dists/jammy/main/binary-amd64/Packages")

	if idx.RepoCount() != 2 {
		t.Fatalf("Expected 2 repos, got %d", idx.RepoCount())
	}

	idx.ClearRepo("deb.debian.org/debian")

	if idx.RepoCount() != 1 {
		t.Errorf("Expected 1 repo after ClearRepo, got %d", idx.RepoCount())
	}
	if idx.Count() != 1 {
		t.Errorf("Expected 1 package after ClearRepo, got %d", idx.Count())
	}
}

func TestExtractRepoFromURL(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.gz", "deb.debian.org/debian"},
		{"https://archive.ubuntu.com/ubuntu/pool/main/v/vim/vim_9.0.deb", "archive.ubuntu.com/ubuntu"},
		{"http://mirror.example.com/debian/dists/stable/Release", "mirror.example.com/debian"},
		{"https://packages.example.org/dists/test/Packages", "packages.example.org"},
		{"http://localhost:8080/pool/main/test.deb", "localhost:8080"},
	}

	for _, tt := range tests {
		result := ExtractRepoFromURL(tt.url)
		if result != tt.expected {
			t.Errorf("ExtractRepoFromURL(%q) = %q, want %q", tt.url, result, tt.expected)
		}
	}
}

func TestExtractPathFromURL(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"http://deb.debian.org/debian/pool/main/v/vim/vim_9.0.deb", "pool/main/v/vim/vim_9.0.deb"},
		{"https://archive.ubuntu.com/ubuntu/dists/jammy/main/binary-amd64/Packages.gz", "dists/jammy/main/binary-amd64/Packages.gz"},
		{"http://example.com/test", ""},
	}

	for _, tt := range tests {
		result := ExtractPathFromURL(tt.url)
		if result != tt.expected {
			t.Errorf("ExtractPathFromURL(%q) = %q, want %q", tt.url, result, tt.expected)
		}
	}
}

func TestMultipleRepos(t *testing.T) {
	idx := New("/tmp/test", testLogger())

	// Load same package from two repos
	content1 := `Package: vim
Version: 9.0.1
Architecture: amd64
Filename: pool/main/v/vim/vim_9.0.1_amd64.deb
Size: 1000
SHA256: aaaa111122223333444455556666777788889999aaaabbbbccccddddeeeeffff
Description: vim from debian
`
	content2 := `Package: vim
Version: 9.0.2
Architecture: amd64
Filename: pool/main/v/vim/vim_9.0.2_amd64.deb
Size: 1001
SHA256: bbbb111122223333444455556666777788889999aaaabbbbccccddddeeeeffff
Description: vim from ubuntu
`

	_ = idx.LoadFromData([]byte(content1), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")
	_ = idx.LoadFromData([]byte(content2), "http://archive.ubuntu.com/ubuntu/dists/jammy/main/binary-amd64/Packages")

	if idx.Count() != 2 {
		t.Errorf("Expected 2 packages, got %d", idx.Count())
	}
	if idx.RepoCount() != 2 {
		t.Errorf("Expected 2 repos, got %d", idx.RepoCount())
	}

	// Should be able to get both by SHA256
	pkg1 := idx.GetBySHA256("aaaa111122223333444455556666777788889999aaaabbbbccccddddeeeeffff")
	pkg2 := idx.GetBySHA256("bbbb111122223333444455556666777788889999aaaabbbbccccddddeeeeffff")

	if pkg1 == nil || pkg2 == nil {
		t.Fatal("Failed to get packages from different repos")
	}
	if pkg1.Version != "9.0.1" || pkg2.Version != "9.0.2" {
		t.Error("Package versions don't match expected")
	}
}

func TestPackageWithoutSHA256(t *testing.T) {
	idx := New("/tmp/test", testLogger())

	// Package without SHA256 should be skipped
	content := `Package: test
Version: 1.0
Architecture: amd64
Filename: pool/main/t/test/test_1.0_amd64.deb
Size: 1000
Description: test package without sha256

Package: test2
Version: 1.0
Architecture: amd64
Filename: pool/main/t/test2/test2_1.0_amd64.deb
Size: 1000
SHA256: cccc111122223333444455556666777788889999aaaabbbbccccddddeeeeffff
Description: test package with sha256
`

	_ = idx.LoadFromData([]byte(content), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	// Only the package with SHA256 should be indexed
	if idx.Count() != 1 {
		t.Errorf("Expected 1 package (with SHA256), got %d", idx.Count())
	}
}

func TestConcurrentAccess(t *testing.T) {
	idx := New("/tmp/test", testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = idx.GetBySHA256("abc123def456789012345678901234567890123456789012345678901234abcd")
			_ = idx.GetByPath("pool/main/v/vim/vim_9.0.1378-2_amd64.deb")
			_ = idx.Count()
			_ = idx.RepoCount()
		}()
	}

	// Concurrent writes (loading more data)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			content := `Package: test` + string(rune('0'+i)) + `
Version: 1.0
Architecture: amd64
Filename: pool/main/t/test` + string(rune('0'+i)) + `/test_1.0_amd64.deb
Size: 1000
SHA256: dddd` + string(rune('0'+i)) + `11122223333444455556666777788889999aaaabbbbccccddddeeeeffff
Description: test
`
			err := idx.LoadFromData([]byte(content), "http://test"+string(rune('0'+i))+".example.com/debian/dists/test/Packages")
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent access error: %v", err)
	}
}

func TestIsAllowedIndexURL(t *testing.T) {
	tests := []struct {
		url     string
		allowed bool
	}{
		{"http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.gz", true},
		{"https://archive.ubuntu.com/ubuntu/pool/main/v/vim/vim.deb", true},
		{"http://localhost/dists/test/Packages", false},
		{"http://127.0.0.1/pool/main/test.deb", false},
		{"http://169.254.169.254/latest/meta-data/", false},
		{"http://192.168.1.1/debian/dists/test/Packages", false},
	}

	for _, tt := range tests {
		result := isAllowedIndexURL(tt.url)
		if result != tt.allowed {
			t.Errorf("isAllowedIndexURL(%q) = %v, want %v", tt.url, result, tt.allowed)
		}
	}
}

func TestLoadFromFile(t *testing.T) {
	idx := New(t.TempDir(), testLogger())

	// Create a temporary plain Packages file
	tmpFile := filepath.Join(t.TempDir(), "Packages")
	err := os.WriteFile(tmpFile, []byte(samplePackagesContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	err = idx.LoadFromFile(tmpFile)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if idx.Count() != 3 {
		t.Errorf("Expected 3 packages, got %d", idx.Count())
	}
}

func TestLoadFromFile_Gzip(t *testing.T) {
	idx := New(t.TempDir(), testLogger())

	// Create a gzip compressed file
	tmpFile := filepath.Join(t.TempDir(), "Packages.gz")
	f, err := os.Create(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	gzWriter := gzip.NewWriter(f)
	_, err = gzWriter.Write([]byte(samplePackagesContent))
	if err != nil {
		f.Close()
		t.Fatalf("Failed to write gzip: %v", err)
	}
	gzWriter.Close()
	f.Close()

	err = idx.LoadFromFile(tmpFile)
	if err != nil {
		t.Fatalf("LoadFromFile with gzip failed: %v", err)
	}

	if idx.Count() != 3 {
		t.Errorf("Expected 3 packages, got %d", idx.Count())
	}
}

func TestLoadFromFile_NotFound(t *testing.T) {
	idx := New(t.TempDir(), testLogger())

	err := idx.LoadFromFile("/nonexistent/path/Packages")
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestLoadFromFile_InvalidGzip(t *testing.T) {
	idx := New(t.TempDir(), testLogger())

	// Create a file with .gz extension but invalid gzip content
	tmpFile := filepath.Join(t.TempDir(), "Packages.gz")
	err := os.WriteFile(tmpFile, []byte("not gzip content"), 0644)
	if err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	err = idx.LoadFromFile(tmpFile)
	if err == nil {
		t.Error("Expected error for invalid gzip file")
	}
}

func TestLoadFromURL(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(samplePackagesContent))
	}))
	defer server.Close()

	idx := New(t.TempDir(), testLogger())

	// LoadFromURL will reject localhost URLs for security
	// So we test the error path
	err := idx.LoadFromURL(server.URL + "/dists/test/Packages")
	if err == nil {
		t.Error("Expected error for localhost URL (SSRF protection)")
	}
}

func TestLoadFromURL_BlockedURL(t *testing.T) {
	idx := New(t.TempDir(), testLogger())

	// Test SSRF protection
	err := idx.LoadFromURL("http://127.0.0.1/Packages")
	if err == nil {
		t.Error("Expected error for blocked URL")
	}

	err = idx.LoadFromURL("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Error("Expected error for AWS metadata URL")
	}
}

func TestGetByURLPath_NotFound(t *testing.T) {
	idx := New(t.TempDir(), testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	// Nonexistent package path - GetByURLPath has fallback to GetByPath
	pkg := idx.GetByURLPath("http://wrong.repo.com/pool/main/x/nonexistent/nonexistent.deb")
	if pkg != nil {
		t.Error("Expected nil for nonexistent package path")
	}
}

func TestGetByPathSuffix_NotFound(t *testing.T) {
	idx := New(t.TempDir(), testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	pkg := idx.GetByPathSuffix("nonexistent/package.deb")
	if pkg != nil {
		t.Error("Expected nil for nonexistent suffix")
	}
}

func TestClearRepo_NonexistentRepo(t *testing.T) {
	idx := New(t.TempDir(), testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	// Clear a repo that doesn't exist - should not error
	idx.ClearRepo("nonexistent.repo.com")

	// Original packages should still be there
	if idx.Count() != 3 {
		t.Errorf("Expected 3 packages after clearing nonexistent repo, got %d", idx.Count())
	}
}

func TestExtractRepoFromURL_EdgeCases(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"", ""},
		{"invalid-url", "invalid-url"}, // No "/" means return input as-is
		{"http://example.com", "example.com"},
		{"http://example.com/", "example.com"},
		{"http://example.com/path", "example.com"},
	}

	for _, tt := range tests {
		result := ExtractRepoFromURL(tt.url)
		if result != tt.expected {
			t.Errorf("ExtractRepoFromURL(%q) = %q, want %q", tt.url, result, tt.expected)
		}
	}
}

func TestLoadFromData_InvalidXZ(t *testing.T) {
	idx := New(t.TempDir(), testLogger())

	// Try to load data that has XZ magic bytes but is otherwise invalid
	// XZ magic: 0xfd '7' 'z' 'X' 'Z' 0x00
	invalidXZ := []byte{0xfd, '7', 'z', 'X', 'Z', 0x00, 0x00, 0x00}
	err := idx.LoadFromData(invalidXZ, "http://deb.debian.org/debian/dists/test/Packages")
	if err == nil {
		t.Error("Expected error for invalid xz data")
	}
}

func TestGetByPath_EmptyIndex(t *testing.T) {
	idx := New(t.TempDir(), testLogger())

	pkg := idx.GetByPath("pool/main/v/vim/vim.deb")
	if pkg != nil {
		t.Error("Expected nil for empty index")
	}
}

func TestGetByRepoAndPath_EmptyRepo(t *testing.T) {
	idx := New(t.TempDir(), testLogger())
	_ = idx.LoadFromData([]byte(samplePackagesContent), "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages")

	// Try to get from empty repo string
	pkg := idx.GetByRepoAndPath("", "pool/main/v/vim/vim.deb")
	if pkg != nil {
		t.Error("Expected nil for empty repo")
	}
}
