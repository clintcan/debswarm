// Package downloader provides parallel chunked downloads from multiple sources
package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Configuration constants
const (
	// Chunk size for parallel downloads (4 MB)
	DefaultChunkSize = 4 * 1024 * 1024

	// Minimum file size for chunked downloads
	MinChunkedSize = 10 * 1024 * 1024 // 10 MB

	// Maximum concurrent chunk downloads
	MaxConcurrentChunks = 8

	// Maximum retries per chunk
	MaxChunkRetries = 3

	// Timeout for individual chunk download
	ChunkTimeout = 30 * time.Second

	// Time to wait before starting mirror fallback
	MirrorFallbackDelay = 200 * time.Millisecond

	// Reference throughput for scoring (10 MB/s is considered "good")
	ReferenceThroughput = 10 * 1024 * 1024

	// Source type identifiers
	SourceTypePeer   = "peer"
	SourceTypeMirror = "mirror"
	SourceTypeMixed  = "mixed"
)

var (
	ErrNoSources      = errors.New("no download sources available")
	ErrHashMismatch   = errors.New("hash verification failed")
	ErrAllSourcesFailed = errors.New("all download sources failed")
	ErrTimeout        = errors.New("download timeout")
)

// Source represents a download source (peer or mirror)
type Source interface {
	// ID returns a unique identifier for this source
	ID() string

	// Download downloads a byte range from this source
	// If end is -1, downloads from start to end of file
	Download(ctx context.Context, hash string, start, end int64) ([]byte, error)

	// DownloadFull downloads the entire file
	DownloadFull(ctx context.Context, hash string) ([]byte, error)

	// Type returns "peer" or "mirror"
	Type() string
}

// PeerSource wraps a peer as a download source
type PeerSource struct {
	Info       peer.AddrInfo
	Downloader func(ctx context.Context, info peer.AddrInfo, hash string, start, end int64) ([]byte, error)
}

func (p *PeerSource) ID() string { return p.Info.ID.String() }
func (p *PeerSource) Type() string { return SourceTypePeer }

func (p *PeerSource) Download(ctx context.Context, hash string, start, end int64) ([]byte, error) {
	return p.Downloader(ctx, p.Info, hash, start, end)
}

func (p *PeerSource) DownloadFull(ctx context.Context, hash string) ([]byte, error) {
	return p.Downloader(ctx, p.Info, hash, 0, -1)
}

// MirrorSource wraps an HTTP mirror as a download source
type MirrorSource struct {
	URL        string
	Fetcher    func(ctx context.Context, url string, start, end int64) ([]byte, error)
}

func (m *MirrorSource) ID() string { return m.URL }
func (m *MirrorSource) Type() string { return SourceTypeMirror }

func (m *MirrorSource) Download(ctx context.Context, hash string, start, end int64) ([]byte, error) {
	return m.Fetcher(ctx, m.URL, start, end)
}

func (m *MirrorSource) DownloadFull(ctx context.Context, hash string) ([]byte, error) {
	return m.Fetcher(ctx, m.URL, 0, -1)
}

// Chunk represents a piece of a file being downloaded
type Chunk struct {
	Index    int
	Start    int64
	End      int64
	Data     []byte
	Source   Source
	Attempts int
	Error    error
	Duration time.Duration
}

// Downloader handles parallel chunked downloads
type Downloader struct {
	scorer       *peers.Scorer
	metrics      *metrics.Metrics
	chunkSize    int64
	maxConc      int
	stateManager *StateManager
}

// Config holds downloader configuration
type Config struct {
	ChunkSize       int64
	MaxConcurrent   int
	Scorer          *peers.Scorer
	Metrics         *metrics.Metrics
	StateManager    *StateManager
}

// New creates a new Downloader
func New(cfg *Config) *Downloader {
	chunkSize := int64(DefaultChunkSize)
	maxConc := MaxConcurrentChunks

	if cfg != nil {
		if cfg.ChunkSize > 0 {
			chunkSize = cfg.ChunkSize
		}
		if cfg.MaxConcurrent > 0 {
			maxConc = cfg.MaxConcurrent
		}
	}

	return &Downloader{
		scorer:       cfg.Scorer,
		metrics:      cfg.Metrics,
		chunkSize:    chunkSize,
		maxConc:      maxConc,
		stateManager: cfg.StateManager,
	}
}

