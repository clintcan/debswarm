// Package timeouts provides adaptive timeout management for network operations
package timeouts

import (
	"math"
	"sync"
	"time"
)

// Default timeout values
const (
	// Base timeouts
	DefaultDHTLookup      = 100 * time.Millisecond
	DefaultDHTLookupFull  = 5 * time.Second
	DefaultPeerConnect    = 2 * time.Second
	DefaultPeerFirstByte  = 5 * time.Second
	DefaultPeerStall      = 10 * time.Second
	DefaultMirrorFallback = 200 * time.Millisecond

	// Timeout bounds
	MinTimeout = 50 * time.Millisecond
	MaxTimeout = 60 * time.Second

	// Adaptation parameters
	AdaptationAlpha   = 0.2 // EMA smoothing factor
	SuccessMultiplier = 0.9 // Reduce timeout on success
	FailureMultiplier = 1.5 // Increase timeout on failure
	TimeoutMultiplier = 2.0 // Double on timeout

	// Size-based timeout calculation
	BytesPerSecondBase = 1024 * 1024 // 1 MB/s baseline
)

// Operation types for timeout tracking
type Operation string

const (
	OpDHTLookup     Operation = "dht_lookup"
	OpDHTLookupFull Operation = "dht_lookup_full"
	OpPeerConnect   Operation = "peer_connect"
	OpPeerFirstByte Operation = "peer_first_byte"
	OpPeerTransfer  Operation = "peer_transfer"
	OpMirrorFetch   Operation = "mirror_fetch"
	OpChunkDownload Operation = "chunk_download"
)

// Manager handles adaptive timeouts for various operations
type Manager struct {
	mu       sync.RWMutex
	timeouts map[Operation]*adaptiveTimeout
	config   *Config
}

// Config holds timeout configuration
type Config struct {
	DHTLookup      time.Duration
	DHTLookupFull  time.Duration
	PeerConnect    time.Duration
	PeerFirstByte  time.Duration
	PeerStall      time.Duration
	MirrorFallback time.Duration

	// If true, timeouts adapt based on observed performance
	AdaptiveEnabled bool

	// Size-based timeout: base + (size / bytesPerSecond)
	BytesPerSecond int64
}

// DefaultConfig returns default timeout configuration
func DefaultConfig() *Config {
	return &Config{
		DHTLookup:       DefaultDHTLookup,
		DHTLookupFull:   DefaultDHTLookupFull,
		PeerConnect:     DefaultPeerConnect,
		PeerFirstByte:   DefaultPeerFirstByte,
		PeerStall:       DefaultPeerStall,
		MirrorFallback:  DefaultMirrorFallback,
		AdaptiveEnabled: true,
		BytesPerSecond:  BytesPerSecondBase,
	}
}

// adaptiveTimeout tracks timeout statistics for an operation
type adaptiveTimeout struct {
	baseTimeout    time.Duration
	currentTimeout time.Duration
	avgDuration    time.Duration
	successCount   int64
	failureCount   int64
	timeoutCount   int64
	lastUpdated    time.Time
}

// NewManager creates a new timeout manager
func NewManager(cfg *Config) *Manager {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	m := &Manager{
		timeouts: make(map[Operation]*adaptiveTimeout),
		config:   cfg,
	}

	// Initialize with defaults
	m.initTimeout(OpDHTLookup, cfg.DHTLookup)
	m.initTimeout(OpDHTLookupFull, cfg.DHTLookupFull)
	m.initTimeout(OpPeerConnect, cfg.PeerConnect)
	m.initTimeout(OpPeerFirstByte, cfg.PeerFirstByte)
	m.initTimeout(OpPeerTransfer, cfg.PeerStall)
	m.initTimeout(OpMirrorFetch, 30*time.Second)
	m.initTimeout(OpChunkDownload, 30*time.Second)

	return m
}

func (m *Manager) initTimeout(op Operation, base time.Duration) {
	m.timeouts[op] = &adaptiveTimeout{
		baseTimeout:    base,
		currentTimeout: base,
		lastUpdated:    time.Now(),
	}
}

