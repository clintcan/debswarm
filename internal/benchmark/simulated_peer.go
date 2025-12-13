// Package benchmark provides simulated peers for performance testing
package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/debswarm/debswarm/internal/downloader"
)

// PeerConfig defines the characteristics of a simulated peer
type PeerConfig struct {
	ID            string        // Unique peer identifier
	LatencyMin    time.Duration // Minimum latency (RTT)
	LatencyMax    time.Duration // Maximum latency (adds jitter)
	ThroughputBps int64         // Bandwidth in bytes per second
	ErrorRate     float64       // Probability of random error (0.0-1.0)
	TimeoutRate   float64       // Probability of timeout (0.0-1.0)
}

// DefaultPeerConfig returns a reasonable default peer configuration
func DefaultPeerConfig(id string) PeerConfig {
	return PeerConfig{
		ID:            id,
		LatencyMin:    10 * time.Millisecond,
		LatencyMax:    30 * time.Millisecond,
		ThroughputBps: 50 * 1024 * 1024, // 50 MB/s
		ErrorRate:     0.0,
		TimeoutRate:   0.0,
	}
}

// SimulatedPeer implements the downloader.Source interface
type SimulatedPeer struct {
	cfg     PeerConfig
	content map[string][]byte // hash -> data
	mu      sync.RWMutex
	rng     *rand.Rand

	// Metrics
	requestCount   int64
	bytesServed    int64
	errorCount     int64
	totalLatencyNs int64
}

// NewSimulatedPeer creates a new simulated peer
func NewSimulatedPeer(cfg PeerConfig) *SimulatedPeer {
	return &SimulatedPeer{
		cfg:     cfg,
		content: make(map[string][]byte),
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// AddContent pre-loads content that this peer can serve
func (p *SimulatedPeer) AddContent(hash string, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.content[hash] = data
}

// AddGeneratedContent generates and stores deterministic content of given size
// Returns the SHA256 hash of the content
func (p *SimulatedPeer) AddGeneratedContent(size int64) string {
	// Generate deterministic content based on size
	data := GenerateTestData(size)
	hash := sha256.Sum256(data)
	hashHex := hex.EncodeToString(hash[:])

	p.AddContent(hashHex, data)
	return hashHex
}

// ID implements downloader.Source
func (p *SimulatedPeer) ID() string {
	return p.cfg.ID
}

// Type implements downloader.Source
func (p *SimulatedPeer) Type() string {
	return downloader.SourceTypePeer
}

// Download implements downloader.Source for range requests
func (p *SimulatedPeer) Download(ctx context.Context, hash string, start, end int64) ([]byte, error) {
	atomic.AddInt64(&p.requestCount, 1)
	startTime := time.Now()
	defer func() {
		atomic.AddInt64(&p.totalLatencyNs, time.Since(startTime).Nanoseconds())
	}()

	// Simulate latency
	latency := p.simulateLatency()
	select {
	case <-time.After(latency):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Check for simulated timeout
	if p.shouldTimeout() {
		atomic.AddInt64(&p.errorCount, 1)
		// Wait for context to expire or a long time
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(60 * time.Second):
			return nil, context.DeadlineExceeded
		}
	}

	// Check for simulated error
	if p.shouldError() {
		atomic.AddInt64(&p.errorCount, 1)
		return nil, fmt.Errorf("simulated peer error")
	}

	// Get content
	p.mu.RLock()
	data, ok := p.content[hash]
	p.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("content not found: %s", hash[:16])
	}

	// Handle range
	if end == -1 || end > int64(len(data)) {
		end = int64(len(data))
	}
	if start < 0 {
		start = 0
	}
	if start >= int64(len(data)) {
		return nil, fmt.Errorf("start offset beyond content length")
	}

	chunk := data[start:end]
	chunkSize := int64(len(chunk))

	// Simulate bandwidth limit
	if err := p.simulateBandwidth(ctx, chunkSize); err != nil {
		return nil, err
	}

	atomic.AddInt64(&p.bytesServed, chunkSize)
	return chunk, nil
}

// DownloadFull implements downloader.Source for full file downloads
func (p *SimulatedPeer) DownloadFull(ctx context.Context, hash string) ([]byte, error) {
	return p.Download(ctx, hash, 0, -1)
}

// simulateLatency returns a random latency within configured bounds
func (p *SimulatedPeer) simulateLatency() time.Duration {
	if p.cfg.LatencyMax <= p.cfg.LatencyMin {
		return p.cfg.LatencyMin
	}
	jitter := time.Duration(p.rng.Int63n(int64(p.cfg.LatencyMax - p.cfg.LatencyMin)))
	return p.cfg.LatencyMin + jitter
}

// simulateBandwidth delays based on throughput limit
func (p *SimulatedPeer) simulateBandwidth(ctx context.Context, bytes int64) error {
	if p.cfg.ThroughputBps <= 0 {
		return nil // Unlimited
	}

	transferTime := time.Duration(float64(bytes) / float64(p.cfg.ThroughputBps) * float64(time.Second))
	if transferTime <= 0 {
		return nil
	}

	select {
	case <-time.After(transferTime):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// shouldError returns true if this request should fail
func (p *SimulatedPeer) shouldError() bool {
	if p.cfg.ErrorRate <= 0 {
		return false
	}
	return p.rng.Float64() < p.cfg.ErrorRate
}

// shouldTimeout returns true if this request should timeout
func (p *SimulatedPeer) shouldTimeout() bool {
	if p.cfg.TimeoutRate <= 0 {
		return false
	}
	return p.rng.Float64() < p.cfg.TimeoutRate
}

// Stats returns the peer's statistics
type PeerStats struct {
	ID             string
	RequestCount   int64
	BytesServed    int64
	ErrorCount     int64
	AvgLatencyMs   float64
	ThroughputMBps float64
}

// Stats returns current statistics for this peer
func (p *SimulatedPeer) Stats() PeerStats {
	requests := atomic.LoadInt64(&p.requestCount)
	bytes := atomic.LoadInt64(&p.bytesServed)
	errors := atomic.LoadInt64(&p.errorCount)
	totalLatencyNs := atomic.LoadInt64(&p.totalLatencyNs)

	var avgLatency float64
	var throughput float64

	if requests > 0 {
		avgLatency = float64(totalLatencyNs) / float64(requests) / 1e6 // ms
	}
	if totalLatencyNs > 0 {
		throughput = float64(bytes) / (float64(totalLatencyNs) / 1e9) / (1024 * 1024) // MB/s
	}

	return PeerStats{
		ID:             p.cfg.ID,
		RequestCount:   requests,
		BytesServed:    bytes,
		ErrorCount:     errors,
		AvgLatencyMs:   avgLatency,
		ThroughputMBps: throughput,
	}
}

// GenerateTestData creates deterministic test data of the specified size
func GenerateTestData(size int64) []byte {
	data := make([]byte, size)
	// Use a simple pattern that's deterministic but compresses poorly
	// (like real .deb files)
	for i := range data {
		data[i] = byte(i*7 + i/256)
	}
	return data
}

// Verify SimulatedPeer implements Source interface
var _ downloader.Source = (*SimulatedPeer)(nil)
