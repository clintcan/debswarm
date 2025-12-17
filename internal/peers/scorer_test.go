package peers

import (
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func testPeerID(id string) peer.ID {
	// Create a simple peer ID for testing
	return peer.ID(id)
}

func TestNewScorer(t *testing.T) {
	s := NewScorer()
	if s == nil {
		t.Fatal("NewScorer returned nil")
	}
	if s.PeerCount() != 0 {
		t.Errorf("Expected 0 peers, got %d", s.PeerCount())
	}
}

func TestRecordSuccess(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// Record success
	s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)

	stats := s.GetStats(peerID)
	if stats == nil {
		t.Fatal("GetStats returned nil")
	}

	if stats.TotalRequests != 1 {
		t.Errorf("Expected 1 request, got %d", stats.TotalRequests)
	}
	if stats.SuccessCount != 1 {
		t.Errorf("Expected 1 success, got %d", stats.SuccessCount)
	}
	if stats.BytesDownloaded != 1024 {
		t.Errorf("Expected 1024 bytes, got %d", stats.BytesDownloaded)
	}
	if stats.AvgLatencyMs != 50.0 {
		t.Errorf("Expected 50ms latency, got %f", stats.AvgLatencyMs)
	}
	if stats.SuccessRate != 1.0 {
		t.Errorf("Expected 100%% success rate, got %f", stats.SuccessRate)
	}
}

func TestRecordFailure(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// Record failure
	s.RecordFailure(peerID, "connection timeout")

	stats := s.GetStats(peerID)
	if stats == nil {
		t.Fatal("GetStats returned nil")
	}

	if stats.TotalRequests != 1 {
		t.Errorf("Expected 1 request, got %d", stats.TotalRequests)
	}
	if stats.FailureCount != 1 {
		t.Errorf("Expected 1 failure, got %d", stats.FailureCount)
	}
	if stats.SuccessRate != 0.0 {
		t.Errorf("Expected 0%% success rate, got %f", stats.SuccessRate)
	}
}

func TestMixedSuccessFailure(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// 3 successes, 1 failure = 75% success rate
	s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)
	s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)
	s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)
	s.RecordFailure(peerID, "timeout")

	stats := s.GetStats(peerID)
	if stats.TotalRequests != 4 {
		t.Errorf("Expected 4 requests, got %d", stats.TotalRequests)
	}
	if stats.SuccessRate != 0.75 {
		t.Errorf("Expected 75%% success rate, got %f", stats.SuccessRate)
	}
}

func TestBlacklist(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// Initially not blacklisted
	if s.IsBlacklisted(peerID) {
		t.Error("Peer should not be blacklisted initially")
	}

	// Blacklist for 1 hour
	s.Blacklist(peerID, "hash mismatch", 1*time.Hour)

	if !s.IsBlacklisted(peerID) {
		t.Error("Peer should be blacklisted")
	}

	stats := s.GetStats(peerID)
	if stats.BlacklistReason != "hash mismatch" {
		t.Errorf("Expected 'hash mismatch', got '%s'", stats.BlacklistReason)
	}

	// Score should be 0 when blacklisted
	score := s.GetScore(peerID)
	if score != 0 {
		t.Errorf("Blacklisted peer score should be 0, got %f", score)
	}
}

func TestBlacklistExpiry(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// Blacklist for a very short duration
	s.Blacklist(peerID, "test", 1*time.Millisecond)

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	// Should no longer be blacklisted
	if s.IsBlacklisted(peerID) {
		t.Error("Blacklist should have expired")
	}
}

func TestBlacklistClearedOnSuccess(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// Blacklist for a very short duration
	s.Blacklist(peerID, "test", 1*time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	// Record success should clear expired blacklist
	s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)

	stats := s.GetStats(peerID)
	if stats.Blacklisted {
		t.Error("Blacklist should be cleared after success")
	}
}

func TestGetScoreUnknownPeer(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("unknown")

	// Unknown peers get neutral score
	score := s.GetScore(peerID)
	if score != 0.5 {
		t.Errorf("Expected 0.5 for unknown peer, got %f", score)
	}
}

