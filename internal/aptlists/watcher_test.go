package aptlists

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/index"
)

func testLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

func TestNew(t *testing.T) {
	idx := index.New(t.TempDir(), testLogger())
	w := New(idx, testLogger(), nil)

	if w.listsPath != DefaultAPTListsPath {
		t.Errorf("Expected default path %s, got %s", DefaultAPTListsPath, w.listsPath)
	}

	// Test with custom config
	w2 := New(idx, testLogger(), &Config{ListsPath: "/custom/path"})
	if w2.listsPath != "/custom/path" {
		t.Errorf("Expected custom path /custom/path, got %s", w2.listsPath)
	}
}

func TestIsPackagesFile(t *testing.T) {
	idx := index.New(t.TempDir(), testLogger())
	w := New(idx, testLogger(), nil)

	tests := []struct {
		name     string
		filename string
		expected bool
	}{
		{"plain packages", "archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages", true},
		{"gzip packages", "archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages.gz", true},
		{"xz packages", "archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages.xz", true},
		{"lz4 packages", "archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages.lz4", true},
		{"partial download", "archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages.partial", false},
		{"release file", "archive.ubuntu.com_ubuntu_dists_jammy_Release", false},
		{"inrelease file", "archive.ubuntu.com_ubuntu_dists_jammy_InRelease", false},
		{"translation file", "archive.ubuntu.com_ubuntu_dists_jammy_main_i18n_Translation-en", false},
		{"lock file", "lock", false},
		{"partial dir marker", "partial", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := w.isPackagesFile(tt.filename)
			if result != tt.expected {
				t.Errorf("isPackagesFile(%q) = %v, want %v", tt.filename, result, tt.expected)
			}
		})
	}
}

func TestExtractRepoFromFilename(t *testing.T) {
	idx := index.New(t.TempDir(), testLogger())
	w := New(idx, testLogger(), nil)

	tests := []struct {
		name     string
		filename string
		expected string
	}{
		{
			"ubuntu archive",
			"archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages.gz",
			"archive.ubuntu.com/ubuntu",
		},
		{
			"debian archive",
			"deb.debian.org_debian_dists_bookworm_main_binary-amd64_Packages.xz",
			"deb.debian.org/debian",
		},
		{
			"security updates",
			"security.ubuntu.com_ubuntu_dists_jammy-security_main_binary-amd64_Packages.gz",
			"security.ubuntu.com/ubuntu",
		},
		{
			"ppa",
			"ppa.launchpad.net_user_ppa_ubuntu_dists_jammy_main_binary-amd64_Packages",
			"ppa.launchpad.net/user/ppa/ubuntu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := w.extractRepoFromFilename(tt.filename)
			if result != tt.expected {
				t.Errorf("extractRepoFromFilename(%q) = %q, want %q", tt.filename, result, tt.expected)
			}
		})
	}
}

func TestScanAll_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	idx := index.New(tmpDir, testLogger())
	w := New(idx, testLogger(), &Config{ListsPath: tmpDir})

	count, err := w.scanAll()
	if err != nil {
		t.Fatalf("scanAll failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 files scanned, got %d", count)
	}
}

func TestScanAll_WithPackagesFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock Packages file
	packagesContent := `Package: test-package
Version: 1.0.0
Architecture: amd64
Filename: pool/main/t/test-package/test-package_1.0.0_amd64.deb
Size: 1234
SHA256: abc123def456789012345678901234567890123456789012345678901234abcd

Package: another-package
Version: 2.0.0
Architecture: amd64
Filename: pool/main/a/another-package/another-package_2.0.0_amd64.deb
Size: 5678
SHA256: def456789012345678901234567890123456789012345678901234abcdef12
`

	packagesPath := filepath.Join(tmpDir, "archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages")
	if err := os.WriteFile(packagesPath, []byte(packagesContent), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	idx := index.New(tmpDir, testLogger())
	w := New(idx, testLogger(), &Config{ListsPath: tmpDir})

	count, err := w.scanAll()
	if err != nil {
		t.Fatalf("scanAll failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 file scanned, got %d", count)
	}

	// Check that packages were added to index
	if idx.Count() != 2 {
		t.Errorf("Expected 2 packages in index, got %d", idx.Count())
	}
}

func TestStart_NonExistentDir(t *testing.T) {
	idx := index.New(t.TempDir(), testLogger())
	w := New(idx, testLogger(), &Config{ListsPath: "/nonexistent/path"})

	ctx := context.Background()
	err := w.Start(ctx)
	if err != nil {
		t.Errorf("Start should not error for non-existent dir, got: %v", err)
	}
}

func TestStart_AndStop(t *testing.T) {
	tmpDir := t.TempDir()
	idx := index.New(tmpDir, testLogger())
	w := New(idx, testLogger(), &Config{ListsPath: tmpDir})

	ctx := context.Background()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Stop should not block
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Stop timed out")
	}
}

func TestWatcher_FileChange(t *testing.T) {
	tmpDir := t.TempDir()
	idx := index.New(tmpDir, testLogger())
	w := New(idx, testLogger(), &Config{ListsPath: tmpDir})

	ctx := context.Background()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer w.Stop()

	// Create a Packages file after watcher starts
	packagesContent := `Package: new-package
Version: 1.0.0
Architecture: amd64
Filename: pool/main/n/new-package/new-package_1.0.0_amd64.deb
Size: 1234
SHA256: 1234567890123456789012345678901234567890123456789012345678901234
`

	packagesPath := filepath.Join(tmpDir, "test.example.com_repo_dists_stable_main_binary-amd64_Packages")
	if err := os.WriteFile(packagesPath, []byte(packagesContent), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Wait for debounce + processing (debounce is 2 seconds)
	time.Sleep(3 * time.Second)

	// Check that package was added
	if idx.Count() < 1 {
		t.Errorf("Expected at least 1 package in index after file change, got %d", idx.Count())
	}
}