// SetStateManager sets the state manager for download resume support
func (d *Downloader) SetStateManager(sm *StateManager) {
	d.stateManager = sm
}

// GetStateManager returns the state manager
func (d *Downloader) GetStateManager() *StateManager {
	return d.stateManager
}

// DownloadResult contains the result of a download
type DownloadResult struct {
	Data         []byte
	Hash         string
	Size         int64
	Duration     time.Duration
	Source       string // "peer", "mirror", or "mixed"
	PeerBytes    int64
	MirrorBytes  int64
	ChunksTotal  int
	ChunksFromP2P int
}

// Download downloads a file using the best available strategy
func (d *Downloader) Download(
	ctx context.Context,
	expectedHash string,
	expectedSize int64,
	peerSources []Source,
	mirrorSource Source,
) (*DownloadResult, error) {
	start := time.Now()

	if d.metrics != nil {
		d.metrics.ActiveDownloads.Inc()
		defer d.metrics.ActiveDownloads.Dec()
	}

	// Choose strategy based on file size and available sources
	if expectedSize > 0 && expectedSize >= MinChunkedSize && len(peerSources) > 0 {
		// Large file with peers available - use chunked parallel download
		return d.downloadChunked(ctx, expectedHash, expectedSize, peerSources, mirrorSource, start)
	}

	// Small file or no peers - use racing strategy
	return d.downloadRacing(ctx, expectedHash, peerSources, mirrorSource, start)
}

// downloadChunked performs parallel chunked download from multiple sources
func (d *Downloader) downloadChunked(
	ctx context.Context,
	expectedHash string,
	expectedSize int64,
	peerSources []Source,
	mirrorSource Source,
	startTime time.Time,
) (*DownloadResult, error) {
	// Calculate chunks
	numChunks := int((expectedSize + d.chunkSize - 1) / d.chunkSize)
	chunks := make([]*Chunk, numChunks)

	for i := 0; i < numChunks; i++ {
		start := int64(i) * d.chunkSize
		end := start + d.chunkSize
		if end > expectedSize {
			end = expectedSize
		}
		chunks[i] = &Chunk{
			Index: i,
			Start: start,
			End:   end,
		}
	}

	// Create work queue
	pendingChunks := make(chan *Chunk, numChunks)
	for _, c := range chunks {
		pendingChunks <- c
	}
	close(pendingChunks)

	// Results channel
	results := make(chan *Chunk, numChunks)

	// Track source performance for adaptive assignment
	sourceStats := &sourceTracker{
		stats: make(map[string]*sourceStats),
	}

	// All sources (peers + mirror)
	allSources := make([]Source, 0, len(peerSources)+1)
	allSources = append(allSources, peerSources...)
	if mirrorSource != nil {
		allSources = append(allSources, mirrorSource)
	}

	if len(allSources) == 0 {
		return nil, ErrNoSources
	}

	// Start workers
	var wg sync.WaitGroup
	workerCount := d.maxConc
	if workerCount > len(allSources) {
		workerCount = len(allSources)
	}
	if workerCount > numChunks {
		workerCount = numChunks
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			d.chunkWorker(ctx, workerID, pendingChunks, results, allSources, sourceStats, expectedHash)
		}(i)
	}

	// Wait for completion in separate goroutine
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	completedChunks := make([]*Chunk, numChunks)
	var peerBytes, mirrorBytes int64
	var chunksFromP2P int
	var firstError error

	for chunk := range results {
		if chunk.Error != nil {
			if firstError == nil {
				firstError = fmt.Errorf("chunk %d failed: %w", chunk.Index, chunk.Error)
				cancel() // Cancel other downloads on failure
			}
			continue // Drain remaining results to allow goroutines to exit
		}

		completedChunks[chunk.Index] = chunk

		if chunk.Source.Type() == SourceTypePeer {
			peerBytes += int64(len(chunk.Data))
			chunksFromP2P++
		} else {
			mirrorBytes += int64(len(chunk.Data))
		}
	}

	// Return error after all goroutines have finished
	if firstError != nil {
		return nil, firstError
	}

	// Verify all chunks received
	for i, c := range completedChunks {
		if c == nil {
			return nil, fmt.Errorf("chunk %d missing", i)
		}
	}

	// Assemble file - pre-allocate and copy directly to positions
	assembled := make([]byte, expectedSize)
	for _, c := range completedChunks {
		copy(assembled[c.Start:], c.Data)
	}

	// Verify hash
	actualHash := sha256.Sum256(assembled)
	actualHashHex := hex.EncodeToString(actualHash[:])

	if actualHashHex != expectedHash {
		if d.metrics != nil {
			d.metrics.VerificationFailures.Inc()
		}
		return nil, ErrHashMismatch
	}

	// Determine source type
	sourceType := SourceTypeMixed
	if peerBytes == 0 {
		sourceType = SourceTypeMirror
	} else if mirrorBytes == 0 {
		sourceType = SourceTypePeer
	}

	return &DownloadResult{
		Data:          assembled,
		Hash:          actualHashHex,
		Size:          int64(len(assembled)),
		Duration:      time.Since(startTime),
		Source:        sourceType,
		PeerBytes:     peerBytes,
		MirrorBytes:   mirrorBytes,
		ChunksTotal:   numChunks,
		ChunksFromP2P: chunksFromP2P,
	}, nil
}

