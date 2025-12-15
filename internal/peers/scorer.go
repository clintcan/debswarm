// Package peers provides peer scoring and selection for debswarm
package peers

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Score weights for peer scoring algorithm
const (
	WeightLatency     = 0.3
	WeightThroughput  = 0.3
	WeightReliability = 0.25
	WeightFreshness   = 0.15

	// Decay factor for exponential moving average
	EMAAlpha = 0.3

	// Minimum samples before trusting a peer's score
	MinSamples = 3

	// Score thresholds
	ScoreExcellent = 0.8
	ScoreGood      = 0.6
	ScoreFair      = 0.4
	ScorePoor      = 0.2

	// Blacklist threshold - peers below this are not used
	ScoreBlacklist = 0.1

	// Maximum age before peer is considered stale
	MaxPeerAge = 24 * time.Hour
)

// PeerScore holds scoring data for a peer
type PeerScore struct {
	PeerID peer.ID

	// Performance metrics (exponential moving averages)
	AvgLatencyMs  float64 // Lower is better
	AvgThroughput float64 // Bytes per second, higher is better
	SuccessRate   float64 // 0-1, higher is better

	// Counters
	TotalRequests   int64
	SuccessCount    int64
	FailureCount    int64
	BytesDownloaded int64
	BytesUploaded   int64

	// Timing
	FirstSeen   time.Time
	LastSeen    time.Time
	LastSuccess time.Time
	LastFailure time.Time

	// Flags
	Blacklisted     bool
	BlacklistReason string
	BlacklistUntil  time.Time

	// Computed score (cached)
	cachedScore   float64
	scoreCachedAt time.Time
}

// Scorer manages peer scores and selection
type Scorer struct {
	peers map[peer.ID]*PeerScore
	mu    sync.RWMutex

	// Reference values for normalization
	refLatencyMs  float64 // Expected good latency
	refThroughput float64 // Expected good throughput
}

// NewScorer creates a new peer scorer
func NewScorer() *Scorer {
	return &Scorer{
		peers:         make(map[peer.ID]*PeerScore),
		refLatencyMs:  100,              // 100ms is "good"
		refThroughput: 1024 * 1024 * 10, // 10 MB/s is "good"
	}
}

// RecordSuccess records a successful transfer
func (s *Scorer) RecordSuccess(peerID peer.ID, bytes int64, latencyMs float64, throughput float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ps := s.getOrCreate(peerID)
	now := time.Now()

	ps.TotalRequests++
	ps.SuccessCount++
	ps.BytesDownloaded += bytes
	ps.LastSeen = now
	ps.LastSuccess = now

	// Clear expired blacklist on successful transfer
	if ps.Blacklisted && now.After(ps.BlacklistUntil) {
		ps.Blacklisted = false
		ps.BlacklistReason = ""
	}

	// Update EMAs
	if ps.TotalRequests == 1 {
		ps.AvgLatencyMs = latencyMs
		ps.AvgThroughput = throughput
	} else {
		ps.AvgLatencyMs = ema(ps.AvgLatencyMs, latencyMs, EMAAlpha)
		ps.AvgThroughput = ema(ps.AvgThroughput, throughput, EMAAlpha)
	}

	ps.SuccessRate = float64(ps.SuccessCount) / float64(ps.TotalRequests)

	// Update cached score while holding write lock
	ps.cachedScore = s.computeScore(ps)
	ps.scoreCachedAt = time.Now()
}

// RecordFailure records a failed transfer
func (s *Scorer) RecordFailure(peerID peer.ID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ps := s.getOrCreate(peerID)
	now := time.Now()

	ps.TotalRequests++
	ps.FailureCount++
	ps.LastSeen = now
	ps.LastFailure = now

	ps.SuccessRate = float64(ps.SuccessCount) / float64(ps.TotalRequests)

	// Update cached score while holding write lock
	ps.cachedScore = s.computeScore(ps)
	ps.scoreCachedAt = time.Now()
}

// RecordUpload records bytes uploaded to a peer
func (s *Scorer) RecordUpload(peerID peer.ID, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ps := s.getOrCreate(peerID)
	ps.BytesUploaded += bytes
	ps.LastSeen = time.Now()
}

// Blacklist marks a peer as blacklisted
func (s *Scorer) Blacklist(peerID peer.ID, reason string, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ps := s.getOrCreate(peerID)
	ps.Blacklisted = true
	ps.BlacklistReason = reason
	ps.BlacklistUntil = time.Now().Add(duration)
	ps.cachedScore = 0
	ps.scoreCachedAt = time.Now()
}

// IsBlacklisted checks if a peer is currently blacklisted
func (s *Scorer) IsBlacklisted(peerID peer.ID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.isBlacklistedLocked(peerID)
}

// isBlacklistedLocked checks blacklist status - caller must hold at least RLock
// Note: This only checks if the peer is currently blacklisted; expired blacklists
// are cleared during Cleanup() or write operations
func (s *Scorer) isBlacklistedLocked(peerID peer.ID) bool {
	ps, ok := s.peers[peerID]
	if !ok {
		return false
	}

	if !ps.Blacklisted {
		return false
	}

	// Check if blacklist has expired
	if time.Now().After(ps.BlacklistUntil) {
		return false
	}

	return true
}

// GetScore returns the current score for a peer (0-1)
func (s *Scorer) GetScore(peerID peer.ID) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ps, ok := s.peers[peerID]
	if !ok {
		return 0.5 // Unknown peers get neutral score
	}

	return s.computeScore(ps)
}