// Get returns the current timeout for an operation
func (m *Manager) Get(op Operation) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if t, ok := m.timeouts[op]; ok {
		return t.currentTimeout
	}
	return 30 * time.Second // Default fallback
}

// GetForSize returns a timeout adjusted for file size
func (m *Manager) GetForSize(op Operation, sizeBytes int64) time.Duration {
	base := m.Get(op)

	if sizeBytes <= 0 || m.config.BytesPerSecond <= 0 {
		return base
	}

	// Add time based on size
	sizeTimeout := time.Duration(float64(sizeBytes) / float64(m.config.BytesPerSecond) * float64(time.Second))

	// Use the larger of base timeout or size-based timeout, with some margin
	timeout := base + sizeTimeout
	timeout = time.Duration(float64(timeout) * 1.5) // 50% margin

	return clampTimeout(timeout)
}

// RecordSuccess records a successful operation and adapts the timeout
func (m *Manager) RecordSuccess(op Operation, duration time.Duration) {
	if !m.config.AdaptiveEnabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	t, ok := m.timeouts[op]
	if !ok {
		return
	}

	t.successCount++
	t.lastUpdated = time.Now()

	// Update average duration using EMA
	if t.avgDuration == 0 {
		t.avgDuration = duration
	} else {
		t.avgDuration = time.Duration(
			AdaptationAlpha*float64(duration) + (1-AdaptationAlpha)*float64(t.avgDuration),
		)
	}

	// Gradually reduce timeout if operations are succeeding quickly
	if duration < t.currentTimeout/2 {
		t.currentTimeout = time.Duration(float64(t.currentTimeout) * SuccessMultiplier)
	}

	// Don't go below base or the observed average (with margin)
	minTimeout := t.baseTimeout
	avgWithMargin := time.Duration(float64(t.avgDuration) * 2)
	if avgWithMargin > minTimeout {
		minTimeout = avgWithMargin
	}

	t.currentTimeout = clampTimeout(max(t.currentTimeout, minTimeout))
}

// RecordFailure records a failed operation (not timeout) and adapts
func (m *Manager) RecordFailure(op Operation) {
	if !m.config.AdaptiveEnabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	t, ok := m.timeouts[op]
	if !ok {
		return
	}

	t.failureCount++
	t.lastUpdated = time.Now()

	// Slightly increase timeout on non-timeout failures
	// (the peer might just be slow, not unreachable)
	t.currentTimeout = time.Duration(float64(t.currentTimeout) * FailureMultiplier)
	t.currentTimeout = clampTimeout(t.currentTimeout)
}

// RecordTimeout records a timeout and adapts
func (m *Manager) RecordTimeout(op Operation) {
	if !m.config.AdaptiveEnabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	t, ok := m.timeouts[op]
	if !ok {
		return
	}

	t.timeoutCount++
	t.lastUpdated = time.Now()

	// Significantly increase timeout on actual timeouts
	t.currentTimeout = time.Duration(float64(t.currentTimeout) * TimeoutMultiplier)
	t.currentTimeout = clampTimeout(t.currentTimeout)
}

// Stats returns statistics for an operation
type Stats struct {
	Operation      Operation
	BaseTimeout    time.Duration
	CurrentTimeout time.Duration
	AvgDuration    time.Duration
	SuccessCount   int64
	FailureCount   int64
	TimeoutCount   int64
	LastUpdated    time.Time
}

// GetStats returns timeout statistics for an operation
func (m *Manager) GetStats(op Operation) *Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, ok := m.timeouts[op]
	if !ok {
		return nil
	}

	return &Stats{
		Operation:      op,
		BaseTimeout:    t.baseTimeout,
		CurrentTimeout: t.currentTimeout,
		AvgDuration:    t.avgDuration,
		SuccessCount:   t.successCount,
		FailureCount:   t.failureCount,
		TimeoutCount:   t.timeoutCount,
		LastUpdated:    t.lastUpdated,
	}
}

