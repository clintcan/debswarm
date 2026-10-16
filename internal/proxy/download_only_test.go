package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/p2p"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/timeouts"
)

// newSeederServer builds a proxy Server around a real P2P node whose cache holds
// one package, wired exactly as the daemon does (NewServer + SetP2PNode).
func newSeederServer(t *testing.T, downloadOnly bool) (*Server, *p2p.Node, string, []byte) {
	t.Helper()
	logger := zap.NewNop()
	ctx := context.Background()

	content := []byte("download-only test package content")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	pkgCache, err := cache.New(t.TempDir(), 10*1024*1024, logger)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = pkgCache.Close() })
	if err := pkgCache.Put(strings.NewReader(string(content)), hash, "pkg_1.0_all.deb"); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}

	node, err := p2p.New(ctx, &p2p.Config{ListenPort: 0, MaxConnections: 10}, logger)
	if err != nil {
		t.Fatalf("p2p.New: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	cfg := &Config{
		Addr:         "127.0.0.1:0",
		P2PTimeout:   5 * time.Second,
		Metrics:      metrics.New(),
		Timeouts:     timeouts.NewManager(nil),
		Scorer:       peers.NewScorer(),
		DownloadOnly: downloadOnly,
	}
	s := NewServer(cfg, pkgCache, index.New(t.TempDir(), logger), node, mirror.NewFetcher(nil, logger), logger)
	s.SetP2PNode(node)
	return s, node, hash, content
}

// fetchFrom downloads hash from seeder using a fresh leecher node.
func fetchFrom(t *testing.T, seeder *p2p.Node, hash string) ([]byte, error) {
	t.Helper()
	leecher, err := p2p.New(context.Background(), &p2p.Config{ListenPort: 0, MaxConnections: 10}, zap.NewNop())
	if err != nil {
		t.Fatalf("p2p.New leecher: %v", err)
	}
	defer leecher.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return leecher.Download(ctx, peer.AddrInfo{ID: seeder.PeerID(), Addrs: seeder.Addrs()}, hash)
}

func TestDownloadOnly_DoesNotServePeers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping P2P test in short mode")
	}
	_, seeder, hash, _ := newSeederServer(t, true)

	if data, err := fetchFrom(t, seeder, hash); err == nil {
		t.Fatalf("download-only node served %d bytes to a peer; want refusal", len(data))
	}
}

func TestDownloadOnly_SharingNodeStillServes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping P2P test in short mode")
	}
	_, seeder, hash, content := newSeederServer(t, false)

	data, err := fetchFrom(t, seeder, hash)
	if err != nil {
		t.Fatalf("sharing node refused download: %v", err)
	}
	if string(data) != string(content) {
		t.Fatal("served content does not match")
	}
}

func TestDownloadOnly_SkipsAnnouncements(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping P2P test in short mode")
	}
	s, _, hash, _ := newSeederServer(t, true)

	s.announceAsync(hash)
	if n := len(s.announceChan); n != 0 {
		t.Errorf("announceAsync queued %d announcements in download-only mode, want 0", n)
	}
	if err := s.ReannouncePackages(context.Background()); err != nil {
		t.Errorf("ReannouncePackages: %v", err)
	}
	unannounced, err := s.cache.GetUnannounced()
	if err != nil {
		t.Fatalf("GetUnannounced: %v", err)
	}
	if len(unannounced) != 1 {
		t.Errorf("package marked announced in download-only mode: %d unannounced, want 1", len(unannounced))
	}
}
