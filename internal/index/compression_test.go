package index

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"go.uber.org/zap"
)

func compressionEntry(name, hash string) []byte {
	return fmt.Appendf(nil,
		"Package: %s\nVersion: 1.0\nArchitecture: amd64\nFilename: pool/main/x/%s/%s_1.0_amd64.deb\nSize: 100\nSHA256: %s\n\n",
		name, name, name, hash)
}

// TestLoadFromFile_LZ4AndZstd verifies that lz4- and zstd-compressed APT list
// files parse into index entries. Ubuntu minimized/cloud images write lists as
// .lz4 by default (Acquire::GzipIndexes); these used to be scanned as raw
// binary and silently contributed zero entries, quietly breaking
// restart-recovery via the lists watcher on those platforms.
func TestLoadFromFile_LZ4AndZstd(t *testing.T) {
	dir := t.TempDir()

	// lz4 frame
	lz4Hash := "eeee567890123456789012345678901234567890123456789012345678901234"
	var lz4Buf bytes.Buffer
	lw := lz4.NewWriter(&lz4Buf)
	if _, err := lw.Write(compressionEntry("lzpkg", lz4Hash)); err != nil {
		t.Fatalf("lz4 write: %v", err)
	}
	if err := lw.Close(); err != nil {
		t.Fatalf("lz4 close: %v", err)
	}
	lz4Path := filepath.Join(dir, "repo_dists_stable_main_binary-amd64_Packages.lz4")
	if err := os.WriteFile(lz4Path, lz4Buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	// zstd
	zstHash := "ffff567890123456789012345678901234567890123456789012345678901234"
	var zstBuf bytes.Buffer
	zw, err := zstd.NewWriter(&zstBuf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	if _, err := zw.Write(compressionEntry("zstpkg", zstHash)); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	zstPath := filepath.Join(dir, "repo_dists_stable_universe_binary-amd64_Packages.zst")
	if err := os.WriteFile(zstPath, zstBuf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	idx := New(t.TempDir(), zap.NewNop())
	if err := idx.LoadFromFileWithRepo(lz4Path, "repo.example.org/debian"); err != nil {
		t.Fatalf("LoadFromFileWithRepo lz4: %v", err)
	}
	if err := idx.LoadFromFileWithRepo(zstPath, "repo.example.org/debian"); err != nil {
		t.Fatalf("LoadFromFileWithRepo zstd: %v", err)
	}

	if idx.GetBySHA256(lz4Hash) == nil {
		t.Error("lz4-compressed list contributed no entries")
	}
	if idx.GetBySHA256(zstHash) == nil {
		t.Error("zstd-compressed list contributed no entries")
	}
}

// TestLoadFromFile_Bz2 decodes a pre-built bz2 fixture (Go's standard library
// has no bz2 writer).
func TestLoadFromFile_Bz2(t *testing.T) {
	const bz2Fixture = "QlpoOTFBWSZTWXNNtLQAADTfgEAQQAH/8CFASQC+794QIAByM9T1GIGgaaAAaD9UDJUfoRMg2mmg0hiYjTDo5F5X4vBoDdGMzpmdAHn1kjAWAxqBfAIR2IerxAloNgMQa/RVL7RJAI1Sz0sjTRiqDbJa5ggbhw6dKRrzQUznGO7kAgGxEcDXzQwm9fKtVKwwX4u5IpwoSDmm2loA"
	raw, err := base64.StdEncoding.DecodeString(bz2Fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "repo_Packages.bz2")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	idx := New(t.TempDir(), zap.NewNop())
	if err := idx.LoadFromFileWithRepo(path, "repo.example.org/debian"); err != nil {
		t.Fatalf("LoadFromFileWithRepo bz2: %v", err)
	}
	if idx.GetBySHA256("dddd567890123456789012345678901234567890123456789012345678901234") == nil {
		t.Error("bz2-compressed list contributed no entries")
	}
}

// TestLoadFromData_ZstdAndLZ4Magic verifies magic-byte detection for by-hash
// URLs that carry no file extension.
func TestLoadFromData_ZstdAndLZ4Magic(t *testing.T) {
	hash := "abab567890123456789012345678901234567890123456789012345678901234"
	entry := compressionEntry("magicpkg", hash)

	var zstBuf bytes.Buffer
	zw, _ := zstd.NewWriter(&zstBuf)
	_, _ = zw.Write(entry)
	_ = zw.Close()

	idx := New(t.TempDir(), zap.NewNop())
	byHashURL := "http://deb.example.org/debian/dists/stable/main/binary-amd64/by-hash/SHA256/cafe1234"
	if err := idx.LoadFromData(zstBuf.Bytes(), byHashURL); err != nil {
		t.Fatalf("LoadFromData zstd: %v", err)
	}
	if idx.GetBySHA256(hash) == nil {
		t.Error("zstd by-hash payload contributed no entries")
	}

	hash2 := "cdcd567890123456789012345678901234567890123456789012345678901234"
	var lz4Buf bytes.Buffer
	lw := lz4.NewWriter(&lz4Buf)
	_, _ = lw.Write(compressionEntry("magicpkg2", hash2))
	_ = lw.Close()

	byHashURL2 := "http://deb.example.org/debian/dists/stable/contrib/binary-amd64/by-hash/SHA256/beef5678"
	if err := idx.LoadFromData(lz4Buf.Bytes(), byHashURL2); err != nil {
		t.Fatalf("LoadFromData lz4: %v", err)
	}
	if idx.GetBySHA256(hash2) == nil {
		t.Error("lz4 by-hash payload contributed no entries")
	}
}