// chunkWorker downloads chunks from the work queue
func (d *Downloader) chunkWorker(
	ctx context.Context,
	workerID int,
	pending <-chan *Chunk,
	results chan<- *Chunk,
	sources []Source,
	tracker *sourceTracker,
	hash string,
) {
	for chunk := range pending {
		select {
		case <-ctx.Done():
			chunk.Error = ctx.Err()
			results <- chunk
			return
		default:
		}

		// Select best source for this chunk
		source := tracker.selectBest(sources)

		// Download chunk with retries, collecting errors for context
		var data []byte
		var lastErr error
		var duration time.Duration
		var allErrors []string

		for attempt := 0; attempt < MaxChunkRetries; attempt++ {
			chunk.Attempts++

			chunkCtx, cancel := context.WithTimeout(ctx, ChunkTimeout)
			start := time.Now()

			data, lastErr = source.Download(chunkCtx, hash, chunk.Start, chunk.End)
			duration = time.Since(start)
			cancel()

			if lastErr == nil && int64(len(data)) == chunk.End-chunk.Start {
				break
			}

			// Record error for context
			if lastErr != nil {
				allErrors = append(allErrors, fmt.Sprintf("attempt %d (%s): %v", attempt+1, source.ID(), lastErr))
			} else {
				allErrors = append(allErrors, fmt.Sprintf("attempt %d (%s): incomplete data", attempt+1, source.ID()))
			}

			// Try a different source on failure
			tracker.recordFailure(source.ID())
			source = tracker.selectBest(sources)
		}

		if lastErr != nil {
			chunk.Error = fmt.Errorf("all retries failed: %v (history: %v)", lastErr, allErrors)
		} else if int64(len(data)) != chunk.End-chunk.Start {
			chunk.Error = fmt.Errorf("incomplete chunk: got %d, expected %d (history: %v)", len(data), chunk.End-chunk.Start, allErrors)
		} else {
			chunk.Data = data
			chunk.Source = source
			chunk.Duration = duration
			tracker.recordSuccess(source.ID(), int64(len(data)), duration)

			if d.metrics != nil {
				d.metrics.ChunkDownloadTime.Observe(duration.Seconds())
			}
		}

		results <- chunk
	}
}

