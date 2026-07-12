package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/downloader"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/timeouts"
)

// A chunked download that succeeds and verifies but cannot be cached (e.g. the
// cache is full) must still be served to APT, not turned into an HTTP 500.
func TestProcessDownloadSuccess_CacheFullStillServes(t *testing.T) {
	logger := newTestLogger()

	// Tiny cache so PutFile's ensureSpace fails for any real payload.
	tinyCache, err := cache.New(t.TempDir(), 10, logger) // 10 bytes
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = tinyCache.Close() })

	cfg := &Config{
		Addr:        "127.0.0.1:0",
		MetricsPort: 0,
		Metrics:     metrics.New(),
		Timeouts:    timeouts.NewManager(nil),
		Scorer:      peers.NewScorer(),
	}
	srv := NewServer(cfg, tinyCache, index.New(t.TempDir(), logger), nil, mirror.NewFetcher(nil, logger), logger)

	// An assembled, verified file on disk, as the chunked downloader leaves it.
	content := bytes.Repeat([]byte("verified-package-bytes;"), 64) // ~1.5KB, well over the 10-byte cache
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	filePath := filepath.Join(t.TempDir(), "assembled")
	if err := os.WriteFile(filePath, content, 0o600); err != nil {
		t.Fatalf("write assembled file: %v", err)
	}

	res := srv.processDownloadSuccess(context.Background(), &downloader.DownloadResult{
		FilePath: filePath,
		Size:     int64(len(content)),
		Source:   downloader.SourceTypeMirror,
	}, hash, "pkg_1.0_amd64.deb")

	if res == nil {
		t.Fatal("processDownloadSuccess returned nil")
	}
	// Must NOT ask the caller to serve from cache — the cache write failed, so
	// serveFromCache would 500 on the follow-up cache.Get.
	if res.serveFromCache {
		t.Error("serveFromCache=true after a failed cache write would 500; want direct serve")
	}
	if !bytes.Equal(res.data, content) {
		t.Errorf("served %d bytes, want the %d-byte verified payload", len(res.data), len(content))
	}
	// The temp file was read into memory and should be cleaned up, not leaked.
	if _, statErr := os.Stat(filePath); !os.IsNotExist(statErr) {
		t.Errorf("assembled temp file should be removed after in-memory fallback (stat err=%v)", statErr)
	}
	// If the cache had NOT been full, PutFile would have succeeded and the first
	// assertion (serveFromCache) would have caught it — so reaching here with the
	// payload served proves the cache-full path was exercised.
}