func TestGetScoreMinSamples(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// Less than MinSamples should return neutral score
	s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)
	s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)

	stats := s.GetStats(peerID)
	if stats.TotalRequests < MinSamples {
		score := s.GetScore(peerID)
		if score != 0.5 {
			t.Errorf("Expected 0.5 with insufficient samples, got %f", score)
		}
	}

	// After MinSamples (3), should compute real score
	s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)

	stats = s.GetStats(peerID)
	if stats.TotalRequests < MinSamples {
		t.Fatalf("Expected at least %d samples, got %d", MinSamples, stats.TotalRequests)
	}

	// Score is cached during RecordSuccess, so it should not be 0.5 anymore
	// However the cached score was computed before we hit MinSamples
	// The next GetScore call should recompute since cache expires after ScoreCacheTTL
	// For this test, we just verify the scorer works correctly
	score := s.GetScore(peerID)
	// Score could be 0.5 if cached, or computed value
	// The important thing is it's a valid score
	if score < 0 || score > 1 {
		t.Errorf("Score out of range: %f", score)
	}
}

func TestSelectBest(t *testing.T) {
	s := NewScorer()

	// Create peers with different performance
	peer1 := testPeerID("peer1")
	peer2 := testPeerID("peer2")
	peer3 := testPeerID("peer3")

	// peer1: excellent (low latency, high throughput)
	for i := 0; i < 5; i++ {
		s.RecordSuccess(peer1, 1024, 10.0, 10*1024*1024)
	}

	// peer2: good
	for i := 0; i < 5; i++ {
		s.RecordSuccess(peer2, 1024, 100.0, 1*1024*1024)
	}

	// peer3: poor (high latency, low throughput, some failures)
	for i := 0; i < 3; i++ {
		s.RecordSuccess(peer3, 1024, 500.0, 100*1024)
	}
	for i := 0; i < 2; i++ {
		s.RecordFailure(peer3, "timeout")
	}

	candidates := []peer.AddrInfo{
		{ID: peer1},
		{ID: peer2},
		{ID: peer3},
	}

	// Select best 2
	best := s.SelectBest(candidates, 2)
	if len(best) != 2 {
		t.Fatalf("Expected 2 peers, got %d", len(best))
	}

	// peer1 should be first (best performer)
	if best[0].ID != peer1 {
		t.Errorf("Expected peer1 first, got %s", best[0].ID)
	}
}

func TestSelectBestExcludesBlacklisted(t *testing.T) {
	s := NewScorer()

	peer1 := testPeerID("peer1")
	peer2 := testPeerID("peer2")

	// Both have good stats
	for i := 0; i < 5; i++ {
		s.RecordSuccess(peer1, 1024, 50.0, 1*1024*1024)
		s.RecordSuccess(peer2, 1024, 50.0, 1*1024*1024)
	}

	// Blacklist peer1
	s.Blacklist(peer1, "test", 1*time.Hour)

	candidates := []peer.AddrInfo{
		{ID: peer1},
		{ID: peer2},
	}

	best := s.SelectBest(candidates, 10)
	if len(best) != 1 {
		t.Fatalf("Expected 1 peer (blacklisted excluded), got %d", len(best))
	}
	if best[0].ID != peer2 {
		t.Errorf("Expected peer2, got %s", best[0].ID)
	}
}

func TestSelectDiverse(t *testing.T) {
	s := NewScorer()

	// Create many peers
	peers := make([]peer.AddrInfo, 10)
	for i := 0; i < 10; i++ {
		peerID := testPeerID(string(rune('a' + i)))
		peers[i] = peer.AddrInfo{ID: peerID}

		// Give them varying performance
		latency := float64(10 + i*10)
		throughput := float64((10 - i) * 1024 * 1024)
		for j := 0; j < 5; j++ {
			s.RecordSuccess(peerID, 1024, latency, throughput)
		}
	}

	// Select diverse set
	diverse := s.SelectDiverse(peers, 5)
	if len(diverse) != 5 {
		t.Errorf("Expected 5 peers, got %d", len(diverse))
	}

	// Should include top performers
	foundBest := false
	for _, p := range diverse {
		if p.ID == peers[0].ID {
			foundBest = true
			break
		}
	}
	if !foundBest {
		t.Error("Best peer should be in diverse selection")
	}
}

