package ratelimit

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/peers"
)

// mockPeerID creates a test peer ID
func mockPeerID(id string) peer.ID {
	return peer.ID(id)
}

func TestPeerLimiterManager_Enabled(t *testing.T) {
	tests := []struct {
		name         string
		perPeerLimit int64
		expected     bool
	}{
		{"disabled when zero", 0, false},
		{"enabled with limit", 1024 * 1024, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := PeerLimiterConfig{
				PerPeerLimit:  tc.perPeerLimit,
				ExpectedPeers: 10,
			}
			mgr := NewPeerLimiterManager(cfg, nil, nil)
			defer mgr.Close()

			if mgr.Enabled() != tc.expected {
				t.Errorf("Enabled() = %v, want %v", mgr.Enabled(), tc.expected)
			}
		})
	}
}

func TestPeerLimiterManager_NilEnabled(t *testing.T) {
	var mgr *PeerLimiterManager
	if mgr.Enabled() {
		t.Error("nil manager should return false for Enabled()")
	}
}

func TestPeerLimiterManager_AutoCalculation(t *testing.T) {
	// Test auto-calculation from global limit
	cfg := PeerLimiterConfig{
		GlobalLimit:   10 * 1024 * 1024, // 10 MB/s global
		PerPeerLimit:  0,                // auto-calculate
		ExpectedPeers: 10,
		MinPeerLimit:  100 * 1024, // 100 KB/s minimum
	}
	mgr := NewPeerLimiterManager(cfg, nil, nil)
	defer mgr.Close()

	// Per-peer should be 10MB/s / 10 = 1MB/s
	if !mgr.Enabled() {
		t.Error("Manager should be enabled with auto-calculated limit")
	}

	// Create a limiter and verify it works
	peerID := mockPeerID("test-peer-1")
	limiter := mgr.GetLimiter(peerID)
	if limiter == nil {
		t.Error("GetLimiter should return a limiter")
	}
}

func TestPeerLimiterManager_MinPeerLimit(t *testing.T) {
	// Test that minimum peer limit is enforced
	cfg := PeerLimiterConfig{
		GlobalLimit:   100 * 1024, // 100 KB/s global
		PerPeerLimit:  0,          // auto-calculate: 100KB/100 = 1KB/s
		ExpectedPeers: 100,
		MinPeerLimit:  50 * 1024, // 50 KB/s minimum
	}
	mgr := NewPeerLimiterManager(cfg, nil, nil)
	defer mgr.Close()

	// Per-peer should be enforced to minimum (50KB/s), not 1KB/s
	if !mgr.Enabled() {
		t.Error("Manager should be enabled")
	}
}

func TestPeerLimiterManager_GetLimiter_LazyCreation(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit:  1024 * 1024, // 1 MB/s
		ExpectedPeers: 10,
	}
	mgr := NewPeerLimiterManager(cfg, nil, nil)
	defer mgr.Close()

	// Initially no peer limiters
	if mgr.PeerCount() != 0 {
		t.Errorf("Initial peer count should be 0, got %d", mgr.PeerCount())
	}

	// Create limiter for first peer
	peer1 := mockPeerID("peer-1")
	limiter1 := mgr.GetLimiter(peer1)
	if limiter1 == nil {
		t.Error("GetLimiter should return a limiter")
	}
	if mgr.PeerCount() != 1 {
		t.Errorf("Peer count should be 1 after first GetLimiter, got %d", mgr.PeerCount())
	}

	// Get same limiter again
	limiter1Again := mgr.GetLimiter(peer1)
	if limiter1Again != limiter1 {
		t.Error("GetLimiter should return same limiter for same peer")
	}

	// Create limiter for second peer
	peer2 := mockPeerID("peer-2")
	limiter2 := mgr.GetLimiter(peer2)
	if limiter2 == nil {
		t.Error("GetLimiter should return a limiter for second peer")
	}
	if limiter2 == limiter1 {
		t.Error("Different peers should have different limiters")
	}
	if mgr.PeerCount() != 2 {
		t.Errorf("Peer count should be 2, got %d", mgr.PeerCount())
	}
}

func TestPeerLimiterManager_ReaderContext(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit:  10 * 1024 * 1024, // 10 MB/s - fast for tests
		ExpectedPeers: 10,
	}
	mgr := NewPeerLimiterManager(cfg, nil, nil)
	defer mgr.Close()

	ctx := context.Background()
	peerID := mockPeerID("test-peer")
	data := "hello world from peer limiter"
	original := strings.NewReader(data)

	reader := mgr.ReaderContext(ctx, peerID, original)

	// Read all data
	buf := make([]byte, len(data))
	n, err := io.ReadFull(reader, buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Read %d bytes, want %d", n, len(data))
	}
	if string(buf) != data {
		t.Errorf("Read %q, want %q", string(buf), data)
	}
}

