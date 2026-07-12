package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// TestWantPackage_AllDontHave_ReturnsBeforeTimeout verifies that once every
// peer the want reached has answered DontHave, WantPackage resolves immediately
// instead of sitting out the full claim timeout. This is the fix for the flat
// multi-second stall previously added to every cold package download.
func TestWantPackage_AllDontHave_ReturnsBeforeTimeout(t *testing.T) {
	peerA := peer.ID("fleet-peer-a")
	peerB := peer.ID("fleet-peer-b")
	cfg := DefaultConfig() // 5s ClaimTimeout — the test must not need it
	c := New(cfg, &mockPeerProvider{peers: []peer.AddrInfo{{ID: peerA}, {ID: peerB}}},
		&mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())
	defer func() { _ = c.Close() }()
	c.SetSender(&mockFleetSender{})

	hash := "hash_all_donthave"
	start := time.Now()
	resultChan := make(chan *WantResult, 1)
	go func() {
		result, _ := c.WantPackage(context.Background(), hash, 1000)
		resultChan <- result
	}()

	// Let WantPackage register its pending want, then both peers NACK.
	time.Sleep(50 * time.Millisecond)
	c.HandleMessage(peerA, Message{Type: MsgDontHave, Hash: hash})
	c.HandleMessage(peerB, Message{Type: MsgDontHave, Hash: hash})

	select {
	case result := <-resultChan:
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("WantPackage took %v, want well under the 5s claim timeout", elapsed)
		}
		if result.Action != ActionFetchWAN {
			t.Errorf("expected ActionFetchWAN, got %v", result.Action)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WantPackage still waiting after all peers answered DontHave")
	}
}

// TestWantPackage_DontHaveAlsoWanting_ElectsLowerNonce verifies that a NACK
// carrying a competitor's lower election nonce (Offset=1) makes us wait on
// that peer instead of both nodes fetching from WAN.
func TestWantPackage_DontHaveAlsoWanting_ElectsLowerNonce(t *testing.T) {
	competitor := peer.ID("competing-peer")
	cfg := DefaultConfig()
	c := New(cfg, &mockPeerProvider{peers: []peer.AddrInfo{{ID: competitor}}},
		&mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())
	defer func() { _ = c.Close() }()
	c.nonceFn = func() uint32 { return 500 }
	c.SetSender(&mockFleetSender{})

	hash := "hash_donthave_election"
	resultChan := make(chan *WantResult, 1)
	go func() {
		result, _ := c.WantPackage(context.Background(), hash, 1000)
		resultChan <- result
	}()

	time.Sleep(50 * time.Millisecond)
	c.HandleMessage(competitor, Message{Type: MsgDontHave, Hash: hash, Nonce: 100, Offset: 1})

	select {
	case result := <-resultChan:
		if result.Action != ActionWaitPeer {
			t.Fatalf("expected ActionWaitPeer (lost election via DontHave), got %v", result.Action)
		}
		if result.Provider != competitor {
			t.Errorf("expected to wait on %v, got %v", competitor, result.Provider)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WantPackage did not return in time")
	}
}

// TestWantPackage_PartialDontHave_FallsBackToTimer verifies mixed-fleet
// compatibility: when one peer never answers (e.g. it runs an older version
// without MsgDontHave), the claim timer remains the backstop.
func TestWantPackage_PartialDontHave_FallsBackToTimer(t *testing.T) {
	peerNew := peer.ID("fleet-peer-new")
	peerOld := peer.ID("fleet-peer-old")
	cfg := DefaultConfig()
	cfg.ClaimTimeout = 300 * time.Millisecond
	c := New(cfg, &mockPeerProvider{peers: []peer.AddrInfo{{ID: peerNew}, {ID: peerOld}}},
		&mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())
	defer func() { _ = c.Close() }()
	c.SetSender(&mockFleetSender{})

	hash := "hash_partial_donthave"
	start := time.Now()
	resultChan := make(chan *WantResult, 1)
	go func() {
		result, _ := c.WantPackage(context.Background(), hash, 1000)
		resultChan <- result
	}()

	// Only one of the two peers answers.
	time.Sleep(50 * time.Millisecond)
	c.HandleMessage(peerNew, Message{Type: MsgDontHave, Hash: hash})

	select {
	case result := <-resultChan:
		if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
			t.Errorf("WantPackage returned after %v; with an unanswered peer it must wait for the claim timer", elapsed)
		}
		if result.Action != ActionFetchWAN {
			t.Errorf("expected ActionFetchWAN, got %v", result.Action)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WantPackage did not return in time")
	}
}

// TestHandleWantPackage_RepliesDontHave verifies that a node that neither has
// nor is fetching a package answers a WantPackage with an explicit NACK.
func TestHandleWantPackage_RepliesDontHave(t *testing.T) {
	sender := &mockFleetSender{}
	c := New(DefaultConfig(), &mockPeerProvider{},
		&mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())
	defer func() { _ = c.Close() }()
	c.SetSender(sender)

	requester := peer.ID("requesting-peer")
	c.HandleMessage(requester, Message{Type: MsgWantPackage, Hash: "hash_nack", Nonce: 42, Size: 1000})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range sender.getMessages() {
			if m.msg.Type == MsgDontHave && m.peerID == requester {
				if m.msg.Hash != "hash_nack" {
					t.Errorf("DontHave hash = %q, want %q", m.msg.Hash, "hash_nack")
				}
				if m.msg.Offset != 0 {
					t.Errorf("DontHave Offset = %d, want 0 (not also wanting)", m.msg.Offset)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no DontHave reply was sent to the requester")
}

// TestHandleWantPackage_RepliesDontHaveAlsoWanting verifies that a node racing
// for the same cold package flags its own election nonce in the NACK.
func TestHandleWantPackage_RepliesDontHaveAlsoWanting(t *testing.T) {
	sender := &mockFleetSender{}
	other := peer.ID("some-fleet-peer")
	cfg := DefaultConfig()
	c := New(cfg, &mockPeerProvider{peers: []peer.AddrInfo{{ID: other}}},
		&mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())
	defer func() { _ = c.Close() }()
	c.nonceFn = func() uint32 { return 777 }
	c.SetSender(sender)

	hash := "hash_nack_wanting"
	go func() {
		_, _ = c.WantPackage(context.Background(), hash, 1000)
	}()
	time.Sleep(50 * time.Millisecond) // let the pending want register

	requester := peer.ID("requesting-peer")
	c.HandleMessage(requester, Message{Type: MsgWantPackage, Hash: hash, Nonce: 42, Size: 1000})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range sender.getMessages() {
			if m.msg.Type == MsgDontHave && m.peerID == requester {
				if m.msg.Offset != 1 {
					t.Errorf("DontHave Offset = %d, want 1 (also wanting)", m.msg.Offset)
				}
				if m.msg.Nonce != 777 {
					t.Errorf("DontHave Nonce = %d, want our election nonce 777", m.msg.Nonce)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no DontHave reply was sent to the requester")
}