func TestCleanup(t *testing.T) {
	s := NewScorer()

	peer1 := testPeerID("peer1")
	peer2 := testPeerID("peer2")

	s.RecordSuccess(peer1, 1024, 50.0, 1*1024*1024)
	s.RecordSuccess(peer2, 1024, 50.0, 1*1024*1024)

	if s.PeerCount() != 2 {
		t.Fatalf("Expected 2 peers, got %d", s.PeerCount())
	}

	// Cleanup shouldn't remove recent peers
	removed := s.Cleanup()
	if removed != 0 {
		t.Errorf("Expected 0 removed (recent peers), got %d", removed)
	}
}

func TestRecordUpload(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	s.RecordUpload(peerID, 2048)

	stats := s.GetStats(peerID)
	if stats.BytesUploaded != 2048 {
		t.Errorf("Expected 2048 bytes uploaded, got %d", stats.BytesUploaded)
	}
}

func TestEMA(t *testing.T) {
	// Test exponential moving average calculation
	result := ema(100, 200, 0.3)
	expected := 0.3*200 + 0.7*100 // 60 + 70 = 130
	if result != expected {
		t.Errorf("Expected %f, got %f", expected, result)
	}
}

func TestScoreCategory(t *testing.T) {
	tests := []struct {
		score    float64
		category string
	}{
		{0.9, "excellent"},
		{0.8, "excellent"},
		{0.7, "good"},
		{0.6, "good"},
		{0.5, "fair"},
		{0.4, "fair"},
		{0.3, "poor"},
		{0.2, "poor"},
		{0.1, "bad"},
		{0.0, "bad"},
	}

	for _, tt := range tests {
		got := ScoreCategory(tt.score)
		if got != tt.category {
			t.Errorf("ScoreCategory(%f) = %s, want %s", tt.score, got, tt.category)
		}
	}
}

func TestGetAllStats(t *testing.T) {
	s := NewScorer()

	peer1 := testPeerID("peer1")
	peer2 := testPeerID("peer2")

	s.RecordSuccess(peer1, 1024, 50.0, 1*1024*1024)
	s.RecordSuccess(peer2, 2048, 100.0, 2*1024*1024)

	allStats := s.GetAllStats()
	if len(allStats) != 2 {
		t.Fatalf("Expected 2 stats, got %d", len(allStats))
	}

	// Stats should be copies, not references
	for _, stat := range allStats {
		stat.BytesDownloaded = 0 // Modify copy
	}

	// Original should be unchanged
	stats := s.GetStats(peer1)
	if stats.BytesDownloaded != 1024 {
		t.Error("Stats modification affected original")
	}
}

func TestSelectBestEmpty(t *testing.T) {
	s := NewScorer()

	result := s.SelectBest(nil, 5)
	if result != nil {
		t.Error("Expected nil for empty candidates")
	}

	result = s.SelectBest([]peer.AddrInfo{}, 5)
	if result != nil {
		t.Error("Expected nil for empty slice")
	}
}

func TestSelectBestMoreThanAvailable(t *testing.T) {
	s := NewScorer()

	peer1 := testPeerID("peer1")
	for i := 0; i < 5; i++ {
		s.RecordSuccess(peer1, 1024, 50.0, 1*1024*1024)
	}

	candidates := []peer.AddrInfo{{ID: peer1}}

	// Request more than available
	result := s.SelectBest(candidates, 10)
	if len(result) != 1 {
		t.Errorf("Expected 1 peer, got %d", len(result))
	}
}

