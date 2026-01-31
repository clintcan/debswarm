package aptarchives

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/index"
)

func testLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

func TestNew(t *testing.T) {
	tmpDir := t.TempDir()
	c, err := cache.New(tmpDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	idx := index.New(tmpDir, testLogger())
	i := New(c, idx, testLogger(), nil)

	if i.archivesPath != DefaultAPTArchivesPath {
		t.Errorf("Expected default path %s, got %s", DefaultAPTArchivesPath, i.archivesPath)
	}

	// Test with custom config
	i2 := New(c, idx, testLogger(), &Config{ArchivesPath: "/custom/path"})
	if i2.archivesPath != "/custom/path" {
		t.Errorf("Expected custom path /custom/path, got %s", i2.archivesPath)
	}
}

func TestImport_NonExistentDir(t *testing.T) {
	tmpDir := t.TempDir()
	c, err := cache.New(tmpDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	idx := index.New(tmpDir, testLogger())
	i := New(c, idx, testLogger(), &Config{ArchivesPath: "/nonexistent/path"})

	ctx := context.Background()
	result, err := i.Import(ctx)
	if err != nil {
		t.Errorf("Import should not error for non-existent dir, got: %v", err)
	}
	if result.Scanned != 0 {
		t.Errorf("Expected 0 scanned, got %d", result.Scanned)
	}
}

func TestImport_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	archivesDir := filepath.Join(tmpDir, "archives")
	if err := os.MkdirAll(archivesDir, 0755); err != nil {
		t.Fatalf("Failed to create archives dir: %v", err)
	}

	cacheDir := filepath.Join(tmpDir, "cache")
	c, err := cache.New(cacheDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	idx := index.New(cacheDir, testLogger())
	i := New(c, idx, testLogger(), &Config{ArchivesPath: archivesDir})

	ctx := context.Background()
	result, err := i.Import(ctx)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	if result.Scanned != 0 {
		t.Errorf("Expected 0 scanned, got %d", result.Scanned)
	}
}

func TestImport_SkipsPartialDir(t *testing.T) {
	tmpDir := t.TempDir()
	archivesDir := filepath.Join(tmpDir, "archives")
	partialDir := filepath.Join(archivesDir, "partial")
	if err := os.MkdirAll(partialDir, 0755); err != nil {
		t.Fatalf("Failed to create partial dir: %v", err)
	}

	// Create a .deb file in partial dir (should be skipped)
	partialDeb := filepath.Join(partialDir, "test_1.0_amd64.deb")
	if err := os.WriteFile(partialDeb, []byte("partial content"), 0644); err != nil {
		t.Fatalf("Failed to create partial deb: %v", err)
	}

	cacheDir := filepath.Join(tmpDir, "cache")
	c, err := cache.New(cacheDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	idx := index.New(cacheDir, testLogger())
	i := New(c, idx, testLogger(), &Config{ArchivesPath: archivesDir})

	ctx := context.Background()
	result, err := i.Import(ctx)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	// Should not have scanned anything - the .deb is in a subdirectory which we skip
	if result.Scanned != 0 {
		t.Errorf("Expected 0 scanned (partial dir should be skipped), got %d", result.Scanned)
	}
}

func TestImport_SkipsNonDeb(t *testing.T) {
	tmpDir := t.TempDir()
	archivesDir := filepath.Join(tmpDir, "archives")
	if err := os.MkdirAll(archivesDir, 0755); err != nil {
		t.Fatalf("Failed to create archives dir: %v", err)
	}

	// Create non-.deb files
	if err := os.WriteFile(filepath.Join(archivesDir, "lock"), []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create lock file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archivesDir, "test.txt"), []byte("text"), 0644); err != nil {
		t.Fatalf("Failed to create txt file: %v", err)
	}

	cacheDir := filepath.Join(tmpDir, "cache")
	c, err := cache.New(cacheDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	idx := index.New(cacheDir, testLogger())
	i := New(c, idx, testLogger(), &Config{ArchivesPath: archivesDir})

	ctx := context.Background()
	result, err := i.Import(ctx)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	if result.Scanned != 0 {
		t.Errorf("Expected 0 scanned (non-.deb files), got %d", result.Scanned)
	}
}

func TestImport_UnverifiedPackage(t *testing.T) {
	tmpDir := t.TempDir()
	archivesDir := filepath.Join(tmpDir, "archives")
	if err := os.MkdirAll(archivesDir, 0755); err != nil {
		t.Fatalf("Failed to create archives dir: %v", err)
	}

	// Create a .deb file that's not in the index
	debPath := filepath.Join(archivesDir, "unknown_1.0_amd64.deb")
	if err := os.WriteFile(debPath, []byte("fake deb content"), 0644); err != nil {
		t.Fatalf("Failed to create deb file: %v", err)
	}

	cacheDir := filepath.Join(tmpDir, "cache")
	c, err := cache.New(cacheDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	idx := index.New(cacheDir, testLogger())
	i := New(c, idx, testLogger(), &Config{ArchivesPath: archivesDir})

	ctx := context.Background()
	result, err := i.Import(ctx)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	if result.Scanned != 1 {
		t.Errorf("Expected 1 scanned, got %d", result.Scanned)
	}
	if result.Unverified != 1 {
		t.Errorf("Expected 1 unverified, got %d", result.Unverified)
	}
	if result.Imported != 0 {
		t.Errorf("Expected 0 imported, got %d", result.Imported)
	}
}

func TestImport_WithKnownPackage(t *testing.T) {
	tmpDir := t.TempDir()
	archivesDir := filepath.Join(tmpDir, "archives")
	if err := os.MkdirAll(archivesDir, 0755); err != nil {
		t.Fatalf("Failed to create archives dir: %v", err)
	}

	// Create a fake .deb file
	debContent := []byte("This is fake deb content for testing purposes only.")
	debPath := filepath.Join(archivesDir, "test-package_1.0.0_amd64.deb")
	if err := os.WriteFile(debPath, debContent, 0644); err != nil {
		t.Fatalf("Failed to create deb file: %v", err)
	}

	// Compute the hash of our fake deb
	// SHA256 of "This is fake deb content for testing purposes only." is:
	// We'll compute it properly by using hashutil
	cacheDir := filepath.Join(tmpDir, "cache")
	c, err := cache.New(cacheDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	idx := index.New(cacheDir, testLogger())

	// Create a Packages file that includes our test package
	// First, we need to know the hash
	importer := New(c, idx, testLogger(), &Config{ArchivesPath: archivesDir})
	hash, err := importer.computeHash(debPath)
	if err != nil {
		t.Fatalf("Failed to compute hash: %v", err)
	}

	// Create a Packages file with this hash
	packagesContent := "Package: test-package\n" +
		"Version: 1.0.0\n" +
		"Architecture: amd64\n" +
		"Filename: pool/main/t/test-package/test-package_1.0.0_amd64.deb\n" +
		"Size: " + string(rune(len(debContent))) + "\n" +
		"SHA256: " + hash + "\n\n"

	packagesPath := filepath.Join(tmpDir, "Packages")
	if err := os.WriteFile(packagesPath, []byte(packagesContent), 0644); err != nil {
		t.Fatalf("Failed to write Packages file: %v", err)
	}

	// Load the Packages file into the index
	if err := idx.LoadFromFile(packagesPath); err != nil {
		t.Fatalf("Failed to load Packages file: %v", err)
	}

	// Now run the import
	ctx := context.Background()
	result, err := importer.Import(ctx)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if result.Scanned != 1 {
		t.Errorf("Expected 1 scanned, got %d", result.Scanned)
	}
	if result.Imported != 1 {
		t.Errorf("Expected 1 imported, got %d", result.Imported)
	}

	// Verify the package is now in the cache
	if !c.Has(hash) {
		t.Error("Package should be in cache after import")
	}
}

func TestImport_AlreadyInCache(t *testing.T) {
	tmpDir := t.TempDir()
	archivesDir := filepath.Join(tmpDir, "archives")
	if err := os.MkdirAll(archivesDir, 0755); err != nil {
		t.Fatalf("Failed to create archives dir: %v", err)
	}

	// Create a fake .deb file
	debContent := []byte("Already cached package content.")
	debPath := filepath.Join(archivesDir, "cached-pkg_1.0.0_amd64.deb")
	if err := os.WriteFile(debPath, debContent, 0644); err != nil {
		t.Fatalf("Failed to create deb file: %v", err)
	}

	cacheDir := filepath.Join(tmpDir, "cache")
	c, err := cache.New(cacheDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	idx := index.New(cacheDir, testLogger())
	importer := New(c, idx, testLogger(), &Config{ArchivesPath: archivesDir})

	// Compute hash and pre-cache the package
	hash, err := importer.computeHash(debPath)
	if err != nil {
		t.Fatalf("Failed to compute hash: %v", err)
	}

	// Add to cache first
	f, err := os.Open(debPath)
	if err != nil {
		t.Fatalf("Failed to open deb: %v", err)
	}
	if err := c.Put(f, hash, "cached-pkg_1.0.0_amd64.deb"); err != nil {
		f.Close()
		t.Fatalf("Failed to pre-cache: %v", err)
	}
	f.Close()

	// Now import - should skip since already in cache
	ctx := context.Background()
	result, err := importer.Import(ctx)
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if result.Scanned != 1 {
		t.Errorf("Expected 1 scanned, got %d", result.Scanned)
	}
	if result.Skipped != 1 {
		t.Errorf("Expected 1 skipped (already in cache), got %d", result.Skipped)
	}
	if result.Imported != 0 {
		t.Errorf("Expected 0 imported, got %d", result.Imported)
	}
}

func TestImport_Cancellation(t *testing.T) {
	tmpDir := t.TempDir()
	archivesDir := filepath.Join(tmpDir, "archives")
	if err := os.MkdirAll(archivesDir, 0755); err != nil {
		t.Fatalf("Failed to create archives dir: %v", err)
	}

	// Create multiple .deb files
	for i := 0; i < 5; i++ {
		debPath := filepath.Join(archivesDir, "pkg"+string(rune('0'+i))+"_1.0_amd64.deb")
		if err := os.WriteFile(debPath, []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to create deb file: %v", err)
		}
	}

	cacheDir := filepath.Join(tmpDir, "cache")
	c, err := cache.New(cacheDir, 100*1024*1024, testLogger()) // 100MB
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	idx := index.New(cacheDir, testLogger())
	importer := New(c, idx, testLogger(), &Config{ArchivesPath: archivesDir})

	// Cancel immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = importer.Import(ctx)
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled error, got: %v", err)
	}
}
