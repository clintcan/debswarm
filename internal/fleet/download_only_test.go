package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

func newDownloadOnlyCoordinator(t *testing.T, cached map[string]bool, fleetPeers []peer.AddrInfo) (*Coordinator, *mockFleetSender) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DownloadOnly = true
	cfg.ClaimTimeout = 200 * time.Millisecond
	c := New(cfg, &mockPeerProvider{peers: fleetPeers}, &mockCacheChecker{hashes: cached}, zap.NewNop())
	t.Cleanup(func() { _ = c.Close() })
	sender := &mockFleetSender{}
	c.SetSender(sender)
	return c, sender
}

func waitForMessage(sender *mockFleetSender) []struct {
	peerID peer.ID
	msg    *Message
} {
	for i := 0; i < 50; i++ {
		if m := sender.getMessages(); len(m) > 0 {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// A download-only node must not claim a package it holds: it would refuse the
// transfer, so the asker should go elsewhere.
func TestDownloadOnly_CachedPackageAnswersDontHave(t *testing.T) {
	c, sender := newDownloadOnlyCoordinator(t, map[string]bool{"cachedhash": true}, nil)

	c.HandleMessage(peer.ID("asker"), Message{Type: MsgWantPackage, Hash: "cachedhash", Nonce: 7})

	msgs := waitForMessage(sender)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(msgs))
	}
	if msgs[0].msg.Type != MsgDontHave {
		t.Errorf("reply type = %d, want MsgDontHave (%d)", msgs[0].msg.Type, MsgDontHave)
	}
}

// A download-only node fetching from WAN must not tell the fleet, or peers would
// wait on it for a package they cannot then retrieve.
func TestDownloadOnly_FetchingIsNotAdvertised(t *testing.T) {
	c, sender := newDownloadOnlyCoordinator(t, map[string]bool{}, nil)

	c.NotifyFetching("wanhash", 1000)
	c.HandleMessage(peer.ID("asker"), Message{Type: MsgWantPackage, Hash: "wanhash", Nonce: 7})
	msgs := waitForMessage(sender)
	c.NotifyComplete("wanhash")

	if b := sender.getBroadcasts(); len(b) != 0 {
		t.Errorf("download-only node broadcast %d fleet messages, want 0", len(b))
	}
	if len(msgs) != 1 || msgs[0].msg.Type != MsgDontHave {
		t.Fatalf("reply to WantPackage while fetching = %+v, want a single MsgDontHave", msgs)
	}
}

// A download-only node always loses the fetcher election, so a sharing peer
// racing for the same cold package fetches it and the download-only node waits.
func TestDownloadOnly_LosesElection(t *testing.T) {
	sharing := peer.ID("sharing-peer")
	c, _ := newDownloadOnlyCoordinator(t, map[string]bool{}, []peer.AddrInfo{{ID: sharing}})

	if n := c.nonceFn(); n != ^uint32(0) {
		t.Fatalf("download-only nonce = %d, want MaxUint32", n)
	}

	go func() {
		// The sharing peer answers that it is also racing, with an ordinary nonce.
		time.Sleep(20 * time.Millisecond)
		c.HandleMessage(sharing, Message{Type: MsgDontHave, Hash: "coldhash", Nonce: 12345, Offset: 1})
	}()

	res, err := c.WantPackage(context.Background(), "coldhash", 1000)
	if err != nil {
		t.Fatalf("WantPackage: %v", err)
	}
	if res.Action != ActionWaitPeer || res.Provider != sharing {
		t.Fatalf("result = action %v provider %q, want ActionWaitPeer on %q", res.Action, res.Provider, sharing)
	}
}