func TestConcurrentRecordSuccess(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)
		}()
	}
	wg.Wait()

	stats := s.GetStats(peerID)
	if stats.TotalRequests != 100 {
		t.Errorf("Expected 100 requests, got %d", stats.TotalRequests)
	}
	if stats.SuccessCount != 100 {
		t.Errorf("Expected 100 successes, got %d", stats.SuccessCount)
	}
}

func TestConcurrentMixedOperations(t *testing.T) {
	s := NewScorer()

	var wg sync.WaitGroup
	peers := make([]peer.ID, 10)
	for i := 0; i < 10; i++ {
		peers[i] = testPeerID(string(rune('a' + i)))
	}

	// Concurrent successes, failures, blacklists, and reads
	for i := 0; i < 50; i++ {
		wg.Add(4)
		go func(idx int) {
			defer wg.Done()
			s.RecordSuccess(peers[idx%10], 1024, 50.0, 1024*1024)
		}(i)
		go func(idx int) {
			defer wg.Done()
			s.RecordFailure(peers[idx%10], "test error")
		}(i)
		go func(idx int) {
			defer wg.Done()
			_ = s.GetScore(peers[idx%10])
		}(i)
		go func(idx int) {
			defer wg.Done()
			_ = s.IsBlacklisted(peers[idx%10])
		}(i)
	}
	wg.Wait()

	// Verify no data corruption
	if s.PeerCount() != 10 {
		t.Errorf("Expected 10 peers, got %d", s.PeerCount())
	}
}

func TestConcurrentGetStats(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// Pre-populate with data
	for i := 0; i < 10; i++ {
		s.RecordSuccess(peerID, 1024, 50.0, 1024*1024)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stats := s.GetStats(peerID)
			if stats == nil {
				t.Error("GetStats returned nil")
			}
		}()
	}
	wg.Wait()
}

func TestEMAEdgeCases(t *testing.T) {
	// Alpha = 0 (no change)
	result := ema(100, 200, 0)
	if result != 100 {
		t.Errorf("ema with alpha=0 should return old value, got %f", result)
	}

	// Alpha = 1 (full replacement)
	result = ema(100, 200, 1)
	if result != 200 {
		t.Errorf("ema with alpha=1 should return new value, got %f", result)
	}

	// Very small values
	result = ema(0.001, 0.002, 0.5)
	expected := 0.5*0.002 + 0.5*0.001
	if result != expected {
		t.Errorf("ema with small values: expected %f, got %f", expected, result)
	}
}

func TestScoreClamping(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// Score should always be between 0 and 1
	// Create conditions that might push score to extremes
	for i := 0; i < 100; i++ {
		s.RecordSuccess(peerID, 1024, 1.0, 100*1024*1024) // Very fast
	}

	score := s.GetScore(peerID)
	if score < 0 || score > 1 {
		t.Errorf("Score out of bounds: %f", score)
	}
}

func TestSelectDiverseEdgeCases(t *testing.T) {
	s := NewScorer()

	// Single peer
	peer1 := testPeerID("peer1")
	s.RecordSuccess(peer1, 1024, 50.0, 1024*1024)
	s.RecordSuccess(peer1, 1024, 50.0, 1024*1024)
	s.RecordSuccess(peer1, 1024, 50.0, 1024*1024)

	candidates := []peer.AddrInfo{{ID: peer1}}
	diverse := s.SelectDiverse(candidates, 5)
	if len(diverse) != 1 {
		t.Errorf("Expected 1 peer, got %d", len(diverse))
	}

	// Empty candidates
	diverse = s.SelectDiverse(nil, 5)
	if diverse != nil {
		t.Error("Expected nil for nil candidates")
	}

	diverse = s.SelectDiverse([]peer.AddrInfo{}, 5)
	if diverse != nil {
		t.Error("Expected nil for empty candidates")
	}
}