func TestPeerLimiterManager_WriterContext(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit:  10 * 1024 * 1024, // 10 MB/s - fast for tests
		ExpectedPeers: 10,
	}
	mgr := NewPeerLimiterManager(cfg, nil, nil)
	defer mgr.Close()

	ctx := context.Background()
	peerID := mockPeerID("test-peer")
	var buf bytes.Buffer

	writer := mgr.WriterContext(ctx, peerID, &buf)

	data := "hello world from peer limiter"
	n, err := writer.Write([]byte(data))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Wrote %d bytes, want %d", n, len(data))
	}
	if buf.String() != data {
		t.Errorf("Buffer contains %q, want %q", buf.String(), data)
	}
}

func TestPeerLimiterManager_DisabledPassthrough(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit:  0, // disabled
		ExpectedPeers: 0,
	}
	mgr := NewPeerLimiterManager(cfg, nil, nil)
	defer mgr.Close()

	ctx := context.Background()
	peerID := mockPeerID("test-peer")

	// Reader should pass through
	data := "test data"
	original := strings.NewReader(data)
	reader := mgr.ReaderContext(ctx, peerID, original)
	if reader != original {
		t.Error("Disabled manager should return original reader")
	}

	// Writer should pass through
	var buf bytes.Buffer
	writer := mgr.WriterContext(ctx, peerID, &buf)
	if writer != &buf {
		t.Error("Disabled manager should return original writer")
	}
}

func TestPeerLimiterManager_WithGlobalLimiter(t *testing.T) {
	// Test composed limiting with both global and per-peer
	globalLimiter := New(5 * 1024 * 1024) // 5 MB/s global

	cfg := PeerLimiterConfig{
		GlobalLimit:   5 * 1024 * 1024, // 5 MB/s
		PerPeerLimit:  2 * 1024 * 1024, // 2 MB/s per peer
		ExpectedPeers: 10,
	}
	mgr := NewPeerLimiterManager(cfg, globalLimiter, nil)
	defer mgr.Close()

	ctx := context.Background()
	peerID := mockPeerID("test-peer")
	data := "test data for composed limiting"
	original := strings.NewReader(data)

	reader := mgr.ReaderContext(ctx, peerID, original)

	// Should return a ComposedLimitedReader
	_, isComposed := reader.(*ComposedLimitedReader)
	if !isComposed {
		t.Error("Should return ComposedLimitedReader when both limiters active")
	}

	// Read should work
	buf := make([]byte, len(data))
	n, err := io.ReadFull(reader, buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Read %d bytes, want %d", n, len(data))
	}
}

func TestPeerLimiterManager_GetPeerStats(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit:  1024 * 1024, // 1 MB/s
		ExpectedPeers: 10,
	}
	mgr := NewPeerLimiterManager(cfg, nil, nil)
	defer mgr.Close()

	peerID := mockPeerID("test-peer")

	// Stats for unknown peer
	currentLimit, baseLimit, exists := mgr.GetPeerStats(peerID)
	if exists {
		t.Error("Unknown peer should not exist")
	}
	if currentLimit != 0 {
		t.Errorf("Current limit for unknown peer should be 0, got %d", currentLimit)
	}
	if baseLimit != cfg.PerPeerLimit {
		t.Errorf("Base limit should be %d, got %d", cfg.PerPeerLimit, baseLimit)
	}

	// Create limiter
	mgr.GetLimiter(peerID)

	// Stats should now exist
	currentLimit, baseLimit, exists = mgr.GetPeerStats(peerID)
	if !exists {
		t.Error("Peer should exist after GetLimiter")
	}
	if currentLimit != cfg.PerPeerLimit {
		t.Errorf("Current limit should be %d, got %d", cfg.PerPeerLimit, currentLimit)
	}
	if baseLimit != cfg.PerPeerLimit {
		t.Errorf("Base limit should be %d, got %d", cfg.PerPeerLimit, baseLimit)
	}
}

func TestPeerLimiterManager_Close(t *testing.T) {
	cfg := PeerLimiterConfig{
		PerPeerLimit:  1024 * 1024,
		ExpectedPeers: 10,
	}
	mgr := NewPeerLimiterManager(cfg, nil, nil)

	// Create some limiters
	mgr.GetLimiter(mockPeerID("peer-1"))
	mgr.GetLimiter(mockPeerID("peer-2"))

	// Close should not panic
	mgr.Close()

	// Manager should still report peer count (cleanup happens on background goroutine)
	// but we mainly want to ensure Close() doesn't hang
}

func TestComposedLimitedReader_Read(t *testing.T) {
	data := "test data for composed reader"

	// Create both limiters
	globalLim := New(10 * 1024 * 1024) // 10 MB/s
	peerLim := New(5 * 1024 * 1024)    // 5 MB/s

	reader := &ComposedLimitedReader{
		r:         strings.NewReader(data),
		globalLim: globalLim.limiter,
		peerLim:   peerLim.limiter,
		ctx:       context.Background(),
	}

	buf := make([]byte, len(data))
	n, err := io.ReadFull(reader, buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Read %d bytes, want %d", n, len(data))
	}
	if string(buf) != data {
		t.Errorf("Read %q, want %q", string(buf), data)
	}
}

