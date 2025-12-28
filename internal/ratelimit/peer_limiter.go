// Package ratelimit provides rate-limited io.Reader/Writer wrappers
package ratelimit

import (
	"context"
	"io"
	"math"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/debswarm/debswarm/internal/lifecycle"
	"github.com/debswarm/debswarm/internal/peers"
)

// Default constants for per-peer rate limiting
const (
	DefaultExpectedPeers    = 10
	DefaultMinPeerRate      = 100 * 1024       // 100 KB/s minimum
	DefaultMaxBoostFactor   = 1.5              // Max 1.5x base rate
	DefaultIdleTimeout      = 30 * time.Second // Cleanup idle limiters
	DefaultAdaptiveRecalc   = 10 * time.Second // Recalculate rates
	DefaultLatencyThreshold = 500.0            // 500ms latency threshold for congestion
)

// PeerLimiterConfig configures the per-peer rate limiter manager
type PeerLimiterConfig struct {
	// GlobalLimit is the total bandwidth cap (bytes/sec), 0 = unlimited
	GlobalLimit int64

	// PerPeerLimit is the per-peer bandwidth cap (bytes/sec)
	// 0 = auto-calculate from GlobalLimit / ExpectedPeers
	PerPeerLimit int64

	// ExpectedPeers is used for auto-calculation when PerPeerLimit is 0
	ExpectedPeers int

	// MinPeerLimit is the floor for adaptive reduction (bytes/sec)
	MinPeerLimit int64

	// AdaptiveEnabled enables adaptive rate adjustment based on peer scores
	AdaptiveEnabled bool

	// MaxBoostFactor is the maximum multiplier for high-performing peers
	MaxBoostFactor float64

	// LatencyThresholdMs is the latency above which rates are reduced
	LatencyThresholdMs float64

	// IdleTimeout is how long before idle peer limiters are cleaned up
	IdleTimeout time.Duration

	// AdaptiveRecalcInterval is how often to recalculate adaptive rates
	AdaptiveRecalcInterval time.Duration

	// Logger for debug output
	Logger *zap.Logger
}

// DefaultPeerLimiterConfig returns a configuration with sensible defaults
func DefaultPeerLimiterConfig() PeerLimiterConfig {
	return PeerLimiterConfig{
		ExpectedPeers:          DefaultExpectedPeers,
		MinPeerLimit:           DefaultMinPeerRate,
		AdaptiveEnabled:        true,
		MaxBoostFactor:         DefaultMaxBoostFactor,
		LatencyThresholdMs:     DefaultLatencyThreshold,
		IdleTimeout:            DefaultIdleTimeout,
		AdaptiveRecalcInterval: DefaultAdaptiveRecalc,
	}
}

// PeerLimiter wraps a rate limiter with per-peer metadata
type PeerLimiter struct {
	limiter      *rate.Limiter
	peerID       peer.ID
	currentLimit int64
	baseLimit    int64
	lastAccess   time.Time
	mu           sync.Mutex
}

// PeerLimiterManager manages per-peer rate limiters with optional adaptive adjustment
type PeerLimiterManager struct {
	mu           sync.RWMutex
	peerLimiters map[peer.ID]*PeerLimiter

	// Configuration
	globalLimit    int64
	perPeerLimit   int64
	minPeerLimit   int64
	expectedPeers  int
	maxBoostFactor float64
	latencyThresh  float64
	idleTimeout    time.Duration
	recalcInterval time.Duration

	// Dependencies
	globalLimiter   *Limiter
	scorer          *peers.Scorer
	adaptiveEnabled bool
	logger          *zap.Logger

	// Lifecycle
	lc *lifecycle.Manager
}

// NewPeerLimiterManager creates a new per-peer rate limiter manager
func NewPeerLimiterManager(cfg PeerLimiterConfig, globalLimiter *Limiter, scorer *peers.Scorer) *PeerLimiterManager {
	// Calculate effective per-peer limit
	perPeerLimit := cfg.PerPeerLimit
	if perPeerLimit == 0 && cfg.GlobalLimit > 0 {
		expectedPeers := cfg.ExpectedPeers
		if expectedPeers <= 0 {
			expectedPeers = DefaultExpectedPeers
		}
		perPeerLimit = cfg.GlobalLimit / int64(expectedPeers)
	}

	// Ensure minimum
	if perPeerLimit > 0 && perPeerLimit < cfg.MinPeerLimit {
		perPeerLimit = cfg.MinPeerLimit
	}

	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	// Ensure we have valid timeouts
	idleTimeout := cfg.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	recalcInterval := cfg.AdaptiveRecalcInterval
	if recalcInterval <= 0 {
		recalcInterval = DefaultAdaptiveRecalc
	}

	m := &PeerLimiterManager{
		peerLimiters:    make(map[peer.ID]*PeerLimiter),
		globalLimit:     cfg.GlobalLimit,
		perPeerLimit:    perPeerLimit,
		minPeerLimit:    cfg.MinPeerLimit,
		expectedPeers:   cfg.ExpectedPeers,
		maxBoostFactor:  cfg.MaxBoostFactor,
		latencyThresh:   cfg.LatencyThresholdMs,
		idleTimeout:     idleTimeout,
		recalcInterval:  recalcInterval,
		globalLimiter:   globalLimiter,
		scorer:          scorer,
		adaptiveEnabled: cfg.AdaptiveEnabled && scorer != nil,
		logger:          logger,
		lc:              lifecycle.New(nil),
	}

	// Only start background goroutines if per-peer limiting is enabled
	if m.perPeerLimit > 0 {
		m.lc.RunTicker(m.idleTimeout, m.cleanupIdleLimiters)

		if m.adaptiveEnabled {
			m.lc.RunTicker(m.recalcInterval, m.recalculateRates)
		}
	}

	return m
}

