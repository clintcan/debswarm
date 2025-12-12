// Package proxy provides an HTTP proxy server that intercepts APT requests
package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/downloader"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/p2p"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/timeouts"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// Server is the HTTP proxy server
type Server struct {
	addr       string
	cache      *cache.Cache
	index      *index.Index
	p2pNode    *p2p.Node
	fetcher    *mirror.Fetcher
	downloader *downloader.Downloader
	logger     *zap.Logger
	server     *http.Server
	metrics    *metrics.Metrics
	timeouts   *timeouts.Manager
	scorer     *peers.Scorer

	// Statistics (atomic)
	requestsTotal   int64
	requestsP2P     int64
	requestsMirror  int64
	bytesFromP2P    int64
	bytesFromMirror int64
	cacheHits       int64
	activeConns     int64

	// Configuration
	p2pTimeout     time.Duration
	dhtLookupLimit int
	metricsPort    int
}

// Config holds proxy server configuration
type Config struct {
	Addr           string
	P2PTimeout     time.Duration
	DHTLookupLimit int
	MetricsPort    int
	Metrics        *metrics.Metrics
	Timeouts       *timeouts.Manager
	Scorer         *peers.Scorer
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		Addr:           "127.0.0.1:9977",
		P2PTimeout:     5 * time.Second,
		DHTLookupLimit: 10,
		MetricsPort:    9978,
	}
}