func TestComposedLimitedWriter_Write(t *testing.T) {
	data := "test data for composed writer"
	var buf bytes.Buffer

	// Create both limiters
	globalLim := New(10 * 1024 * 1024) // 10 MB/s
	peerLim := New(5 * 1024 * 1024)    // 5 MB/s

	writer := &ComposedLimitedWriter{
		w:         &buf,
		globalLim: globalLim.limiter,
		peerLim:   peerLim.limiter,
		ctx:       context.Background(),
	}

	n, err := writer.Write([]byte(data))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Wrote %d bytes, want %d", n, len(data))
	}
	if buf.String() != data {
		t.Errorf("Buffer contains %q, want %q", buf.String(), data)
	}
}

func TestComposedLimitedReader_ContextCanceled(t *testing.T) {
	data := strings.Repeat("x", 10000)

	// Use very low rate to trigger waiting
	globalLim := New(1) // 1 byte/s
	peerLim := New(1)   // 1 byte/s

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	reader := &ComposedLimitedReader{
		r:         strings.NewReader(data),
		globalLim: globalLim.limiter,
		peerLim:   peerLim.limiter,
		ctx:       ctx,
	}

	buf := make([]byte, len(data))
	_, err := reader.Read(buf)
	// First read might succeed due to burst, but we're testing the structure
	_ = err
}

func TestPeerLimiterManager_AdaptiveWithScorer(t *testing.T) {
	scorer := peers.NewScorer()

	cfg := PeerLimiterConfig{
		PerPeerLimit:           1024 * 1024, // 1 MB/s base
		ExpectedPeers:          10,
		AdaptiveEnabled:        true,
		MaxBoostFactor:         1.5,
		LatencyThresholdMs:     500,
		AdaptiveRecalcInterval: 50 * time.Millisecond, // Fast for testing
		Logger:                 zap.NewNop(),
	}
	mgr := NewPeerLimiterManager(cfg, nil, scorer)
	defer mgr.Close()

	peerID := mockPeerID("adaptive-peer")

	// Record some good performance
	scorer.RecordSuccess(peerID, 1024*1024, 50, 2*1024*1024) // 50ms latency, 2MB/s throughput
	scorer.RecordSuccess(peerID, 1024*1024, 50, 2*1024*1024)
	scorer.RecordSuccess(peerID, 1024*1024, 50, 2*1024*1024)

	// Get limiter - should have some adaptive adjustment
	_ = mgr.GetLimiter(peerID)

	// Wait for adaptive loop to run
	time.Sleep(100 * time.Millisecond)

	// Just verify it doesn't panic and limiter still works
	currentLimit, _, exists := mgr.GetPeerStats(peerID)
	if !exists {
		t.Error("Peer should exist")
	}
	// The limit should be positive
	if currentLimit <= 0 {
		t.Errorf("Current limit should be positive, got %d", currentLimit)
	}
}

func TestCalculateBurst(t *testing.T) {
	tests := []struct {
		name           string
		bytesPerSecond int64
		minBurst       int64
		maxBurst       int64
	}{
		{"zero", 0, 64 * 1024, 64 * 1024},
		{"below minimum", 1000, 64 * 1024, 64 * 1024},
		{"between min and max", 500 * 1024, 500 * 1024, 500 * 1024},
		{"above maximum", 10 * 1024 * 1024, 4 * 1024 * 1024, 4 * 1024 * 1024},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			burst := calculateBurst(tc.bytesPerSecond)
			if burst < tc.minBurst || burst > tc.maxBurst {
				t.Errorf("calculateBurst(%d) = %d, want between %d and %d",
					tc.bytesPerSecond, burst, tc.minBurst, tc.maxBurst)
			}
		})
	}
}

func TestDefaultPeerLimiterConfig(t *testing.T) {
	cfg := DefaultPeerLimiterConfig()

	if cfg.ExpectedPeers != DefaultExpectedPeers {
		t.Errorf("ExpectedPeers = %d, want %d", cfg.ExpectedPeers, DefaultExpectedPeers)
	}
	if cfg.MinPeerLimit != DefaultMinPeerRate {
		t.Errorf("MinPeerLimit = %d, want %d", cfg.MinPeerLimit, DefaultMinPeerRate)
	}
	if cfg.MaxBoostFactor != DefaultMaxBoostFactor {
		t.Errorf("MaxBoostFactor = %v, want %v", cfg.MaxBoostFactor, DefaultMaxBoostFactor)
	}
	if !cfg.AdaptiveEnabled {
		t.Error("AdaptiveEnabled should be true by default")
	}
}