// SelectBest returns the best n peers from the given list, sorted by score
func (s *Scorer) SelectBest(candidates []peer.AddrInfo, n int) []peer.AddrInfo {
	if len(candidates) == 0 {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	type scored struct {
		info  peer.AddrInfo
		score float64
	}

	scoredPeers := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		if s.isBlacklistedLocked(c.ID) {
			continue
		}

		ps, ok := s.peers[c.ID]
		var score float64
		if ok {
			score = s.computeScore(ps)
		} else {
			score = 0.5 // Unknown peers get neutral score
		}

		if score >= ScoreBlacklist {
			scoredPeers = append(scoredPeers, scored{c, score})
		}
	}

	// Sort by score descending
	sort.Slice(scoredPeers, func(i, j int) bool {
		return scoredPeers[i].score > scoredPeers[j].score
	})

	// Take top n
	if n > len(scoredPeers) {
		n = len(scoredPeers)
	}

	result := make([]peer.AddrInfo, n)
	for i := 0; i < n; i++ {
		result[i] = scoredPeers[i].info
	}

	return result
}

// SelectDiverse returns peers with a mix of scores for exploration
// Returns top performers plus some random lower-scored peers
func (s *Scorer) SelectDiverse(candidates []peer.AddrInfo, n int) []peer.AddrInfo {
	if len(candidates) == 0 {
		return nil
	}

	// Get best peers
	best := s.SelectBest(candidates, n*2)
	if len(best) <= n {
		return best
	}

	// Take 70% best, 30% exploratory
	numBest := (n * 7) / 10
	if numBest < 1 {
		numBest = 1
	}
	numExplore := n - numBest

	result := make([]peer.AddrInfo, 0, n)
	result = append(result, best[:numBest]...)

	// Add some lower-ranked peers for exploration
	if numExplore > 0 && len(best) > numBest {
		exploratory := best[numBest:]
		step := len(exploratory) / numExplore
		if step < 1 {
			step = 1
		}
		for i := 0; i < len(exploratory) && len(result) < n; i += step {
			result = append(result, exploratory[i])
		}
	}

	return result
}

// GetStats returns statistics for a peer
func (s *Scorer) GetStats(peerID peer.ID) *PeerScore {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ps, ok := s.peers[peerID]
	if !ok {
		return nil
	}

	// Return a copy
	copy := *ps
	copy.cachedScore = s.computeScore(ps)
	return &copy
}

// GetAllStats returns statistics for all known peers
func (s *Scorer) GetAllStats() []*PeerScore {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*PeerScore, 0, len(s.peers))
	for _, ps := range s.peers {
		copy := *ps
		copy.cachedScore = s.computeScore(ps)
		result = append(result, &copy)
	}

	return result
}

// Cleanup removes stale peer entries
func (s *Scorer) Cleanup() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	threshold := time.Now().Add(-MaxPeerAge)
	removed := 0

	for id, ps := range s.peers {
		// Remove stale peers
		if ps.LastSeen.Before(threshold) {
			delete(s.peers, id)
			removed++
			continue
		}

		// Clear expired blacklists
		if ps.Blacklisted && time.Now().After(ps.BlacklistUntil) {
			ps.Blacklisted = false
			ps.BlacklistReason = ""
		}
	}

	return removed
}

// PeerCount returns the number of known peers
func (s *Scorer) PeerCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.peers)
}

func (s *Scorer) getOrCreate(peerID peer.ID) *PeerScore {
	ps, ok := s.peers[peerID]
	if !ok {
		ps = &PeerScore{
			PeerID:    peerID,
			FirstSeen: time.Now(),
			LastSeen:  time.Now(),
		}
		s.peers[peerID] = ps
	}
	return ps
}

func (s *Scorer) computeScore(ps *PeerScore) float64 {
	// Check cache - use cached value if valid and recent
	// Note: cachedScore of 0 is valid for blacklisted peers, so check scoreCachedAt
	if !ps.scoreCachedAt.IsZero() && time.Since(ps.scoreCachedAt) < time.Minute {
		return ps.cachedScore
	}

	// Not enough data - return neutral score
	if ps.TotalRequests < MinSamples {
		return 0.5
	}

	// Blacklisted peers get zero score
	if ps.Blacklisted && time.Now().Before(ps.BlacklistUntil) {
		return 0
	}

	// Latency score (lower is better)
	// Score of 1.0 at refLatency, decreasing as latency increases
	latencyScore := s.refLatencyMs / (s.refLatencyMs + ps.AvgLatencyMs)

	// Throughput score (higher is better)
	// Score of 1.0 at refThroughput, increasing as throughput increases
	throughputScore := ps.AvgThroughput / (ps.AvgThroughput + s.refThroughput)
	if throughputScore > 1 {
		throughputScore = 1
	}

	// Reliability score is just success rate
	reliabilityScore := ps.SuccessRate

	// Freshness score - prefer recently active peers
	hoursSinceLastSeen := time.Since(ps.LastSeen).Hours()
	freshnessScore := math.Exp(-hoursSinceLastSeen / 24) // Decay over 24 hours

	// Weighted combination
	score := WeightLatency*latencyScore +
		WeightThroughput*throughputScore +
		WeightReliability*reliabilityScore +
		WeightFreshness*freshnessScore

	// Clamp to 0-1
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	// Note: We don't cache here as this may be called from RLock context
	// Caching is done in write operations (RecordSuccess/RecordFailure)

	return score
}

// Exponential moving average
func ema(old, new, alpha float64) float64 {
	return alpha*new + (1-alpha)*old
}

// ScoreCategory returns a human-readable category for a score
func ScoreCategory(score float64) string {
	switch {
	case score >= ScoreExcellent:
		return "excellent"
	case score >= ScoreGood:
		return "good"
	case score >= ScoreFair:
		return "fair"
	case score >= ScorePoor:
		return "poor"
	default:
		return "bad"
	}
}