// downloadRacing downloads from multiple sources simultaneously, using the first to complete
func (d *Downloader) downloadRacing(
	ctx context.Context,
	expectedHash string,
	peerSources []Source,
	mirrorSource Source,
	startTime time.Time,
) (*DownloadResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type raceResult struct {
		data   []byte
		source Source
		err    error
	}

	results := make(chan raceResult, len(peerSources)+1)
	var racers int32

	// Start peer downloads immediately
	for _, src := range peerSources {
		atomic.AddInt32(&racers, 1)
		go func(s Source) {
			data, err := s.DownloadFull(ctx, expectedHash)
			results <- raceResult{data, s, err}
		}(src)
	}

	// Start mirror with slight delay (give P2P a chance)
	if mirrorSource != nil {
		atomic.AddInt32(&racers, 1)
		go func() {
			select {
			case <-time.After(MirrorFallbackDelay):
			case <-ctx.Done():
				results <- raceResult{nil, mirrorSource, ctx.Err()}
				return
			}
			data, err := mirrorSource.DownloadFull(ctx, expectedHash)
			results <- raceResult{data, mirrorSource, err}
		}()
	}

	if atomic.LoadInt32(&racers) == 0 {
		return nil, ErrNoSources
	}

	// Collect results
	var lastErr error
	received := int32(0)

	for {
		select {
		case res := <-results:
			received++

			if res.err != nil {
				lastErr = res.err
				if received >= atomic.LoadInt32(&racers) {
					return nil, fmt.Errorf("%w: %v", ErrAllSourcesFailed, lastErr)
				}
				continue
			}

			// Verify hash
			actualHash := sha256.Sum256(res.data)
			actualHashHex := hex.EncodeToString(actualHash[:])

			if actualHashHex != expectedHash {
				if d.metrics != nil {
					d.metrics.VerificationFailures.Inc()
				}
				// Blacklist peer if hash mismatch
				if res.source.Type() == SourceTypePeer && d.scorer != nil {
					if ps, ok := res.source.(*PeerSource); ok {
						d.scorer.Blacklist(ps.Info.ID, "hash mismatch", 24*time.Hour)
					}
				}
				lastErr = ErrHashMismatch
				if received >= atomic.LoadInt32(&racers) {
					return nil, fmt.Errorf("%w: %v", ErrAllSourcesFailed, lastErr)
				}
				continue
			}

			// Success! Cancel other downloads
			cancel()

			sourceType := res.source.Type()
			var peerBytes, mirrorBytes int64
			if sourceType == SourceTypePeer {
				peerBytes = int64(len(res.data))
			} else {
				mirrorBytes = int64(len(res.data))
			}

			return &DownloadResult{
				Data:          res.data,
				Hash:          actualHashHex,
				Size:          int64(len(res.data)),
				Duration:      time.Since(startTime),
				Source:        sourceType,
				PeerBytes:     peerBytes,
				MirrorBytes:   mirrorBytes,
				ChunksTotal:   1,
				ChunksFromP2P: btoi(sourceType == SourceTypePeer),
			}, nil

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// sourceTracker tracks source performance for adaptive selection
type sourceTracker struct {
	mu    sync.RWMutex
	stats map[string]*sourceStats
}

type sourceStats struct {
	successCount int
	failureCount int
	totalBytes   int64
	totalTime    time.Duration
	lastFailure  time.Time
}

func (st *sourceTracker) selectBest(sources []Source) Source {
	st.mu.RLock()
	defer st.mu.RUnlock()

	type scored struct {
		source Source
		score  float64
	}

	scoredSources := make([]scored, 0, len(sources))

	for _, s := range sources {
		stats, ok := st.stats[s.ID()]
		var score float64

		if !ok {
			// Unknown source - give neutral score, slight preference for peers
			score = 0.5
			if s.Type() == SourceTypePeer {
				score = 0.55
			}
		} else {
			total := stats.successCount + stats.failureCount
			if total == 0 {
				score = 0.5
			} else {
				reliability := float64(stats.successCount) / float64(total)
				var throughput float64
				if stats.totalTime > 0 {
					throughput = float64(stats.totalBytes) / stats.totalTime.Seconds()
				}
				// Normalize throughput
				throughputScore := throughput / (throughput + ReferenceThroughput)

				score = 0.6*reliability + 0.4*throughputScore

				// Penalty for recent failure
				if time.Since(stats.lastFailure) < 10*time.Second {
					score *= 0.5
				}
			}
		}

		scoredSources = append(scoredSources, scored{s, score})
	}

	// Sort by score
	sort.Slice(scoredSources, func(i, j int) bool {
		return scoredSources[i].score > scoredSources[j].score
	})

	return scoredSources[0].source
}

func (st *sourceTracker) recordSuccess(id string, bytes int64, duration time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()

	s, ok := st.stats[id]
	if !ok {
		s = &sourceStats{}
		st.stats[id] = s
	}

	s.successCount++
	s.totalBytes += bytes
	s.totalTime += duration
}

func (st *sourceTracker) recordFailure(id string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	s, ok := st.stats[id]
	if !ok {
		s = &sourceStats{}
		st.stats[id] = s
	}

	s.failureCount++
	s.lastFailure = time.Now()
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
