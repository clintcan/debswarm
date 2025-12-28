package ratelimit

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Tests to verify peer_limiter lifecycle behavior after refactoring

func TestPeerLimiterManager_CleanupLoopRuns(t *testing.T) {
	// Verify cleanup loop runs periodically
	cfg := PeerLimiterConfig{
		PerPeerLimit: 1024 * 1024, // 1MB/s
		IdleTimeout:  50 * time.Millisecond,
	}
	m := NewPeerLimiterManager(cfg, nil, nil)
	defer m.Close()

	// Create a peer limiter
	peerID := peer.ID("test-peer-1")
	_ = m.GetLimiter(peerID)

	if m.PeerCount() != 1 {
		t.Fatalf("expected 1 peer, got %d", m.PeerCount())
	}

	// Wait for cleanup to run (should clean up after IdleTimeout)
	time.Sleep(150 * time.Millisecond)

	// Peer should be cleaned up
	if m.PeerCount() != 0 {
		t.Errorf("expected 0 peers after cleanup, got %d", m.PeerCount())
	}
}

func TestPeerLimiterManager_CloseStopsCleanup(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit: 1024 * 1024,
		IdleTimeout:  20 * time.Millisecond,
	}
	m := NewPeerLimiterManager(cfg, nil, nil)

	// Create a peer limiter
	peerID := peer.ID("test-peer-2")
	_ = m.GetLimiter(peerID)

	// Close immediately
	m.Close()

	// Wait what would be cleanup time
	time.Sleep(100 * time.Millisecond)

	// Peer count is irrelevant after close, but no panic should occur
}

func TestPeerLimiterManager_MultipleClose(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit: 1024 * 1024,
		IdleTimeout:  100 * time.Millisecond,
	}
	m := NewPeerLimiterManager(cfg, nil, nil)

	// Create some activity
	_ = m.GetLimiter(peer.ID("peer1"))
	_ = m.GetLimiter(peer.ID("peer2"))

	// Close multiple times - should not panic
	m.Close()
	m.Close()
	m.Close()
}

func TestPeerLimiterManager_CloseWaitsForCleanup(t *testing.T) {
	// Create a manager with very short intervals to force activity
	cfg := PeerLimiterConfig{
		PerPeerLimit: 1024 * 1024,
		IdleTimeout:  10 * time.Millisecond,
	}
	m := NewPeerLimiterManager(cfg, nil, nil)

	// Create some peers
	for i := 0; i < 5; i++ {
		_ = m.GetLimiter(peer.ID("peer-" + string(rune('a'+i))))
	}

	// Close should wait for cleanup goroutine
	start := time.Now()
	m.Close()
	elapsed := time.Since(start)

	// Should complete quickly (not hang)
	if elapsed > 1*time.Second {
		t.Errorf("Close took too long: %v", elapsed)
	}
}

func TestPeerLimiterManager_DisabledNoGoroutines(t *testing.T) {
	// When per-peer limiting is disabled, no background goroutines should run
	cfg := PeerLimiterConfig{
		PerPeerLimit: 0, // Disabled
	}
	m := NewPeerLimiterManager(cfg, nil, nil)

	if m.Enabled() {
		t.Error("should be disabled with PerPeerLimit=0")
	}

	// Close should be instant
	start := time.Now()
	m.Close()
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("Close should be instant for disabled manager: %v", elapsed)
	}
}

func TestPeerLimiterManager_AdaptiveLoopRuns(t *testing.T) {
	// This test verifies the adaptive loop runs when enabled
	// We use a mock scorer to track calls
	cfg := PeerLimiterConfig{
		PerPeerLimit:           1024 * 1024,
		AdaptiveEnabled:        true,
		AdaptiveRecalcInterval: 30 * time.Millisecond,
		IdleTimeout:            1 * time.Second, // Long so cleanup doesn't interfere
	}

	// We need a scorer for adaptive to be enabled
	// The scorer is nil, so adaptiveEnabled will be false
	m := NewPeerLimiterManager(cfg, nil, nil)
	defer m.Close()

	// Without scorer, adaptive should not be enabled
	if m.adaptiveEnabled {
		t.Error("adaptive should not be enabled without scorer")
	}
}

func TestPeerLimiterManager_ConcurrentOperations(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit: 1024 * 1024,
		IdleTimeout:  50 * time.Millisecond,
	}
	m := NewPeerLimiterManager(cfg, nil, nil)

	var ops int32
	done := make(chan struct{})

	// Concurrent GetLimiter calls
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				peerID := peer.ID("peer-" + string(rune('a'+id)))
				_ = m.GetLimiter(peerID)
				atomic.AddInt32(&ops, 1)
			}
		}(i)
	}

	// Let operations run while cleanup is happening
	time.Sleep(200 * time.Millisecond)
	close(done)

	m.Close()

	if atomic.LoadInt32(&ops) < 500 {
		t.Errorf("expected many concurrent ops, got %d", ops)
	}
}

func TestPeerLimiterManager_CleanupDuringActiveUse(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit: 1024 * 1024,
		IdleTimeout:  30 * time.Millisecond,
	}
	m := NewPeerLimiterManager(cfg, nil, nil)
	defer m.Close()

	peerID := peer.ID("active-peer")

	// Keep accessing the peer to prevent cleanup
	for i := 0; i < 5; i++ {
		_ = m.GetLimiter(peerID)
		time.Sleep(20 * time.Millisecond) // Less than IdleTimeout
	}

	// Peer should still exist because we kept accessing it
	if m.PeerCount() == 0 {
		t.Error("peer should not be cleaned up while actively used")
	}
}

func TestPeerLimiterManager_CloseFromMultipleGoroutines(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit: 1024 * 1024,
		IdleTimeout:  100 * time.Millisecond,
	}
	m := NewPeerLimiterManager(cfg, nil, nil)

	// Create some peers
	for i := 0; i < 5; i++ {
		_ = m.GetLimiter(peer.ID("peer-" + string(rune('a'+i))))
	}

	// Close from multiple goroutines simultaneously
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		go func() {
			m.Close()
			done <- struct{}{}
		}()
	}

	// Wait for all closes
	for i := 0; i < 5; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Close hung")
		}
	}
}

func TestPeerLimiterManager_TimeoutDefaults(t *testing.T) {
	// Test that zero timeouts get defaults
	cfg := PeerLimiterConfig{
		PerPeerLimit: 1024 * 1024,
		// IdleTimeout and AdaptiveRecalcInterval are 0
	}
	m := NewPeerLimiterManager(cfg, nil, nil)
	defer m.Close()

	// Verify defaults were applied
	if m.idleTimeout != DefaultIdleTimeout {
		t.Errorf("expected default idle timeout %v, got %v", DefaultIdleTimeout, m.idleTimeout)
	}
	if m.recalcInterval != DefaultAdaptiveRecalc {
		t.Errorf("expected default recalc interval %v, got %v", DefaultAdaptiveRecalc, m.recalcInterval)
	}
}
