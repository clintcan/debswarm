package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/debswarm/debswarm/internal/downloader"
)

// Bug 5: .udeb (installer) and .ddeb (debug symbol) packages must be classified
// as packages so they are verified and cached, not passed through unverified.
func TestClassifyRequest_InstallerAndDebugPackages(t *testing.T) {
	s := &Server{}
	cases := []struct {
		url  string
		want requestType
	}{
		{"http://deb.debian.org/debian/pool/main/h/hello/hello_2.10_amd64.deb", requestTypePackage},
		{"http://ddebs.ubuntu.com/pool/main/h/hello/hello-dbgsym_2.10_amd64.ddeb", requestTypePackage},
		{"http://deb.debian.org/debian/pool/main/g/grub2/grub-efi_2.06_amd64.udeb", requestTypePackage},
		{"http://deb.debian.org/debian/dists/stable/main/binary-amd64/Packages", requestTypeIndex},
		{"http://deb.debian.org/debian/dists/stable/InRelease", requestTypeRelease},
	}
	for _, c := range cases {
		if got := s.classifyRequest(c.url); got != c.want {
			t.Errorf("classifyRequest(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

// Bug 2: shutting down the announce subsystem must not close announceChan — an
// in-flight goroutine can still call announceAsync, and a send on a closed
// channel panics. The worker must instead exit on context cancellation.
func TestAnnouncementWorker_StopsWithoutClosingChannel(t *testing.T) {
	s := newTestServer(t)

	// This is what Shutdown does to the announce subsystem: cancel and wait.
	// (On the old code the worker only exited when the channel was closed, so this
	// would time out; and the close would make the send below panic.)
	s.announceCancel()
	select {
	case <-s.announceDone:
	case <-time.After(2 * time.Second):
		t.Fatal("announcement worker did not stop on context cancellation")
	}

	// The channel must not have been closed: a send must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("send after shutdown panicked — announceChan was closed: %v", r)
		}
	}()
	for range 10 {
		select {
		case s.announceChan <- "somehash":
		default:
		}
	}
}

// Bug 6: after a chunked download is cached, its per-download assembly directory
// must be removed rather than left behind as an empty (or chunk-littered) dir.
func TestProcessDownloadSuccess_CleansAssemblyDir(t *testing.T) {
	s := newTestServer(t) // 100MB cache — PutFile succeeds

	content := []byte("assembled verified package payload for cleanup test")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	// Simulate the downloader's output: partial/{hash}/assembled plus a leftover
	// chunk file (so we exercise removing a non-empty dir).
	partialDir := s.cache.PartialDir(hash)
	if err := os.MkdirAll(partialDir, 0o755); err != nil {
		t.Fatalf("mkdir partial: %v", err)
	}
	assembly := filepath.Join(partialDir, "assembled")
	if err := os.WriteFile(assembly, content, 0o600); err != nil {
		t.Fatalf("write assembly: %v", err)
	}
	if err := os.WriteFile(filepath.Join(partialDir, "chunk_0"), []byte("leftover"), 0o600); err != nil {
		t.Fatalf("write chunk: %v", err)
	}

	res := s.processDownloadSuccess(context.Background(), &downloader.DownloadResult{
		FilePath: assembly,
		Size:     int64(len(content)),
		Source:   downloader.SourceTypeMixed,
	}, hash, "pkg_1.0_amd64.deb")

	if res == nil || !res.serveFromCache {
		t.Fatalf("expected a serve-from-cache result, got %+v", res)
	}
	if _, err := os.Stat(partialDir); !os.IsNotExist(err) {
		t.Errorf("assembly dir was not cleaned up (stat err=%v)", err)
	}
	if !s.cache.Has(hash) {
		t.Error("package is not in the cache after a successful PutFile")
	}
}