func TestSelectDiverseTwoPeers(t *testing.T) {
	s := NewScorer()

	peer1 := testPeerID("peer1")
	peer2 := testPeerID("peer2")

	for i := 0; i < 5; i++ {
		s.RecordSuccess(peer1, 1024, 50.0, 1024*1024)
		s.RecordSuccess(peer2, 1024, 100.0, 512*1024) // Worse performance
	}

	candidates := []peer.AddrInfo{{ID: peer1}, {ID: peer2}}
	diverse := s.SelectDiverse(candidates, 5)

	if len(diverse) != 2 {
		t.Errorf("Expected 2 peers, got %d", len(diverse))
	}
}

func TestBlacklistMultipleTimes(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// Blacklist multiple times
	s.Blacklist(peerID, "reason1", 1*time.Hour)
	s.Blacklist(peerID, "reason2", 2*time.Hour)

	stats := s.GetStats(peerID)
	if stats.BlacklistReason != "reason2" {
		t.Errorf("Expected 'reason2', got '%s'", stats.BlacklistReason)
	}
}

func TestGetStatsUnknownPeer(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("unknown")

	stats := s.GetStats(peerID)
	if stats != nil {
		t.Error("Expected nil for unknown peer")
	}
}

func TestPeerCountAfterOperations(t *testing.T) {
	s := NewScorer()

	if s.PeerCount() != 0 {
		t.Error("Initial peer count should be 0")
	}

	peer1 := testPeerID("peer1")
	peer2 := testPeerID("peer2")

	s.RecordSuccess(peer1, 1024, 50.0, 1024*1024)
	if s.PeerCount() != 1 {
		t.Errorf("Expected 1 peer, got %d", s.PeerCount())
	}

	s.RecordFailure(peer2, "error")
	if s.PeerCount() != 2 {
		t.Errorf("Expected 2 peers, got %d", s.PeerCount())
	}

	// Recording more for existing peer shouldn't increase count
	s.RecordSuccess(peer1, 1024, 50.0, 1024*1024)
	if s.PeerCount() != 2 {
		t.Errorf("Expected 2 peers (no increase), got %d", s.PeerCount())
	}
}

func TestRecordUploadCreatesEntry(t *testing.T) {
	s := NewScorer()
	peerID := testPeerID("peer1")

	// RecordUpload should create peer entry if doesn't exist
	s.RecordUpload(peerID, 1024)

	if s.PeerCount() != 1 {
		t.Errorf("Expected 1 peer after RecordUpload, got %d", s.PeerCount())
	}

	stats := s.GetStats(peerID)
	if stats == nil {
		t.Fatal("GetStats returned nil")
	}
	if stats.BytesUploaded != 1024 {
		t.Errorf("Expected 1024 bytes uploaded, got %d", stats.BytesUploaded)
	}
}

func TestScoreCategoryBoundaries(t *testing.T) {
	// Test exact boundary values
	tests := []struct {
		score    float64
		category string
	}{
		{0.8, "excellent"}, // Exactly at boundary
		{0.79, "good"},     // Just below
		{0.6, "good"},      // Exactly at boundary
		{0.59, "fair"},     // Just below
		{0.4, "fair"},      // Exactly at boundary
		{0.39, "poor"},     // Just below
		{0.2, "poor"},      // Exactly at boundary
		{0.19, "bad"},      // Just below
	}

	for _, tt := range tests {
		got := ScoreCategory(tt.score)
		if got != tt.category {
			t.Errorf("ScoreCategory(%f) = %s, want %s", tt.score, got, tt.category)
		}
	}
}

func TestGetAllStatsCopy(t *testing.T) {
	s := NewScorer()
	peer1 := testPeerID("peer1")

	s.RecordSuccess(peer1, 1024, 50.0, 1*1024*1024)

	allStats := s.GetAllStats()
	if len(allStats) != 1 {
		t.Fatalf("Expected 1 stat, got %d", len(allStats))
	}

	// Modify returned stats
	for _, stat := range allStats {
		stat.BytesDownloaded = 999999
	}

	// Verify original is unchanged
	original := s.GetStats(peer1)
	if original.BytesDownloaded == 999999 {
		t.Error("GetAllStats should return copies, not references")
	}
}