// Enabled returns whether per-peer limiting is active
func (m *PeerLimiterManager) Enabled() bool {
	return m != nil && m.perPeerLimit > 0
}

// GetLimiter returns the rate limiter for a specific peer, creating if needed
func (m *PeerLimiterManager) GetLimiter(peerID peer.ID) *rate.Limiter {
	if !m.Enabled() {
		return nil
	}

	// Fast path: check if limiter exists
	m.mu.RLock()
	pl, ok := m.peerLimiters[peerID]
	m.mu.RUnlock()

	if ok {
		pl.mu.Lock()
		pl.lastAccess = time.Now()
		pl.mu.Unlock()
		return pl.limiter
	}

	// Slow path: create new limiter
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if pl, ok = m.peerLimiters[peerID]; ok {
		pl.mu.Lock()
		pl.lastAccess = time.Now()
		pl.mu.Unlock()
		return pl.limiter
	}

	// Calculate initial limit (may be adjusted by adaptive logic)
	limit := m.calculatePeerLimit(peerID)

	// Create limiter with appropriate burst
	burst := calculateBurst(limit)
	limiter := rate.NewLimiter(rate.Limit(limit), int(burst))

	pl = &PeerLimiter{
		limiter:      limiter,
		peerID:       peerID,
		currentLimit: limit,
		baseLimit:    m.perPeerLimit,
		lastAccess:   time.Now(),
	}
	m.peerLimiters[peerID] = pl

	m.logger.Debug("Created per-peer limiter",
		zap.String("peer", peerID.String()),
		zap.Int64("limit_bytes_sec", limit))

	return pl.limiter
}

// ReaderContext returns a rate-limited reader that applies both global and per-peer limits
func (m *PeerLimiterManager) ReaderContext(ctx context.Context, peerID peer.ID, r io.Reader) io.Reader {
	peerLimiter := m.GetLimiter(peerID)

	// Get global limiter if available
	var globalLim *rate.Limiter
	if m.globalLimiter != nil && m.globalLimiter.Enabled() {
		globalLim = m.globalLimiter.limiter
	}

	// If neither is active, return original reader
	if peerLimiter == nil && globalLim == nil {
		return r
	}

	return &ComposedLimitedReader{
		r:         r,
		globalLim: globalLim,
		peerLim:   peerLimiter,
		ctx:       ctx,
	}
}

// WriterContext returns a rate-limited writer that applies both global and per-peer limits
func (m *PeerLimiterManager) WriterContext(ctx context.Context, peerID peer.ID, w io.Writer) io.Writer {
	peerLimiter := m.GetLimiter(peerID)

	// Get global limiter if available
	var globalLim *rate.Limiter
	if m.globalLimiter != nil && m.globalLimiter.Enabled() {
		globalLim = m.globalLimiter.limiter
	}

	// If neither is active, return original writer
	if peerLimiter == nil && globalLim == nil {
		return w
	}

	return &ComposedLimitedWriter{
		w:         w,
		globalLim: globalLim,
		peerLim:   peerLimiter,
		ctx:       ctx,
	}
}

// calculatePeerLimit calculates the rate limit for a specific peer
func (m *PeerLimiterManager) calculatePeerLimit(peerID peer.ID) int64 {
	baseLimit := m.perPeerLimit
	if baseLimit <= 0 {
		return 0 // Unlimited
	}

	// If adaptive is disabled or no scorer, use base limit
	if !m.adaptiveEnabled || m.scorer == nil {
		return baseLimit
	}

	return m.adjustForPeerScore(peerID, baseLimit)
}

// adjustForPeerScore calculates rate based on peer performance metrics
// Moderate adjustments: ±50% based on score (0.5 to 1.5x multiplier)
func (m *PeerLimiterManager) adjustForPeerScore(peerID peer.ID, baseLimit int64) int64 {
	score := m.scorer.GetScore(peerID)
	stats := m.scorer.GetStats(peerID)

	// Moderate adjustments: score 0.5 = base, 1.0 = 1.5x, 0.0 = 0.5x
	// Formula: adjustmentFactor = 0.5 + score (range: 0.5 to 1.5)
	adjustmentFactor := 0.5 + score

	// Congestion penalty: reduce if latency > threshold
	if stats != nil && stats.AvgLatencyMs > m.latencyThresh {
		// Reduce by up to 30% for high latency
		latencyRatio := (stats.AvgLatencyMs - m.latencyThresh) / m.latencyThresh
		penalty := math.Min(0.3, latencyRatio*0.15)
		adjustmentFactor *= (1.0 - penalty)
	}

	// Calculate new limit
	newLimit := int64(float64(baseLimit) * adjustmentFactor)

	// Clamp to bounds
	if newLimit < m.minPeerLimit {
		newLimit = m.minPeerLimit
	}
	maxLimit := int64(float64(baseLimit) * m.maxBoostFactor)
	if newLimit > maxLimit {
		newLimit = maxLimit
	}

	return newLimit
}