// GetAllStats returns statistics for all operations
func (m *Manager) GetAllStats() []*Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make([]*Stats, 0, len(m.timeouts))
	for op, t := range m.timeouts {
		stats = append(stats, &Stats{
			Operation:      op,
			BaseTimeout:    t.baseTimeout,
			CurrentTimeout: t.currentTimeout,
			AvgDuration:    t.avgDuration,
			SuccessCount:   t.successCount,
			FailureCount:   t.failureCount,
			TimeoutCount:   t.timeoutCount,
			LastUpdated:    t.lastUpdated,
		})
	}
	return stats
}

// Reset resets all timeouts to their base values
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range m.timeouts {
		t.currentTimeout = t.baseTimeout
		t.avgDuration = 0
		t.successCount = 0
		t.failureCount = 0
		t.timeoutCount = 0
		t.lastUpdated = time.Now()
	}
}

// ResetDecay gradually resets timeouts toward base values
// Call periodically to prevent permanent timeout inflation
func (m *Manager) ResetDecay(factor float64) {
	if factor <= 0 || factor >= 1 {
		factor = 0.1 // 10% decay toward base
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, t := range m.timeouts {
		// Move current timeout toward base by factor
		diff := float64(t.currentTimeout - t.baseTimeout)
		t.currentTimeout = time.Duration(float64(t.currentTimeout) - diff*factor)
		t.currentTimeout = clampTimeout(t.currentTimeout)
	}
}

func clampTimeout(d time.Duration) time.Duration {
	if d < MinTimeout {
		return MinTimeout
	}
	if d > MaxTimeout {
		return MaxTimeout
	}
	return d
}

func max(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// CalculateTransferTimeout calculates timeout for a data transfer
func CalculateTransferTimeout(sizeBytes int64, bytesPerSecond int64, margin float64) time.Duration {
	if bytesPerSecond <= 0 {
		bytesPerSecond = BytesPerSecondBase
	}
	if margin <= 0 {
		margin = 2.0
	}

	seconds := float64(sizeBytes) / float64(bytesPerSecond)
	timeout := time.Duration(seconds * margin * float64(time.Second))

	// Add minimum base timeout
	timeout += 5 * time.Second

	return clampTimeout(timeout)
}

// PercentileTimeout calculates a timeout based on percentile of observed durations
type DurationTracker struct {
	mu         sync.Mutex
	durations  []time.Duration
	maxSamples int
}

// NewDurationTracker creates a new duration tracker
func NewDurationTracker(maxSamples int) *DurationTracker {
	if maxSamples <= 0 {
		maxSamples = 100
	}
	return &DurationTracker{
		durations:  make([]time.Duration, 0, maxSamples),
		maxSamples: maxSamples,
	}
}

// Record records a duration
func (dt *DurationTracker) Record(d time.Duration) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	if len(dt.durations) >= dt.maxSamples {
		// Remove oldest (FIFO)
		dt.durations = dt.durations[1:]
	}
	dt.durations = append(dt.durations, d)
}

// Percentile returns the nth percentile of recorded durations
func (dt *DurationTracker) Percentile(p float64) time.Duration {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	if len(dt.durations) == 0 {
		return 0
	}

	// Sort a copy
	sorted := make([]time.Duration, len(dt.durations))
	copy(sorted, dt.durations)

	// Simple insertion sort (small n)
	for i := 1; i < len(sorted); i++ {
		j := i
		for j > 0 && sorted[j-1] > sorted[j] {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
			j--
		}
	}

	idx := int(math.Floor(p / 100.0 * float64(len(sorted)-1)))
	return sorted[idx]
}

// SuggestedTimeout returns a suggested timeout based on observed durations
// Uses P95 + margin
func (dt *DurationTracker) SuggestedTimeout(margin float64) time.Duration {
	p95 := dt.Percentile(95)
	if p95 == 0 {
		return 0
	}

	if margin <= 0 {
		margin = 1.5
	}

	return time.Duration(float64(p95) * margin)
}
