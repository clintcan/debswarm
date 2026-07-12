package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// electionCoordinator builds a coordinator with a deterministic nonce and a
// short claim timeout, wired to a no-op sender, for election tests.
func electionCoordinator(t *testing.T, ourNonce uint32, fleetPeers []peer.AddrInfo) *Coordinator {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ClaimTimeout = 300 * time.Millisecond
	c := New(cfg, &mockPeerProvider{peers: fleetPeers}, &mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())
	c.nonceFn = func() uint32 { return ourNonce }
	c.SetSender(&mockFleetSender{})
	return c
}

// TestWantPackage_LosesElectionToLowerNonce covers the dedup race: when a peer
// is racing us for the same cold package and holds a lower nonce, we must wait
// for that peer instead of also fetching from WAN.
func TestWantPackage_LosesElectionToLowerNonce(t *testing.T) {
	competitor := peer.ID("competitor-peer")
	c := electionCoordinator(t, 500, []peer.AddrInfo{{ID: competitor}})
	defer func() { _ = c.Close() }()

	hash := "hash_lose_election"
	resultChan := make(chan *WantResult, 1)
	go func() {
		result, _ := c.WantPackage(context.Background(), hash, 1000)
		resultChan <- result
	}()

	// Let WantPackage register its pending want and broadcast, then inject a
	// competing want with a lower nonce (the competitor wins the election).
	time.Sleep(50 * time.Millisecond)
	c.HandleMessage(competitor, Message{Type: MsgWantPackage, Hash: hash, Nonce: 100, Size: 1000})

	select {
	case result := <-resultChan:
		if result.Action != ActionWaitPeer {
			t.Fatalf("expected ActionWaitPeer (lost election), got %v", result.Action)
		}
		if result.Provider != competitor {
			t.Errorf("expected to wait on %v, got %v", competitor, result.Provider)
		}
		if result.WaitChan == nil {
			t.Error("expected non-nil WaitChan")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WantPackage did not return in time")
	}
}

// TestWantPackage_WinsElectionWithLowerNonce covers the other side: a competing
// wanter with a higher nonce loses, so we proceed to fetch from WAN ourselves.
func TestWantPackage_WinsElectionWithLowerNonce(t *testing.T) {
	competitor := peer.ID("competitor-peer")
	c := electionCoordinator(t, 100, []peer.AddrInfo{{ID: competitor}})
	defer func() { _ = c.Close() }()

	hash := "hash_win_election"
	resultChan := make(chan *WantResult, 1)
	go func() {
		result, _ := c.WantPackage(context.Background(), hash, 1000)
		resultChan <- result
	}()

	time.Sleep(50 * time.Millisecond)
	// Competitor has a higher nonce — we win and should fetch from WAN.
	c.HandleMessage(competitor, Message{Type: MsgWantPackage, Hash: hash, Nonce: 500, Size: 1000})

	select {
	case result := <-resultChan:
		if result.Action != ActionFetchWAN {
			t.Fatalf("expected ActionFetchWAN (won election), got %v", result.Action)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WantPackage did not return in time")
	}
}

// TestWantPackage_WaitsOnGlobalLowestNonce ensures that with three or more
// racers we wait on the globally lowest nonce, not merely the last-seen or an
// intermediate one — this is what prevents chained waits (and stalls) when
// several nodes want the same package at once.
func TestWantPackage_WaitsOnGlobalLowestNonce(t *testing.T) {
	peerMid := peer.ID("peer-mid")   // nonce 300
	peerLow := peer.ID("peer-low")   // nonce 100 (global lowest -> winner)
	peerHigh := peer.ID("peer-high") // nonce 400
	fleetPeers := []peer.AddrInfo{{ID: peerMid}, {ID: peerLow}, {ID: peerHigh}}
	c := electionCoordinator(t, 500, fleetPeers)
	defer func() { _ = c.Close() }()

	hash := "hash_global_lowest"
	resultChan := make(chan *WantResult, 1)
	go func() {
		result, _ := c.WantPackage(context.Background(), hash, 1000)
		resultChan <- result
	}()

	time.Sleep(50 * time.Millisecond)
	// Inject in an order that would trip a naive "last one wins" or "first one
	// wins" implementation: mid, then low, then high.
	c.HandleMessage(peerMid, Message{Type: MsgWantPackage, Hash: hash, Nonce: 300, Size: 1000})
	c.HandleMessage(peerLow, Message{Type: MsgWantPackage, Hash: hash, Nonce: 100, Size: 1000})
	c.HandleMessage(peerHigh, Message{Type: MsgWantPackage, Hash: hash, Nonce: 400, Size: 1000})

	select {
	case result := <-resultChan:
		if result.Action != ActionWaitPeer {
			t.Fatalf("expected ActionWaitPeer, got %v", result.Action)
		}
		if result.Provider != peerLow {
			t.Errorf("expected to wait on lowest-nonce peer %v, got %v", peerLow, result.Provider)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WantPackage did not return in time")
	}
}

// TestNotifyFetching_PreservesWaiters covers the waiter-loss bug: NotifyFetching
// must update an existing in-flight entry in place rather than overwriting it,
// so callers already queued as waiters are released on completion instead of
// being stranded until the MaxWaitTime backstop.
func TestNotifyFetching_PreservesWaiters(t *testing.T) {
	c := New(nil, &mockPeerProvider{}, &mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())
	defer func() { _ = c.Close() }()
	c.SetSender(&mockFleetSender{})

	hash := "hash_preserve_waiters"

	// Simulate an in-flight entry that already has a queued waiter (as when
	// WantPackage records this node as fetcher and other local callers append
	// themselves before NotifyFetching runs).
	waitCh := make(chan error, 1)
	c.mu.Lock()
	c.inFlight[hash] = &FetchState{
		Hash:    hash,
		Fetcher: peer.ID(""),
		Waiters: []chan error{waitCh},
	}
	c.mu.Unlock()

	c.NotifyFetching(hash, 1000)

	c.mu.RLock()
	gotWaiters := len(c.inFlight[hash].Waiters)
	c.mu.RUnlock()
	if gotWaiters != 1 {
		t.Fatalf("expected the queued waiter to be preserved, got %d", gotWaiters)
	}

	// Completing the fetch must release the preserved waiter.
	c.NotifyComplete(hash)
	select {
	case err := <-waitCh:
		if err != nil {
			t.Errorf("expected nil error on completion, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter was stranded — NotifyFetching dropped it")
	}
}