// cleanupIdleLimiters removes limiters that haven't been used recently
func (m *PeerLimiterManager) cleanupIdleLimiters() {
	m.mu.Lock()
	defer m.mu.Unlock()

	threshold := time.Now().Add(-m.idleTimeout)
	removed := 0

	for peerID, pl := range m.peerLimiters {
		pl.mu.Lock()
		lastAccess := pl.lastAccess
		pl.mu.Unlock()

		if lastAccess.Before(threshold) {
			delete(m.peerLimiters, peerID)
			removed++
		}
	}

	if removed > 0 {
		m.logger.Debug("Cleaned up idle peer limiters", zap.Int("removed", removed))
	}
}

// recalculateRates updates all peer limits based on current scores
func (m *PeerLimiterManager) recalculateRates() {
	m.mu.RLock()
	peerIDs := make([]peer.ID, 0, len(m.peerLimiters))
	for peerID := range m.peerLimiters {
		peerIDs = append(peerIDs, peerID)
	}
	m.mu.RUnlock()

	for _, peerID := range peerIDs {
		newLimit := m.adjustForPeerScore(peerID, m.perPeerLimit)

		m.mu.RLock()
		pl, ok := m.peerLimiters[peerID]
		m.mu.RUnlock()

		if !ok {
			continue
		}

		pl.mu.Lock()
		if newLimit != pl.currentLimit {
			pl.limiter.SetLimit(rate.Limit(newLimit))
			pl.currentLimit = newLimit
			m.logger.Debug("Adjusted peer rate",
				zap.String("peer", peerID.String()),
				zap.Int64("old_limit", pl.currentLimit),
				zap.Int64("new_limit", newLimit))
		}
		pl.mu.Unlock()
	}
}

// GetPeerStats returns current rate limit info for a peer
func (m *PeerLimiterManager) GetPeerStats(peerID peer.ID) (currentLimit int64, baseLimit int64, exists bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	pl, ok := m.peerLimiters[peerID]
	if !ok {
		return 0, m.perPeerLimit, false
	}

	pl.mu.Lock()
	defer pl.mu.Unlock()

	return pl.currentLimit, pl.baseLimit, true
}

// PeerCount returns the number of active peer limiters
func (m *PeerLimiterManager) PeerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.peerLimiters)
}

// Close shuts down the peer limiter manager
func (m *PeerLimiterManager) Close() {
	m.lc.Stop()
}

// ComposedLimitedReader applies both global and per-peer rate limits
type ComposedLimitedReader struct {
	r         io.Reader
	globalLim *rate.Limiter
	peerLim   *rate.Limiter
	ctx       context.Context
}

// Read implements io.Reader with composed rate limiting
func (cr *ComposedLimitedReader) Read(p []byte) (n int, err error) {
	n, err = cr.r.Read(p)
	if n > 0 {
		// Wait for BOTH limiters (the stricter one dominates)
		if cr.globalLim != nil {
			if waitErr := cr.globalLim.WaitN(cr.ctx, n); waitErr != nil {
				return n, waitErr
			}
		}
		if cr.peerLim != nil {
			if waitErr := cr.peerLim.WaitN(cr.ctx, n); waitErr != nil {
				return n, waitErr
			}
		}
	}
	return n, err
}

// ComposedLimitedWriter applies both global and per-peer rate limits
type ComposedLimitedWriter struct {
	w         io.Writer
	globalLim *rate.Limiter
	peerLim   *rate.Limiter
	ctx       context.Context
}

// Write implements io.Writer with composed rate limiting
func (cw *ComposedLimitedWriter) Write(p []byte) (n int, err error) {
	// Wait for BOTH limiters before writing (the stricter one dominates)
	if cw.globalLim != nil {
		if err := cw.globalLim.WaitN(cw.ctx, len(p)); err != nil {
			return 0, err
		}
	}
	if cw.peerLim != nil {
		if err := cw.peerLim.WaitN(cw.ctx, len(p)); err != nil {
			return 0, err
		}
	}
	return cw.w.Write(p)
}

// calculateBurst determines the burst size for a rate limiter
func calculateBurst(bytesPerSecond int64) int64 {
	if bytesPerSecond <= 0 {
		return 64 * 1024 // Default 64KB
	}

	burst := bytesPerSecond
	if burst < 64*1024 {
		burst = 64 * 1024
	}
	if burst > 4*1024*1024 {
		burst = 4 * 1024 * 1024 // Cap at 4MB
	}
	return burst
}
