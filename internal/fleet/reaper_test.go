package fleet

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// TestReapStale_ReleasesWaitersAndDropsEntry verifies the reaper drops an
// in-flight entry whose peer fetcher has gone silent and releases any waiters so
// they fall back to their own download instead of waiting the full wait cap.
func TestReapStale_ReleasesWaitersAndDropsEntry(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StaleTimeout = 50 * time.Millisecond
	c := New(cfg, &mockPeerProvider{}, &mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())
	defer func() { _ = c.Close() }()

	hash := "hash_stale_peer_fetch"
	waitCh := make(chan error, 1)

	// A peer was recorded as fetcher but has gone silent (LastUpdate older than
	// StaleTimeout), with a local caller waiting on it.
	c.mu.Lock()
	c.inFlight[hash] = &FetchState{
		Hash:       hash,
		Fetcher:    peer.ID("dead-peer"),
		LastUpdate: time.Now().Add(-time.Second),
		Waiters:    []chan error{waitCh},
	}
	c.mu.Unlock()

	c.reapStale()

	if c.GetInFlightCount() != 0 {
		t.Errorf("expected stale entry to be reaped, got %d in-flight", c.GetInFlightCount())
	}
	select {
	case err := <-waitCh:
		if err != ErrPeerFetchFailed {
			t.Errorf("expected ErrPeerFetchFailed, got %v", err)
		}
	default:
		t.Fatal("waiter was not released by the reaper")
	}
}

// TestReapStale_KeepsFreshAndSelfEntries verifies the reaper leaves alone a
// fresh peer fetch and never touches our own in-flight downloads (Fetcher == ""),
// even old ones, since those are managed by the download lifecycle and Notify*.
func TestReapStale_KeepsFreshAndSelfEntries(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StaleTimeout = 50 * time.Millisecond
	c := New(cfg, &mockPeerProvider{}, &mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())
	defer func() { _ = c.Close() }()

	c.mu.Lock()
	// Fresh peer fetch — must be kept.
	c.inFlight["fresh"] = &FetchState{Hash: "fresh", Fetcher: peer.ID("live-peer"), LastUpdate: time.Now()}
	// Our own (self) fetch, even if old — must be kept.
	c.inFlight["self"] = &FetchState{Hash: "self", Fetcher: peer.ID(""), LastUpdate: time.Now().Add(-time.Hour)}
	c.mu.Unlock()

	c.reapStale()

	if c.GetInFlightCount() != 2 {
		t.Errorf("expected fresh and self entries to be kept, got %d in-flight", c.GetInFlightCount())
	}
}

// TestReaper_GoroutineReapsAndStops verifies the background reaper goroutine
// started by New actually reaps a stale peer entry on its own, and that Close
// stops it cleanly (Close waits on the waitgroup, so a leaked goroutine hangs).
func TestReaper_GoroutineReapsAndStops(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StaleTimeout = 20 * time.Millisecond // reaper ticks every ~1s (clamped)
	c := New(cfg, &mockPeerProvider{}, &mockCacheChecker{hashes: make(map[string]bool)}, zap.NewNop())

	c.mu.Lock()
	c.inFlight["stale"] = &FetchState{Hash: "stale", Fetcher: peer.ID("dead"), LastUpdate: time.Now().Add(-time.Minute)}
	c.mu.Unlock()

	// The reaper ticks at max(StaleTimeout/2, 1s) = 1s; allow a couple ticks.
	deadline := time.Now().Add(3 * time.Second)
	for c.GetInFlightCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if c.GetInFlightCount() != 0 {
		t.Fatal("background reaper did not drop the stale entry")
	}

	// Close must return promptly (reaper honors ctx cancellation).
	done := make(chan struct{})
	go func() { _ = c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return — reaper goroutine likely leaked")
	}
}