// NewServer creates a new proxy server
func NewServer(
	cfg *Config,
	pkgCache *cache.Cache,
	idx *index.Index,
	node *p2p.Node,
	fetcher *mirror.Fetcher,
	logger *zap.Logger,
) *Server {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// Use provided or create new metrics/timeouts/scorer
	m := cfg.Metrics
	if m == nil {
		m = metrics.New()
	}

	tm := cfg.Timeouts
	if tm == nil {
		tm = timeouts.NewManager(nil)
	}

	scorer := cfg.Scorer
	if scorer == nil {
		scorer = peers.NewScorer()
	}

	s := &Server{
		addr:           cfg.Addr,
		cache:          pkgCache,
		index:          idx,
		p2pNode:        node,
		fetcher:        fetcher,
		logger:         logger,
		metrics:        m,
		timeouts:       tm,
		scorer:         scorer,
		p2pTimeout:     cfg.P2PTimeout,
		dhtLookupLimit: cfg.DHTLookupLimit,
		metricsPort:    cfg.MetricsPort,
	}

	// Create downloader with all the goodies
	s.downloader = downloader.New(&downloader.Config{
		ChunkSize:     downloader.DefaultChunkSize,
		MaxConcurrent: downloader.MaxConcurrentChunks,
		Scorer:        scorer,
		Metrics:       m,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRequest)

	s.server = &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

// Start starts the proxy server and metrics endpoint
func (s *Server) Start() error {
	// Start metrics server in background
	if s.metricsPort > 0 {
		go s.startMetricsServer()
	}

	s.logger.Info("Starting HTTP proxy", zap.String("addr", s.addr))
	return s.server.ListenAndServe()
}

func (s *Server) startMetricsServer() {
	mux := http.NewServeMux()
	mux.Handle("/metrics", s.metrics.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("/stats", s.handleStats)

	addr := fmt.Sprintf("127.0.0.1:%d", s.metricsPort)
	s.logger.Info("Starting metrics server", zap.String("addr", addr))

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		s.logger.Error("Metrics server failed", zap.Error(err))
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	stats := s.GetStats()
	p2pRatio := float64(0)
	if stats.BytesFromP2P+stats.BytesFromMirror > 0 {
		p2pRatio = float64(stats.BytesFromP2P) / float64(stats.BytesFromP2P+stats.BytesFromMirror) * 100
	}

	fmt.Fprintf(w, `{
  "requests_total": %d,
  "requests_p2p": %d,
  "requests_mirror": %d,
  "bytes_from_p2p": %d,
  "bytes_from_mirror": %d,
  "cache_hits": %d,
  "active_connections": %d,
  "p2p_ratio_percent": %.2f,
  "cache_size_bytes": %d,
  "cache_count": %d
}`,
		stats.RequestsTotal,
		stats.RequestsP2P,
		stats.RequestsMirror,
		stats.BytesFromP2P,
		stats.BytesFromMirror,
		stats.CacheHits,
		stats.ActiveConnections,
		p2pRatio,
		s.cache.Size(),
		s.cache.Count(),
	)
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// Stats holds proxy statistics
type Stats struct {
	RequestsTotal     int64
	RequestsP2P       int64
	RequestsMirror    int64
	BytesFromP2P      int64
	BytesFromMirror   int64
	CacheHits         int64
	ActiveConnections int64
}

// GetStats returns current statistics
func (s *Server) GetStats() Stats {
	return Stats{
		RequestsTotal:     atomic.LoadInt64(&s.requestsTotal),
		RequestsP2P:       atomic.LoadInt64(&s.requestsP2P),
		RequestsMirror:    atomic.LoadInt64(&s.requestsMirror),
		BytesFromP2P:      atomic.LoadInt64(&s.bytesFromP2P),
		BytesFromMirror:   atomic.LoadInt64(&s.bytesFromMirror),
		CacheHits:         atomic.LoadInt64(&s.cacheHits),
		ActiveConnections: atomic.LoadInt64(&s.activeConns),
	}
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&s.activeConns, 1)
	defer atomic.AddInt64(&s.activeConns, -1)
	atomic.AddInt64(&s.requestsTotal, 1)

	targetURL := s.extractTargetURL(r)
	if targetURL == "" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	s.logger.Debug("Proxy request",
		zap.String("method", r.Method),
		zap.String("url", targetURL))

	reqType := s.classifyRequest(targetURL)

	switch reqType {
	case requestTypePackage:
		s.handlePackageRequest(w, r, targetURL)
	case requestTypeIndex:
		s.handleIndexRequest(w, r, targetURL)
	case requestTypeRelease:
		s.handleReleaseRequest(w, r, targetURL)
	default:
		s.handlePassthrough(w, r, targetURL)
	}
}

type requestType int

const (
	requestTypeUnknown requestType = iota
	requestTypePackage
	requestTypeIndex
	requestTypeRelease
)

func (s *Server) classifyRequest(url string) requestType {
	lower := strings.ToLower(url)

	if strings.HasSuffix(lower, ".deb") {
		return requestTypePackage
	}

	if strings.Contains(lower, "/packages") ||
		strings.Contains(lower, "/sources") {
		return requestTypeIndex
	}

	if strings.Contains(lower, "/release") ||
		strings.Contains(lower, "/inrelease") {
		return requestTypeRelease
	}

	return requestTypeUnknown
}

func (s *Server) extractTargetURL(r *http.Request) string {
	path := strings.TrimPrefix(r.URL.Path, "/")

	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}

	if strings.Contains(path, "/") {
		return "http://" + path
	}

	return ""
}

func (s *Server) handlePackageRequest(w http.ResponseWriter, r *http.Request, url string) {
	ctx := r.Context()
	start := time.Now()

	// Find expected hash from index
	path := index.ExtractPathFromURL(url)
	var expectedHash string
	var expectedSize int64

	if path != "" {
		if pkg := s.index.GetByPath(path); pkg != nil {
			expectedHash = pkg.SHA256
			expectedSize = pkg.Size
			s.logger.Debug("Found package in index",
				zap.String("path", path),
				zap.String("hash", expectedHash[:16]+"..."),
				zap.Int64("size", expectedSize))
		}
	}

	// Check local cache first
	if expectedHash != "" && s.cache.Has(expectedHash) {
		s.logger.Debug("Cache hit", zap.String("hash", expectedHash[:16]+"..."))
		atomic.AddInt64(&s.cacheHits, 1)
		s.metrics.CacheHits.Inc()
		s.serveFromCache(w, expectedHash)
		return
	}

	s.metrics.CacheMisses.Inc()

	// Build download sources
	var peerSources []downloader.Source
	var mirrorSource downloader.Source

	// Find P2P providers if we have a hash
	if expectedHash != "" && s.p2pNode != nil {
		dhtCtx, dhtCancel := context.WithTimeout(ctx, s.timeouts.Get(timeouts.OpDHTLookup))
		providers, err := s.p2pNode.FindProvidersRanked(dhtCtx, expectedHash, s.dhtLookupLimit)
		dhtCancel()

		if err == nil && len(providers) > 0 {
			s.logger.Debug("Found P2P providers",
				zap.String("hash", expectedHash[:16]+"..."),
				zap.Int("count", len(providers)))

			for _, p := range providers {
				peerSources = append(peerSources, &downloader.PeerSource{
					Info: p,
					Downloader: func(ctx context.Context, info peer.AddrInfo, hash string, start, end int64) ([]byte, error) {
						return s.p2pNode.DownloadRange(ctx, info, hash, start, end)
					},
				})
			}
		}
	}

	// Add mirror source
	mirrorSource = &downloader.MirrorSource{
		URL: url,
		Fetcher: func(ctx context.Context, url string, start, end int64) ([]byte, error) {
			// For now, only full downloads from mirror
			// TODO: Add range request support to mirror fetcher
			return s.fetcher.Fetch(ctx, url)
		},
	}

	// Use parallel downloader for large files with available peers
	if expectedHash != "" && expectedSize > 0 && len(peerSources) > 0 {
		result, err := s.downloader.Download(ctx, expectedHash, expectedSize, peerSources, mirrorSource)
		if err == nil {
			s.handleDownloadSuccess(w, result, expectedHash, path, start)
			return
		}
		s.logger.Debug("Parallel download failed, falling back to mirror", zap.Error(err))
	}

	// Fallback: try simple P2P then mirror
	if expectedHash != "" && len(peerSources) > 0 {
		for _, src := range peerSources[:min(3, len(peerSources))] {
			peerCtx, peerCancel := context.WithTimeout(ctx, s.p2pTimeout)
			data, err := src.DownloadFull(peerCtx, expectedHash)
			peerCancel()

			if err != nil {
				continue
			}

			// Verify hash
			actualHash := sha256.Sum256(data)
			actualHashHex := hex.EncodeToString(actualHash[:])

			if actualHashHex == expectedHash {
				s.logger.Debug("Downloaded from P2P",
					zap.String("hash", expectedHash[:16]+"..."),
					zap.Int("size", len(data)))

				atomic.AddInt64(&s.requestsP2P, 1)
				atomic.AddInt64(&s.bytesFromP2P, int64(len(data)))
				s.metrics.DownloadsTotal.WithLabel(downloader.SourceTypePeer).Inc()
				s.metrics.BytesDownloaded.WithLabel(downloader.SourceTypePeer).Add(int64(len(data)))

				// Cache and serve
				s.cacheAndServe(w, data, expectedHash, path)
				return
			} else {
				s.logger.Warn("P2P hash mismatch, blacklisting peer")
				s.metrics.VerificationFailures.Inc()
				if ps, ok := src.(*downloader.PeerSource); ok {
					s.scorer.Blacklist(ps.Info.ID, "hash mismatch", 24*time.Hour)
				}
			}
		}
	}

	// Final fallback: mirror
	s.logger.Debug("Falling back to mirror", zap.String("url", url))
	atomic.AddInt64(&s.requestsMirror, 1)

	data, err := s.fetcher.Fetch(ctx, url)
	if err != nil {
		s.logger.Error("Mirror fetch failed", zap.Error(err))
		http.Error(w, "Failed to fetch package", http.StatusBadGateway)
		return
	}

	atomic.AddInt64(&s.bytesFromMirror, int64(len(data)))
	s.metrics.DownloadsTotal.WithLabel(downloader.SourceTypeMirror).Inc()
	s.metrics.BytesDownloaded.WithLabel(downloader.SourceTypeMirror).Add(int64(len(data)))

	// Verify and cache if we have expected hash
	if expectedHash != "" {
		actualHash := sha256.Sum256(data)
		actualHashHex := hex.EncodeToString(actualHash[:])
		if actualHashHex == expectedHash {
			s.cacheAndAnnounce(data, expectedHash, path)
		} else {
			s.logger.Warn("Mirror hash mismatch",
				zap.String("expected", expectedHash),
				zap.String("actual", actualHashHex))
		}
	}

	// Serve
	w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleDownloadSuccess(w http.ResponseWriter, result *downloader.DownloadResult, expectedHash, path string, start time.Time) {
	// Update stats
	atomic.AddInt64(&s.bytesFromP2P, result.PeerBytes)
	atomic.AddInt64(&s.bytesFromMirror, result.MirrorBytes)

	if result.PeerBytes > result.MirrorBytes {
		atomic.AddInt64(&s.requestsP2P, 1)
	} else {
		atomic.AddInt64(&s.requestsMirror, 1)
	}

	s.logger.Info("Download complete",
		zap.String("hash", expectedHash[:16]+"..."),
		zap.Int64("size", result.Size),
		zap.String("source", result.Source),
		zap.Int64("peerBytes", result.PeerBytes),
		zap.Int64("mirrorBytes", result.MirrorBytes),
		zap.Int("chunks", result.ChunksTotal),
		zap.Int("chunksP2P", result.ChunksFromP2P),
		zap.Duration("duration", result.Duration))

	// Cache and announce
	s.cacheAndAnnounce(result.Data, expectedHash, path)

	// Serve
	w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", result.Size))
	w.Header().Set("X-Debswarm-Source", result.Source)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Data)
}

func (s *Server) cacheAndServe(w http.ResponseWriter, data []byte, hash, path string) {
	// Cache
	if err := s.cache.Put(bytes.NewReader(data), hash, path); err != nil {
		s.logger.Warn("Failed to cache", zap.Error(err))
	}

	// Announce to DHT
	s.announceAsync(hash)

	// Serve
	w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("X-Debswarm-Source", downloader.SourceTypePeer)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) cacheAndAnnounce(data []byte, hash, path string) {
	if err := s.cache.Put(bytes.NewReader(data), hash, path); err != nil {
		s.logger.Warn("Failed to cache", zap.Error(err))
		return
	}
	s.announceAsync(hash)
}

func (s *Server) announceAsync(hash string) {
	if s.p2pNode == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.p2pNode.Provide(ctx, hash); err != nil {
			s.logger.Debug("Failed to announce", zap.Error(err))
		}
	}()
}

func (s *Server) handleIndexRequest(w http.ResponseWriter, r *http.Request, url string) {
	ctx := r.Context()

	s.logger.Debug("Fetching index", zap.String("url", url))

	data, err := s.fetcher.Fetch(ctx, url)
	if err != nil {
		s.logger.Error("Failed to fetch index", zap.Error(err))
		http.Error(w, "Failed to fetch index", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleReleaseRequest(w http.ResponseWriter, r *http.Request, url string) {
	s.handlePassthrough(w, r, url)
}

func (s *Server) handlePassthrough(w http.ResponseWriter, r *http.Request, url string) {
	ctx := r.Context()

	data, err := s.fetcher.Fetch(ctx, url)
	if err != nil {
		s.logger.Error("Passthrough fetch failed", zap.Error(err))
		http.Error(w, "Failed to fetch", http.StatusBadGateway)
		return
	}

	atomic.AddInt64(&s.requestsMirror, 1)
	atomic.AddInt64(&s.bytesFromMirror, int64(len(data)))

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) serveFromCache(w http.ResponseWriter, hash string) {
	reader, pkg, err := s.cache.Get(hash)
	if err != nil {
		s.logger.Error("Cache read failed", zap.Error(err))
		http.Error(w, "Cache error", http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", pkg.Size))
	w.Header().Set("X-Debswarm-Source", "cache")
	w.WriteHeader(http.StatusOK)

	_, _ = io.Copy(w, reader)
}

// SetP2PNode sets the P2P node
func (s *Server) SetP2PNode(node *p2p.Node) {
	s.p2pNode = node
	s.scorer = node.Scorer()
	s.timeouts = node.Timeouts()

	// Set up content getter for serving to peers
	node.SetContentGetter(func(sha256Hash string) (io.ReadCloser, int64, error) {
		reader, pkg, err := s.cache.Get(sha256Hash)
		if err != nil {
			return nil, 0, err
		}
		return reader, pkg.Size, nil
	})
}

// LoadIndex loads a package index from URL
func (s *Server) LoadIndex(url string) error {
	return s.index.LoadFromURL(url)
}

// ReannouncePackages announces all cached packages to the DHT
func (s *Server) ReannouncePackages(ctx context.Context) error {
	if s.p2pNode == nil {
		return nil
	}

	packages, err := s.cache.GetUnannounced()
	if err != nil {
		return err
	}

	s.logger.Info("Reannouncing packages", zap.Int("count", len(packages)))

	for _, pkg := range packages {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := s.p2pNode.Provide(ctx, pkg.SHA256); err != nil {
			s.logger.Debug("Failed to announce package",
				zap.String("hash", pkg.SHA256[:16]+"..."),
				zap.Error(err))
			continue
		}

		if err := s.cache.MarkAnnounced(pkg.SHA256); err != nil {
			s.logger.Warn("Failed to mark as announced", zap.Error(err))
		}
	}

	return nil
}

// UpdateMetrics updates metrics from current state
func (s *Server) UpdateMetrics() {
	if s.metrics == nil {
		return
	}

	s.metrics.CacheSize.Set(float64(s.cache.Size()))
	s.metrics.CacheCount.Set(float64(s.cache.Count()))

	if s.p2pNode != nil {
		s.metrics.ConnectedPeers.Set(float64(s.p2pNode.ConnectedPeers()))
		s.metrics.RoutingTableSize.Set(float64(s.p2pNode.RoutingTableSize()))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
