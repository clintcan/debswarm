package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peerstore"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/p2p"
)

// A LAN (fleet) peer that serves a tampered copy must be counted as a
// verification failure as well as blacklisted, like a DHT peer. Previously only
// the blacklist was recorded, so the dashboard showed 0 verification failures
// after an attack.
func TestFleetPeerTamperedCopyCountsAsVerificationFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping P2P test in short mode")
	}
	genuine := []byte("genuine package bytes for the fleet tamper test")
	sum := sha256.Sum256(genuine)
	hash := hex.EncodeToString(sum[:])
	tampered := bytes.Clone(genuine)
	tampered[10] ^= 0xFF

	// A malicious peer that answers for the genuine hash with tampered bytes.
	evil, err := p2p.New(context.Background(), &p2p.Config{ListenPort: 0, MaxConnections: 10}, zap.NewNop())
	if err != nil {
		t.Fatalf("p2p.New evil: %v", err)
	}
	defer evil.Close()
	evil.SetContentGetter(func(string) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(tampered)), int64(len(tampered)), nil
	})

	s, node, _, _ := newSeederServer(t, false)
	node.Host().Peerstore().AddAddrs(evil.PeerID(), evil.Addrs(), peerstore.TempAddrTTL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.downloadFromFleetPeer(ctx, evil.PeerID(), hash, "pkg_1.0_all.deb"); err == nil {
		t.Fatal("tampered copy from a fleet peer was accepted")
	}

	if got := s.metrics.VerificationFailures.Value(); got != 1 {
		t.Errorf("VerificationFailures = %d, want 1", got)
	}
	if got := s.metrics.PeersBlacklisted.Value(); got != 1 {
		t.Errorf("PeersBlacklisted = %d, want 1", got)
	}
	if s.cache.Has(hash) {
		t.Error("tampered copy was cached")
	}
}
