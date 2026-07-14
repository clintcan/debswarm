// Package proxy provides an HTTP proxy server that intercepts APT requests
package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/debswarm/debswarm/internal/audit"
	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/connectivity"
	"github.com/debswarm/debswarm/internal/dashboard"
	"github.com/debswarm/debswarm/internal/downloader"
	"github.com/debswarm/debswarm/internal/fleet"
	"github.com/debswarm/debswarm/internal/gpg"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/p2p"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/requestid"
	"github.com/debswarm/debswarm/internal/sanitize"
	"github.com/debswarm/debswarm/internal/scheduler"
	"github.com/debswarm/debswarm/internal/security"
	"github.com/debswarm/debswarm/internal/timeouts"
	"github.com/debswarm/debswarm/internal/verify"
)

// Server is the HTTP proxy server
type Server struct {
	addr         string
	cache        *cache.Cache
	index        *index.Index
	p2pNode      *p2p.Node
	fetcher      *mirror.Fetcher
	downloader   *downloader.Downloader
	stateManager *downloader.StateManager
	logger       *zap.Logger
	server       *http.Server
	metrics      *metrics.Metrics
	timeouts     *timeouts.Manager
	scorer       *peers.Scorer
	audit        audit.Logger
	connectivity *connectivity.Monitor
	scheduler    *scheduler.Scheduler
	fleet        *fleet.Coordinator
	verifier     *verify.Verifier

	// Statistics (atomic)
	requestsTotal   int64
	requestsP2P     int64
	requestsMirror  int64
	bytesFromP2P    int64
	bytesFromMirror int64
	cacheHits       int64
	activeConns     int64

	// Metadata (repository index) cache statistics (atomic).
	metadataHits       int64 // metadata served from cache (immutable hit or upstream 304)
	metadataMisses     int64 // metadata fetched fresh from the mirror (200)
	metadataBytesSaved int64 // body bytes served from cache instead of the WAN

	// CONNECT tunnel statistics (atomic)
	connectTotal   int64
	connectFailed  int64
	activeTunnels  int64
	tunnelBytesIn  int64
	tunnelBytesOut int64

	// Configuration
	p2pTimeout     time.Duration
	dhtLookupLimit int
	metricsPort    int
	metricsBind    string

	// Announcement worker pool (bounded)
	announceChan   chan string
	announceDone   chan struct{}
	announceCtx    context.Context
	announceCancel context.CancelFunc

	// Dashboard
	dashboard    *dashboard.Dashboard
	cacheMaxSize int64

	// Request coalescing - prevents duplicate downloads for same package
	downloadGroup singleflight.Group

	// Retry configuration
	retryMaxAttempts int
	retryInterval    time.Duration
	retryMaxAge      time.Duration
	retryCtx         context.Context
	retryCancel      context.CancelFunc
	retryDone        chan struct{}

	// Security configuration
	allowedHosts       []string     // Additional allowed repository hosts
	httpsUpstreamHosts []string     // Hosts to fetch over HTTPS even when APT requests HTTP
	metadataServeStale bool         // serve cached metadata when the mirror is unreachable
	allowedClientNets  []*net.IPNet // inbound client allowlist for LAN server mode (empty = loopback only)

	// Upstream GPG verification: verify a Packages index against the GPG-signed
	// Release before trusting its hashes. verifyMode is "off" (disabled), "warn"
	// (verify + observe, serve unchanged), "auto" (default; refuse only a decisive
	// failure — the signed Release exists but the index does not match it), or
	// "enforce" (refuse any unverified/mismatched index). keyring holds the trusted
	// public keys; verifyExempt lists hosts served even when unverifiable (auto and
	// enforce); releaseStore caches verified, parsed Release files per base.
	verifyMode   string
	keyring      *gpg.Keyring
	verifyExempt map[string]bool
	releaseStore *releaseStore
	// On-demand Release fetch (enforce only): when the signed Release for a base was
	// never cached (e.g. the client's apt held a current InRelease so our conditional
	// GET relayed a 304 with no body), fetch it so enforce does not falsely refuse a
	// verifiable index. releaseFetch dedups concurrent fetches per base;
	// releaseFetchFailed negative-caches a base whose fetch failed so it is not
	// re-fetched on every request.
	releaseFetch       singleflight.Group
	releaseFetchFailed sync.Map // base(string) -> time.Time of last failed fetch

	// uncachedHostsSeen tracks repository hosts for which we have already logged
	// an INFO-level "served uncached" notice, so the log is emitted once per host
	// (the packages_served_uncached_total metric carries the full count).
	uncachedHostsSeen sync.Map

	// staleHostsSeen tracks repository hosts for which we have already logged an
	// INFO-level "serving stale metadata" notice (once per host).
	staleHostsSeen sync.Map

	// indexWarmOnce guards a one-shot warm of the in-memory index from cached
	// Packages metadata, so a cached .deb resolves to its SHA256 after a restart
	// even when no apt-get update has run this session (the case that otherwise
	// breaks offline installs of already-cached packages).
	indexWarmOnce sync.Once
}

// Config holds proxy server configuration
type Config struct {
	Addr                       string
	P2PTimeout                 time.Duration
	DHTLookupLimit             int
	MetricsPort                int
	MetricsBind                string // Bind address for metrics server (default: 127.0.0.1)
	CacheMaxSize               int64
	MaxConcurrentPeerDownloads int // Maximum concurrent peer downloads (0 = default)
	Metrics                    *metrics.Metrics
	Timeouts                   *timeouts.Manager
	Scorer                     *peers.Scorer
	Audit                      audit.Logger          // Audit logger for structured event logging
	Connectivity               *connectivity.Monitor // Connectivity monitor for offline-first mode
	Scheduler                  *scheduler.Scheduler  // Scheduler for time-based rate limiting
	Fleet                      *fleet.Coordinator    // Fleet coordinator for LAN download coordination
	Verifier                   *verify.Verifier      // Multi-source verifier for download validation
	// Retry settings
	RetryMaxAttempts int           // Max retry attempts per download (0 = disabled)
	RetryInterval    time.Duration // How often to check for failed downloads
	RetryMaxAge      time.Duration // Don't retry downloads older than this

	// Security settings
	AllowedHosts []string // Additional allowed repository hosts (beyond built-in Debian/Ubuntu/Mint)

	// AllowedClientCIDRs restricts which inbound clients may use the proxy when it
	// is bound to a non-loopback address (LAN server mode). Loopback clients are
	// always allowed. Empty means loopback-only (the default). Parsed from
	// network.proxy_allowed_cidrs by the daemon.
	AllowedClientCIDRs []*net.IPNet

	// HTTPSUpstreamHosts lists hosts for which the proxy upgrades an incoming
	// plain-HTTP APT request to an HTTPS upstream fetch (MITM-free), enabling
	// caching and P2P sharing of HTTPS-only repositories.
	HTTPSUpstreamHosts []string

	// MetadataServeStale lets the proxy serve a cached metadata copy when the
	// mirror is unreachable (or connectivity is offline) instead of failing the
	// request, so apt-get update keeps working offline. APT still verifies the
	// signature and Valid-Until of whatever is served.
	MetadataServeStale bool

	// VerifyMode controls daemon-side upstream signature verification: "" or "off"
	// (disabled, unchanged behavior), "warn" (verify + observe, serve unchanged),
	// or "enforce" (refuse an unverified/mismatched index). Keyring holds the
	// trusted public keys (nil disables verification). VerifyExemptHosts lists
	// repository hosts served even when unverifiable (enforce only).
	VerifyMode        string
	Keyring           *gpg.Keyring
	VerifyExemptHosts []string
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

	auditLogger := cfg.Audit
	if auditLogger == nil {
		auditLogger = &audit.NoopLogger{}
	}

	// Default metrics bind to localhost if not specified
	metricsBind := cfg.MetricsBind
	if metricsBind == "" {
		metricsBind = "127.0.0.1"
	}

	s := &Server{
		addr:               cfg.Addr,
		cache:              pkgCache,
		index:              idx,
		p2pNode:            node,
		fetcher:            fetcher,
		logger:             logger,
		metrics:            m,
		timeouts:           tm,
		scorer:             scorer,
		audit:              auditLogger,
		connectivity:       cfg.Connectivity,
		scheduler:          cfg.Scheduler,
		fleet:              cfg.Fleet,
		verifier:           cfg.Verifier,
		p2pTimeout:         cfg.P2PTimeout,
		dhtLookupLimit:     cfg.DHTLookupLimit,
		metricsPort:        cfg.MetricsPort,
		metricsBind:        metricsBind,
		cacheMaxSize:       cfg.CacheMaxSize,
		announceChan:       make(chan string, 100), // Bounded buffer
		announceDone:       make(chan struct{}),
		retryMaxAttempts:   cfg.RetryMaxAttempts,
		retryInterval:      cfg.RetryInterval,
		retryMaxAge:        cfg.RetryMaxAge,
		retryDone:          make(chan struct{}),
		allowedHosts:       cfg.AllowedHosts,
		httpsUpstreamHosts: cfg.HTTPSUpstreamHosts,
		metadataServeStale: cfg.MetadataServeStale,
		allowedClientNets:  cfg.AllowedClientCIDRs,
	}

	// Upstream GPG verification setup (default off preserves existing behavior).
	s.verifyMode = cfg.VerifyMode
	if s.verifyMode == "" {
		s.verifyMode = verifyOff
	}
	s.keyring = cfg.Keyring
	s.releaseStore = newReleaseStore()
	if len(cfg.VerifyExemptHosts) > 0 {
		s.verifyExempt = make(map[string]bool, len(cfg.VerifyExemptHosts))
		for _, h := range cfg.VerifyExemptHosts {
			if h = strings.TrimSpace(strings.ToLower(h)); h != "" {
				s.verifyExempt[h] = true
			}
		}
	}

	// Create context for announcement worker that will be canceled on shutdown
	s.announceCtx, s.announceCancel = context.WithCancel(context.Background())

	// Start announcement worker (bounded goroutines)
	go s.announcementWorker()

	// Create context for retry worker
	s.retryCtx, s.retryCancel = context.WithCancel(context.Background())

	// Create state manager for download resume support
	stateManager := downloader.NewStateManager(pkgCache.GetDB())
	s.stateManager = stateManager

	// Expose the cache's capacity and eviction pressure to operators
	if m != nil {
		m.CacheMaxSize.Set(float64(pkgCache.MaxSize()))
		pkgCache.SetOnEvict(func() { m.CacheEvictions.Inc() })
	}

	// Determine max concurrent downloads (use config or default)
	maxConcurrentDownloads := cfg.MaxConcurrentPeerDownloads
	if maxConcurrentDownloads <= 0 {
		maxConcurrentDownloads = downloader.MaxConcurrentChunks
	}

	// Create downloader with all the goodies
	s.downloader = downloader.New(&downloader.Config{
		ChunkSize:     downloader.DefaultChunkSize,
		MaxConcurrent: maxConcurrentDownloads,
		Scorer:        scorer,
		Metrics:       m,
		StateManager:  stateManager,
		Cache:         pkgCache,
	})

	// Warn when the proxy is exposed beyond loopback. The daemon's fail-closed
	// validation guarantees a client allowlist is present in that case, but a
	// visible startup warning helps operators confirm the exposure is intended.
	if host, _, err := net.SplitHostPort(cfg.Addr); err == nil && !bindIsLoopback(host) {
		logger.Warn("Proxy bound to a non-loopback address (LAN server mode); inbound clients are restricted to network.proxy_allowed_cidrs",
			zap.String("bind", host),
			zap.Int("allowedCIDRs", len(cfg.AllowedClientCIDRs)))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRequest)

	s.server = &http.Server{
		Addr: cfg.Addr,
		// gateClient enforces the inbound client allowlist (loopback always
		// allowed). It is a no-op when the proxy is bound to loopback.
		Handler: s.gateClient(mux),
		// ReadHeaderTimeout (not a blanket ReadTimeout) guards against slow-loris
		// header sends. A full ReadTimeout is a deadline on the whole request
		// lifecycle of a connection; on a keep-alive/pipelined connection — which
		// APT uses by default — it fires mid-handler once a large index response
		// (e.g. an 8 MB Packages file) plus APT's pipelining pushes the cycle past
		// the limit, canceling the request context and stalling `apt-get update`.
		ReadHeaderTimeout: 30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
	}

	return s
}

// Start starts the proxy server and metrics endpoint
func (s *Server) Start() error {
	// Start metrics server in background
	if s.metricsPort > 0 {
		go s.startMetricsServer()
	}

	// Start retry worker if enabled
	if s.retryMaxAttempts > 0 && s.retryInterval > 0 {
		go s.retryWorker()
	}

	s.logger.Info("Starting HTTP proxy", zap.String("addr", s.addr))
	return s.server.ListenAndServe()
}

func (s *Server) startMetricsServer() {
	mux := http.NewServeMux()
	mux.Handle("/metrics", s.metrics.Handler())
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/stats", s.handleStats)
	s.registerAPIRoutes(mux)

	// Add dashboard routes if dashboard is set
	if s.dashboard != nil {
		dashHandler := s.dashboard.Handler()
		mux.Handle("/dashboard", dashHandler)
		mux.Handle("/dashboard/", http.StripPrefix("/dashboard", dashHandler))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/dashboard", http.StatusTemporaryRedirect)
				return
			}
			http.NotFound(w, r)
		})
	}

	// Add pprof endpoints for runtime profiling — only on localhost to prevent
	// unauthorized access when metrics bind address is non-local.
	if s.metricsBind == "127.0.0.1" || s.metricsBind == "localhost" || s.metricsBind == "::1" {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	addr := net.JoinHostPort(s.metricsBind, strconv.Itoa(s.metricsPort))
	s.logger.Info("Starting metrics server", zap.String("addr", addr))

	// When a client allowlist is configured, apply it to the admin read surface as
	// well (loopback always allowed), so binding the admin server to the LAN does
	// not expose stats/dashboard/cache-inventory to every reachable host. With no
	// allowlist set we keep the historical warn-only behavior so existing
	// non-loopback deployments are not broken on upgrade — only warned.
	handler := http.Handler(mux)
	if !bindIsLoopback(s.metricsBind) {
		if len(s.allowedClientNets) > 0 {
			handler = s.gateClient(mux)
			s.logger.Warn("Metrics/admin server bound to a non-loopback address; read endpoints are restricted to network.proxy_allowed_cidrs",
				zap.String("bind", s.metricsBind))
		} else {
			s.logger.Warn("Metrics/admin server bound to a non-loopback address with no network.proxy_allowed_cidrs set - stats, dashboard, and cache inventory are readable by any reachable host; set proxy_allowed_cidrs to restrict access",
				zap.String("bind", s.metricsBind))
		}
	}

	server := &http.Server{
		Addr:           addr,
		Handler:        handler,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		s.logger.Error("Metrics server failed", zap.Error(err))
	}
}

// bindIsLoopback reports whether a bind host restricts a listener to the local
// machine. Empty and "localhost" count as loopback; "0.0.0.0"/"::" (all
// interfaces) and any specific interface IP do not.
func bindIsLoopback(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clientAllowed reports whether the request's client is permitted. Loopback is
// always allowed; otherwise the client IP must fall inside one of the configured
// allowlist networks. The decision is made on the real TCP peer address only —
// X-Forwarded-For and similar client-supplied headers are never consulted.
func (s *Server) clientAllowed(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range s.allowedClientNets {
		if n != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// gateClient wraps a handler with the inbound client allowlist. It is a no-op
// for loopback clients, so a loopback-bound proxy (the default) is unaffected.
func (s *Server) gateClient(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.clientAllowed(r) {
			// Deliberately terse: do not disclose the allowlist to rejected clients.
			http.Error(w, "forbidden: client not permitted", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// setSecurityHeaders adds security headers to HTTP responses
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("X-XSS-Protection", "1; mode=block")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
}

// HealthStatus represents the health check response
type HealthStatus struct {
	Status           string            `json:"status"`
	Checks           map[string]string `json:"checks"`
	ConnectedPeers   int               `json:"connected_peers"`
	RoutingTableSize int               `json:"routing_table_size"`
	ConnectivityMode string            `json:"connectivity_mode,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")

	health := HealthStatus{
		Status: "healthy",
		Checks: make(map[string]string),
	}

	allHealthy := true

	// Check P2P node
	if s.p2pNode != nil {
		health.ConnectedPeers = s.p2pNode.ConnectedPeers()
		health.RoutingTableSize = s.p2pNode.RoutingTableSize()

		if health.RoutingTableSize > 0 {
			health.Checks["dht"] = "ok"
		} else {
			health.Checks["dht"] = "no_peers"
			// Not a failure - DHT may still be bootstrapping
		}

		if health.ConnectedPeers > 0 {
			health.Checks["p2p"] = "ok"
		} else {
			health.Checks["p2p"] = "no_connections"
		}
	} else {
		health.Checks["p2p"] = "not_initialized"
		allHealthy = false
	}

	// Check cache
	if s.cache != nil {
		health.Checks["cache"] = "ok"
	} else {
		health.Checks["cache"] = "not_initialized"
		allHealthy = false
	}

	// Add connectivity mode
	if s.connectivity != nil {
		health.ConnectivityMode = s.connectivity.GetMode().String()
	}

	// Set overall status
	if !allHealthy {
		health.Status = "unhealthy"
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	if err := json.NewEncoder(w).Encode(health); err != nil {
		s.logger.Warn("Failed to encode health response", zap.Error(err))
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")

	stats := s.GetStats()
	p2pRatio := float64(0)
	if stats.BytesFromP2P+stats.BytesFromMirror > 0 {
		p2pRatio = float64(stats.BytesFromP2P) / float64(stats.BytesFromP2P+stats.BytesFromMirror) * 100
	}

	// Get scheduler status if available
	var schedStatus *scheduler.Status
	if s.scheduler != nil {
		status := s.scheduler.Status()
		schedStatus = &status
	}

	// Get fleet status if available
	var fleetStatus *fleet.Status
	if s.fleet != nil {
		status := s.fleet.Status()
		fleetStatus = &status
	}

	response := struct {
		RequestsTotal       int64             `json:"requests_total"`
		RequestsP2P         int64             `json:"requests_p2p"`
		RequestsMirror      int64             `json:"requests_mirror"`
		BytesFromP2P        int64             `json:"bytes_from_p2p"`
		BytesFromMirror     int64             `json:"bytes_from_mirror"`
		CacheHits           int64             `json:"cache_hits"`
		ActiveConnections   int64             `json:"active_connections"`
		P2PRatioPercent     float64           `json:"p2p_ratio_percent"`
		CacheSizeBytes      int64             `json:"cache_size_bytes"`
		CacheCount          int               `json:"cache_count"`
		PackagesUncached    int64             `json:"packages_served_uncached"`
		MetadataCacheHits   int64             `json:"metadata_cache_hits"`
		MetadataCacheMiss   int64             `json:"metadata_cache_misses"`
		MetadataBytesSaved  int64             `json:"metadata_cache_bytes_saved"`
		MetadataCacheSize   int64             `json:"metadata_cache_size_bytes"`
		MetadataStaleServed int64             `json:"metadata_cache_stale_served"`
		Scheduler           *scheduler.Status `json:"scheduler,omitempty"`
		Fleet               *fleet.Status     `json:"fleet,omitempty"`
	}{
		RequestsTotal:       stats.RequestsTotal,
		RequestsP2P:         stats.RequestsP2P,
		RequestsMirror:      stats.RequestsMirror,
		BytesFromP2P:        stats.BytesFromP2P,
		BytesFromMirror:     stats.BytesFromMirror,
		CacheHits:           stats.CacheHits,
		ActiveConnections:   stats.ActiveConnections,
		P2PRatioPercent:     p2pRatio,
		CacheSizeBytes:      s.cache.Size(),
		CacheCount:          s.cache.Count(),
		PackagesUncached:    s.metrics.PackagesServedUncached.Value(),
		MetadataCacheHits:   stats.MetadataHits,
		MetadataCacheMiss:   stats.MetadataMisses,
		MetadataBytesSaved:  stats.MetadataBytesSaved,
		MetadataCacheSize:   s.cache.MetadataSize(),
		MetadataStaleServed: s.metrics.MetadataCacheStaleServed.Value(),
		Scheduler:           schedStatus,
		Fleet:               fleetStatus,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Debug("Failed to encode stats response", zap.Error(err))
	}
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	// Cancel announcement context to stop in-flight announcements
	s.announceCancel()

	// Cancel retry context to stop retry worker
	if s.retryCancel != nil {
		s.retryCancel()
	}

	// Wait for the announcement worker to drain in-flight announcements. It stops
	// on announceCtx cancellation (above); the channel is intentionally NOT closed,
	// because in-flight request/retry goroutines may still call announceAsync during
	// shutdown, and a send on a closed channel would panic.
	select {
	case <-s.announceDone:
	case <-ctx.Done():
		// Timeout waiting for announcements
	}

	// Wait for retry worker to finish
	if s.retryDone != nil {
		select {
		case <-s.retryDone:
		case <-ctx.Done():
			// Timeout waiting for retry worker
		}
	}

	// Note: verifier.Close() is called by daemon.go's defer, not here
	// to avoid double-close and maintain consistent cleanup ordering

	return s.server.Shutdown(ctx)
}

// Stats holds proxy statistics
type Stats struct {
	RequestsTotal      int64
	RequestsP2P        int64
	RequestsMirror     int64
	BytesFromP2P       int64
	BytesFromMirror    int64
	CacheHits          int64
	ActiveConnections  int64
	MetadataHits       int64
	MetadataMisses     int64
	MetadataBytesSaved int64
}

// GetStats returns current statistics
func (s *Server) GetStats() Stats {
	return Stats{
		RequestsTotal:      atomic.LoadInt64(&s.requestsTotal),
		RequestsP2P:        atomic.LoadInt64(&s.requestsP2P),
		RequestsMirror:     atomic.LoadInt64(&s.requestsMirror),
		BytesFromP2P:       atomic.LoadInt64(&s.bytesFromP2P),
		BytesFromMirror:    atomic.LoadInt64(&s.bytesFromMirror),
		CacheHits:          atomic.LoadInt64(&s.cacheHits),
		ActiveConnections:  atomic.LoadInt64(&s.activeConns),
		MetadataHits:       atomic.LoadInt64(&s.metadataHits),
		MetadataMisses:     atomic.LoadInt64(&s.metadataMisses),
		MetadataBytesSaved: atomic.LoadInt64(&s.metadataBytesSaved),
	}
}

// SetDashboard sets the dashboard for the server
func (s *Server) SetDashboard(d *dashboard.Dashboard) {
	s.dashboard = d
}

// GetDashboardStats returns stats in dashboard format
func (s *Server) GetDashboardStats() *dashboard.Stats {
	stats := s.GetStats()

	// Calculate P2P ratio
	p2pRatio := float64(0)
	if stats.BytesFromP2P+stats.BytesFromMirror > 0 {
		p2pRatio = float64(stats.BytesFromP2P) / float64(stats.BytesFromP2P+stats.BytesFromMirror) * 100
	}

	// Calculate cache usage
	cacheUsage := float64(0)
	if s.cacheMaxSize > 0 {
		cacheUsage = float64(s.cache.Size()) / float64(s.cacheMaxSize) * 100
	}

	// Get P2P stats
	connectedPeers := 0
	routingTableSize := 0
	if s.p2pNode != nil {
		connectedPeers = s.p2pNode.ConnectedPeers()
		routingTableSize = s.p2pNode.RoutingTableSize()
	}

	return &dashboard.Stats{
		RequestsTotal:        stats.RequestsTotal,
		RequestsP2P:          stats.RequestsP2P,
		RequestsMirror:       stats.RequestsMirror,
		BytesFromP2P:         stats.BytesFromP2P,
		BytesFromMirror:      stats.BytesFromMirror,
		CacheHits:            stats.CacheHits,
		P2PRatioPercent:      p2pRatio,
		CacheSizeBytes:       s.cache.Size(),
		CacheCount:           s.cache.Count(),
		CacheMaxSize:         formatBytes(s.cacheMaxSize),
		CacheUsagePercent:    cacheUsage,
		ConnectedPeers:       connectedPeers,
		RoutingTableSize:     routingTableSize,
		ActiveDownloads:      int(s.metrics.ActiveDownloads.Value()),
		ActiveUploads:        int(s.metrics.ActiveUploads.Value()),
		VerificationFailures: s.metrics.VerificationFailures.Value(),
	}
}

// GetPeerInfo returns peer information for the dashboard
func (s *Server) GetPeerInfo() []dashboard.PeerInfo {
	if s.p2pNode == nil {
		return nil
	}

	peerStats := s.p2pNode.GetPeerStats()
	result := make([]dashboard.PeerInfo, 0, len(peerStats))

	for _, ps := range peerStats {
		shortID := ps.PeerID.String()
		if len(shortID) > 12 {
			shortID = shortID[:6] + "..." + shortID[len(shortID)-6:]
		}

		// Get score from scorer
		score := s.scorer.GetScore(ps.PeerID)

		// Determine category based on score
		category := "Unknown"
		if ps.Blacklisted {
			category = "Blacklisted"
		} else if score >= 0.8 {
			category = "Excellent"
		} else if score >= 0.6 {
			category = "Good"
		} else if score >= 0.4 {
			category = "Fair"
		} else if ps.TotalRequests > 0 {
			category = "Poor"
		}

		result = append(result, dashboard.PeerInfo{
			ID:          ps.PeerID.String(),
			ShortID:     shortID,
			Score:       score,
			Category:    category,
			Latency:     formatDuration(time.Duration(ps.AvgLatencyMs) * time.Millisecond),
			Throughput:  formatBytes(int64(ps.AvgThroughput)) + "/s",
			Downloaded:  formatBytes(ps.BytesDownloaded),
			Uploaded:    formatBytes(ps.BytesUploaded),
			LastSeen:    formatDuration(time.Since(ps.LastSeen)) + " ago",
			Blacklisted: ps.Blacklisted,
		})
	}

	return result
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&s.activeConns, 1)
	defer atomic.AddInt64(&s.activeConns, -1)
	atomic.AddInt64(&s.requestsTotal, 1)

	// Initialize request context with tracing
	ctx := r.Context()

	// Check for incoming X-Request-ID header (preserve if valid)
	incomingID := r.Header.Get("X-Request-ID")
	if incomingID != "" && requestid.IsValid(incomingID) {
		ctx = requestid.NewContextWithID(ctx, incomingID, s.logger)
	} else {
		ctx = requestid.NewContext(ctx, s.logger)
	}

	// Get request-scoped logger and request ID
	log := requestid.LoggerFromContext(ctx, s.logger)
	reqID := requestid.FromContext(ctx)

	// Set X-Request-ID response header
	w.Header().Set("X-Request-ID", reqID)

	// Update request with new context
	r = r.WithContext(ctx)

	// Handle CONNECT method for HTTPS tunneling
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}

	targetURL, allowed := s.extractTargetURL(r)
	if targetURL == "" {
		http.Error(w, "debswarm: could not parse a repository URL from the request", http.StatusBadRequest)
		return
	}
	if !allowed {
		s.writeBlockedURLError(w, r, targetURL)
		return
	}

	log.Debug("Proxy request",
		zap.String("method", r.Method),
		zap.String("url", sanitize.URL(targetURL)))

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

	if strings.HasSuffix(lower, ".deb") ||
		strings.HasSuffix(lower, ".udeb") ||
		strings.HasSuffix(lower, ".ddeb") {
		return requestTypePackage
	}

	// Source-package artifacts (.dsc/.orig.tar.*/.debian.tar.*/.diff.gz/native
	// tarball) are content-addressed and verified like binary packages once their
	// Sources index is parsed. Classified before the index arm below so a source
	// package whose name contains "sources" (e.g. pool/main/s/sources-list/….dsc)
	// is not mistaken for a Sources index.
	if isSourceArtifactURL(lower) {
		return requestTypePackage
	}

	if strings.Contains(lower, "/packages") ||
		strings.Contains(lower, "/sources") {
		return requestTypeIndex
	}

	// Detect by-hash URLs for Packages/Sources files: dist-layout binary-*/source/,
	// or a flat-layout repo where by-hash sits directly under the repo base.
	// Exclude i18n/ (translations), cnf/ (commands) and dep11/ (appstream).
	if strings.Contains(lower, "/by-hash/") {
		if strings.Contains(lower, "/binary-") || strings.Contains(lower, "/source/") || isFlatByHash(lower) {
			return requestTypeIndex
		}
	}

	if strings.Contains(lower, "/release") ||
		strings.Contains(lower, "/inrelease") {
		return requestTypeRelease
	}

	return requestTypeUnknown
}

// extractTargetURL parses the target repository URL from the request and reports
// whether it is allowed. It returns ("", false) when no URL can be parsed, and
// (url, false) when a URL was parsed but is not permitted (blocked internal host,
// or a host not in the allow list) so the caller can produce a specific error.
func (s *Server) extractTargetURL(r *http.Request) (targetURL string, allowed bool) {
	// For proxy requests, r.URL contains the full absolute URL.
	if r.URL.Host != "" {
		targetURL = r.URL.String()
	} else {
		// Fall back to path-based extraction for non-proxy requests.
		path := strings.TrimPrefix(r.URL.Path, "/")
		switch {
		case strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://"):
			targetURL = path
		case strings.Contains(path, "/"):
			targetURL = "http://" + path
		default:
			return "", false
		}
	}

	// SECURITY: Validate the URL to prevent SSRF and restrict to allowed repos.
	return targetURL, s.isAllowedMirrorURL(targetURL)
}

// writeBlockedURLError responds with a clear, actionable error when a request is
// refused by the allow list, distinguishing an internal/SSRF-blocked target from
// a host that simply hasn't been allow-listed.
func (s *Server) writeBlockedURLError(w http.ResponseWriter, r *http.Request, targetURL string) {
	log := requestid.LoggerFromContext(r.Context(), s.logger)
	log.Warn("Blocked request to non-allowed URL",
		zap.String("url", sanitize.URL(targetURL)),
		zap.String("remoteAddr", r.RemoteAddr))

	if security.IsBlockedHost(targetURL) {
		http.Error(w, "debswarm: refused request to an internal or private address (SSRF protection)", http.StatusForbidden)
		return
	}

	host := targetURL
	if parsed, err := url.Parse(targetURL); err == nil && parsed.Hostname() != "" {
		host = parsed.Hostname()
	}
	http.Error(w, fmt.Sprintf(
		"debswarm: repository host %q is not in the allowed list. If this is a legitimate repository, "+
			"add its host to proxy.allowed_hosts in your debswarm config (common third-party repos are trusted "+
			"by default unless trust_known_repos is disabled).", host),
		http.StatusForbidden)
}

// isAllowedMirrorURL validates that a URL is a legitimate Debian/Ubuntu mirror
// This prevents SSRF attacks by blocking requests to internal services
func (s *Server) isAllowedMirrorURL(url string) bool {
	return security.IsAllowedMirrorURLWithHosts(url, s.allowedHosts)
}

// upstreamFetchURL upgrades a plain-HTTP mirror URL to HTTPS when its host is
// configured for upstream HTTPS fetching. Non-HTTP schemes and unlisted hosts
// are returned unchanged. This affects only the connection debswarm makes to the
// upstream mirror; cache keys, index lookups, and P2P content addressing use the
// request path and SHA256 and are therefore unaffected by the scheme change.
func (s *Server) upstreamFetchURL(rawURL string) string {
	if len(s.httpsUpstreamHosts) == 0 {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" {
		return rawURL
	}
	if !s.isHTTPSUpstreamHost(parsed.Hostname()) {
		return rawURL
	}
	parsed.Scheme = "https"
	// Drop an explicit :80 so the HTTPS request uses the default 443.
	if parsed.Port() == "80" {
		parsed.Host = parsed.Hostname()
	}
	return parsed.String()
}

// isHTTPSUpstreamHost reports whether host matches a configured HTTPS-upstream
// host, either exactly or as a subdomain (case-insensitive).
func (s *Server) isHTTPSUpstreamHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, h := range s.httpsUpstreamHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

func (s *Server) handlePackageRequest(w http.ResponseWriter, r *http.Request, url string) {
	ctx := r.Context()
	log := requestid.LoggerFromContext(ctx, s.logger)
	reqID := requestid.FromContext(ctx)

	// Extract path for caching
	path := index.ExtractPathFromURL(url)

	// Find expected hash from index using repo-aware lookup
	var expectedHash string
	var expectedSize int64

	// Try repo-specific lookup first (more accurate for multi-repo setups)
	if pkg := s.index.GetByURLPath(url); pkg != nil {
		// Validate hash format before use (must be 64 hex characters)
		if len(pkg.SHA256) == 64 {
			if _, err := hex.DecodeString(pkg.SHA256); err == nil {
				expectedHash = pkg.SHA256
				expectedSize = pkg.Size
				path = pkg.Filename // Use filename from index if available
				log.Debug("Found package in index",
					zap.String("repo", pkg.Repo),
					zap.String("path", sanitize.Path(path)),
					zap.String("hash", expectedHash[:16]+"..."),
					zap.Int64("size", expectedSize))
			} else {
				log.Warn("Invalid hash format in index", zap.String("url", sanitize.URL(url)))
			}
		}
	}

	// A cold in-memory index cannot resolve a cached .deb's hash. The index is
	// populated at startup by the aptlists watcher (from /var/lib/apt/lists) and
	// lazily by live Packages requests through the proxy — but neither fires on a
	// host that never runs `apt-get update` locally, e.g. a dedicated debswarm
	// cache-server whose /var/lib/apt/lists is empty. There the only record of the
	// package's hash is debswarm's own metadata cache, so warm the index from it
	// once and retry; otherwise an already-cached package falls through to an
	// uncached passthrough that fails when the mirror is unreachable. On a normal
	// host (apt lists present) the warm is a no-op — the index is already warm.
	if expectedHash == "" {
		s.warmIndexFromCacheOnce()
		if pkg := s.index.GetByURLPath(url); pkg != nil && len(pkg.SHA256) == 64 {
			if _, err := hex.DecodeString(pkg.SHA256); err == nil {
				expectedHash = pkg.SHA256
				expectedSize = pkg.Size
				path = pkg.Filename
				log.Debug("Resolved package after warming index from cache",
					zap.String("hash", expectedHash[:16]+"..."))
			}
		}
	}

	// No signed index entry: the package cannot be verified, cached, or shared
	// over P2P. Stream it straight from the mirror to the client instead of
	// buffering the whole file in memory (it can be hundreds of MB). This path
	// skips singleflight — a stream cannot be shared between coalesced waiters.
	if expectedHash == "" {
		s.metrics.CacheMisses.Inc()
		s.metrics.PackagesServedUncached.Inc()
		s.noteUncachedServe(log, url)
		s.streamUncachedPackage(w, r, url, path)
		return
	}

	// Check local cache first
	if s.cache.Has(expectedHash) {
		err := s.serveFromCache(w, expectedHash)
		if err == nil {
			log.Debug("Cache hit", zap.String("hash", expectedHash[:16]+"..."))
			atomic.AddInt64(&s.cacheHits, 1)
			s.metrics.CacheHits.Inc()

			// Audit log cache hit
			s.audit.Log(audit.NewCacheHitEvent(expectedHash, path, expectedSize).WithRequestID(reqID))
			return
		}
		// Has() saw the file but Get() failed — the classic aftermath of
		// database corruption recovery, which leaves package files on disk
		// with no metadata rows. Previously this returned 500 for every such
		// package until a manual `cache rebuild`. Treat it as a miss instead:
		// the re-download below re-caches the package (Put handles an
		// already-present file), self-healing the entry.
		log.Warn("Cached file unreadable, re-downloading",
			zap.String("hash", expectedHash[:16]+"..."),
			zap.Error(err))
	}

	s.metrics.CacheMisses.Inc()

	// Offline fast-fail: the package is not cached and there is genuinely nothing
	// to reach — ModeOffline means no internet AND no mDNS peers. Skip the doomed
	// fleet -> DHT -> P2P -> mirror chain and tell APT immediately instead of
	// making it wait out the download timeouts.
	if s.connectivity != nil && s.connectivity.GetMode() == connectivity.ModeOffline {
		log.Debug("Package not cached and node is offline", zap.String("url", sanitize.URL(url)))
		http.Error(w, "package not cached and node is offline", http.StatusServiceUnavailable)
		return
	}

	// Use singleflight to coalesce concurrent requests for the same package
	// This prevents duplicate downloads when multiple clients request the same package
	coalescingKey := expectedHash

	result, err, shared := s.downloadGroup.Do(coalescingKey, func() (interface{}, error) {
		return s.downloadPackage(ctx, url, expectedHash, expectedSize, path)
	})

	if shared {
		log.Debug("Request coalesced with another download",
			zap.String("url", sanitize.URL(url)),
			zap.String("key", coalescingKey[:min(16, len(coalescingKey))]+"..."))
	}

	if err != nil {
		log.Error("Download failed", zap.Error(err))
		http.Error(w, "Failed to fetch package", http.StatusBadGateway)
		return
	}

	// Serve the result
	downloadResult := result.(*packageDownloadResult)
	s.servePackageResult(w, downloadResult)
}

// warmIndexFromCacheOnce loads every cached Packages index into the in-memory
// index, exactly once per daemon session. It lets the proxy resolve a .deb URL to
// its SHA256 (and thus serve the package from cache) on a host that never runs
// `apt-get update` locally — a dedicated cache-server with an empty
// /var/lib/apt/lists, where the aptlists watcher warms nothing at startup and the
// package's hash lives only in debswarm's metadata cache. Where apt's lists are
// present this is a no-op (the aptlists watcher already warmed the index).
// Best-effort: individual read/parse failures are logged and skipped.
func (s *Server) warmIndexFromCacheOnce() {
	s.indexWarmOnce.Do(func() {
		if s.cache == nil || s.index == nil || !s.cache.MetadataEnabled() {
			return
		}
		urls, err := s.cache.ListMetadataURLs()
		if err != nil {
			s.logger.Debug("Index warm: failed to list cached metadata", zap.Error(err))
			return
		}
		warmed := 0
		for _, u := range urls {
			if !isVerifiableIndexURL(u) || s.index.HasIndexFile(u) {
				continue
			}
			entry, rc, gerr := s.cache.GetMetadata(u)
			if gerr != nil {
				continue
			}
			data, rerr := io.ReadAll(io.LimitReader(rc, entry.Size))
			_ = rc.Close()
			if rerr != nil {
				continue
			}
			// Enforce mode: do not warm an index that does not verify against the
			// signed Release. warn/off warm it as before.
			if !s.checkIndexVerification(nil, u, data, s.logger) {
				continue
			}
			if lerr := s.loadIndexInto(u, data); lerr == nil {
				warmed++
			}
		}
		if warmed > 0 {
			s.logger.Info("Warmed in-memory index from cached metadata",
				zap.Int("files", warmed), zap.Int("totalPackages", s.index.Count()))
		}
	})
}

// packageDownloadResult holds the result of a package download
type packageDownloadResult struct {
	data           []byte
	hash           string
	size           int64 // Used when serveFromCache is true
	source         string
	contentType    string
	serveFromCache bool // If true, stream from cache instead of using data
}

// downloadPackage performs the actual download (called via singleflight)
func (s *Server) downloadPackage(ctx context.Context, url, expectedHash string, expectedSize int64, path string) (result *packageDownloadResult, retErr error) {
	log := requestid.LoggerFromContext(ctx, s.logger)
	reqID := requestid.FromContext(ctx)

	// Check if this is a security update (for scheduler rate bypassing)
	isSecurityUpdate := scheduler.IsSecurityUpdate(url)
	if isSecurityUpdate && s.scheduler != nil {
		log.Debug("Security update detected, using full speed",
			zap.String("url", sanitize.URL(url)))
		s.metrics.SchedulerUrgentDownloads.Inc()
	}

	// Consult fleet coordinator before downloading
	if expectedHash != "" && s.fleet != nil {
		fleetResult, fleetErr := s.fleet.WantPackage(ctx, expectedHash, expectedSize)
		if fleetErr == nil {
			switch fleetResult.Action {
			case fleet.ActionFetchLAN:
				// A peer already has this package cached — download from LAN
				// (downloadFromFleetPeer verifies and caches in one pass)
				data, dlErr := s.downloadFromFleetPeer(ctx, fleetResult.Provider, expectedHash, path)
				if dlErr == nil {
					log.Debug("Downloaded from fleet peer (LAN cache hit)",
						zap.String("hash", expectedHash[:16]+"..."),
						zap.Int("size", len(data)),
						zap.String("provider", fleetResult.Provider.String()[:min(12, len(fleetResult.Provider.String()))]))

					atomic.AddInt64(&s.requestsP2P, 1)
					atomic.AddInt64(&s.bytesFromP2P, int64(len(data)))
					s.metrics.DownloadsTotal.WithLabel(downloader.SourceTypePeer).Inc()
					s.metrics.BytesDownloaded.WithLabel(downloader.SourceTypePeer).Add(int64(len(data)))

					return &packageDownloadResult{
						data:        data,
						hash:        expectedHash,
						source:      downloader.SourceTypePeer,
						contentType: "application/vnd.debian.binary-package",
					}, nil
				}
				log.Debug("Fleet LAN download failed, falling back to normal download", zap.Error(dlErr))

			case fleet.ActionWaitPeer:
				// Another peer is fetching this package — wait for them, then grab via LAN
				waitCtx, waitCancel := context.WithTimeout(ctx, s.fleet.GetMaxWaitTime())
				select {
				case waitErr := <-fleetResult.WaitChan:
					waitCancel()
					if waitErr == nil {
						data, dlErr := s.downloadFromFleetPeer(ctx, fleetResult.Provider, expectedHash, path)
						if dlErr == nil {
							log.Debug("Downloaded from fleet peer after wait",
								zap.String("hash", expectedHash[:16]+"..."),
								zap.Int("size", len(data)),
								zap.String("provider", fleetResult.Provider.String()[:min(12, len(fleetResult.Provider.String()))]))

							atomic.AddInt64(&s.requestsP2P, 1)
							atomic.AddInt64(&s.bytesFromP2P, int64(len(data)))
							s.metrics.DownloadsTotal.WithLabel(downloader.SourceTypePeer).Inc()
							s.metrics.BytesDownloaded.WithLabel(downloader.SourceTypePeer).Add(int64(len(data)))

							return &packageDownloadResult{
								data:        data,
								hash:        expectedHash,
								source:      downloader.SourceTypePeer,
								contentType: "application/vnd.debian.binary-package",
							}, nil
						}
						log.Debug("Fleet peer download after wait failed, falling back", zap.Error(dlErr))
					}
				case <-waitCtx.Done():
					waitCancel()
					log.Debug("Fleet wait timed out, falling back to normal download")
				}

			case fleet.ActionFetchWAN:
				// We're the designated WAN fetcher — fall through to normal download
			}
		}
	}

	// Notify fleet that we're fetching from WAN (so other nodes can wait for us)
	if expectedHash != "" && s.fleet != nil {
		s.fleet.NotifyFetching(expectedHash, expectedSize)
		defer func() {
			if retErr != nil {
				s.fleet.NotifyFailed(expectedHash, retErr)
			} else {
				s.fleet.NotifyComplete(expectedHash)
			}
		}()
	}

	// Build download sources
	var peerSources []downloader.Source
	var mirrorSource downloader.Source

	// Find P2P providers if we have a hash
	if expectedHash != "" && s.p2pNode != nil {
		dhtCtx, dhtCancel := context.WithTimeout(ctx, s.timeouts.Get(timeouts.OpDHTLookup))
		providers, err := s.p2pNode.FindProvidersRanked(dhtCtx, expectedHash, s.dhtLookupLimit)
		dhtCancel()

		if err == nil && len(providers) > 0 {
			log.Debug("Found P2P providers",
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

	// Add mirror source with range request support.
	// For HTTPS-upstream hosts, fetch over HTTPS even though APT requested HTTP;
	// the cache/index/P2P layers keep using the original (unmodified) URL/hash.
	mirrorURL := s.upstreamFetchURL(url)
	mirrorSource = &downloader.MirrorSource{
		URL: mirrorURL,
		Fetcher: func(ctx context.Context, url string, start, end int64) ([]byte, error) {
			// Convert from exclusive end (used by downloader chunks) to inclusive end
			// (used by HTTP Range headers). end=-1 means full file, pass through as-is.
			if end > 0 {
				end = end - 1
			}
			return s.fetcher.FetchRange(ctx, url, start, end)
		},
	}

	// Use parallel downloader for large files with available peers
	if expectedHash != "" && expectedSize > 0 && len(peerSources) > 0 {
		result, err := s.downloader.Download(ctx, expectedHash, expectedSize, peerSources, mirrorSource)
		if err == nil {
			return s.processDownloadSuccess(ctx, result, expectedHash, path), nil
		}
		log.Debug("Parallel download failed, falling back to mirror", zap.Error(err))
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

			// Verify and cache in a single hashing pass (inside cache.Put)
			if verifyErr := s.verifyAndCache(data, expectedHash, path); verifyErr != nil {
				log.Warn("P2P hash mismatch, blacklisting peer")
				s.metrics.VerificationFailures.Inc()
				if ps, ok := src.(*downloader.PeerSource); ok {
					s.scorer.Blacklist(ps.Info.ID, "hash mismatch", 24*time.Hour)
					s.metrics.PeersBlacklisted.Inc()
					// Audit log verification failure and the resulting blacklist
					s.audit.Log(audit.NewVerificationFailedEvent(expectedHash, path, ps.Info.ID.String()).WithRequestID(reqID))
					s.audit.Log(audit.NewPeerBlacklistedEvent(ps.Info.ID.String(), "hash mismatch").WithRequestID(reqID))
				}
				continue
			}

			log.Debug("Downloaded from P2P",
				zap.String("hash", expectedHash[:16]+"..."),
				zap.Int("size", len(data)))

			atomic.AddInt64(&s.requestsP2P, 1)
			atomic.AddInt64(&s.bytesFromP2P, int64(len(data)))
			s.metrics.DownloadsTotal.WithLabel(downloader.SourceTypePeer).Inc()
			s.metrics.BytesDownloaded.WithLabel(downloader.SourceTypePeer).Add(int64(len(data)))

			// Audit log download complete
			s.audit.Log(audit.NewDownloadCompleteEvent(
				expectedHash,
				path,
				int64(len(data)),
				downloader.SourceTypePeer,
				0, // duration not tracked for simple downloads
				int64(len(data)),
				0,
			).WithRequestID(reqID))

			return &packageDownloadResult{
				data:        data,
				hash:        expectedHash,
				source:      downloader.SourceTypePeer,
				contentType: "application/vnd.debian.binary-package",
			}, nil
		}
	}

	// Final fallback: mirror. Stream the body straight into the cache — Put
	// hashes and verifies while writing to disk — then serve from the cached
	// file, so the package is never buffered in memory (it can be hundreds of
	// MB, and this is the default path on nodes with no P2P providers).
	// Packages with no index entry never reach here (handlePackageRequest
	// streams those directly), so expectedHash is always set.
	log.Debug("Falling back to mirror", zap.String("url", sanitize.URL(mirrorURL)))
	atomic.AddInt64(&s.requestsMirror, 1)

	body, _, err := s.fetcher.Stream(ctx, mirrorURL)
	if err != nil {
		logFetchFailure(ctx, log, "Mirror fetch failed", err)
		// Audit log download failure
		s.audit.Log(audit.NewDownloadFailedEvent(expectedHash, path, err.Error()).WithRequestID(reqID))
		return nil, fmt.Errorf("mirror fetch failed: %w", err)
	}

	counted := &countingReader{r: body}
	putErr := s.cache.Put(counted, expectedHash, path)
	if closeErr := body.Close(); closeErr != nil {
		log.Debug("Failed to close mirror response body", zap.Error(closeErr))
	}

	if putErr != nil {
		if errors.Is(putErr, cache.ErrHashMismatch) {
			log.Warn("Mirror hash mismatch",
				zap.String("expected", expectedHash),
				zap.Error(putErr))
			s.metrics.VerificationFailures.Inc()
			s.audit.Log(audit.NewVerificationFailedEvent(expectedHash, path, "mirror").WithRequestID(reqID))
			return nil, fmt.Errorf("mirror data failed hash verification: %w", putErr)
		}

		// The cache could not store the package (cache full, disk error). The
		// stream is already partially consumed, so re-fetch buffered — the old
		// behavior — and serve this one download from memory without caching.
		log.Warn("Failed to cache streamed mirror download, refetching into memory", zap.Error(putErr))
		data, fetchErr := s.fetcher.Fetch(ctx, mirrorURL)
		if fetchErr != nil {
			s.audit.Log(audit.NewDownloadFailedEvent(expectedHash, path, fetchErr.Error()).WithRequestID(reqID))
			return nil, fmt.Errorf("mirror fetch failed: %w", fetchErr)
		}
		actualHash := sha256.Sum256(data)
		if hex.EncodeToString(actualHash[:]) != expectedHash {
			s.metrics.VerificationFailures.Inc()
			s.audit.Log(audit.NewVerificationFailedEvent(expectedHash, path, "mirror").WithRequestID(reqID))
			return nil, fmt.Errorf("mirror data failed hash verification: expected %s", expectedHash)
		}
		atomic.AddInt64(&s.bytesFromMirror, int64(len(data)))
		s.metrics.DownloadsTotal.WithLabel(downloader.SourceTypeMirror).Inc()
		s.metrics.BytesDownloaded.WithLabel(downloader.SourceTypeMirror).Add(int64(len(data)))
		s.audit.Log(audit.NewDownloadCompleteEvent(
			expectedHash, path, int64(len(data)), downloader.SourceTypeMirror,
			0, 0, int64(len(data))).WithRequestID(reqID))
		return &packageDownloadResult{
			data:        data,
			hash:        expectedHash,
			source:      downloader.SourceTypeMirror,
			contentType: "application/vnd.debian.binary-package",
		}, nil
	}

	size := counted.n
	atomic.AddInt64(&s.bytesFromMirror, size)
	s.metrics.DownloadsTotal.WithLabel(downloader.SourceTypeMirror).Inc()
	s.metrics.BytesDownloaded.WithLabel(downloader.SourceTypeMirror).Add(size)

	s.announceAsync(expectedHash)
	if s.verifier != nil {
		s.verifier.VerifyAsync(expectedHash, path)
	}

	// Audit log mirror download complete
	s.audit.Log(audit.NewDownloadCompleteEvent(
		expectedHash,
		path,
		size,
		downloader.SourceTypeMirror,
		0, // duration not tracked for mirror fallback
		0,
		size,
	).WithRequestID(reqID))

	return &packageDownloadResult{
		hash:           expectedHash,
		size:           size,
		source:         downloader.SourceTypeMirror,
		contentType:    "application/vnd.debian.binary-package",
		serveFromCache: true,
	}, nil
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.n += int64(n)
	return n, err
}

// downloadFromFleetPeer downloads a package from a fleet peer that has it
// cached, then verifies and caches it in a single hashing pass. A peer that
// serves corrupt data is blacklisted.
func (s *Server) downloadFromFleetPeer(ctx context.Context, providerID peer.ID, expectedHash, path string) ([]byte, error) {
	addrs := s.p2pNode.Host().Peerstore().Addrs(providerID)
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses for fleet peer %s", providerID.String()[:min(12, len(providerID.String()))])
	}

	peerCtx, cancel := context.WithTimeout(ctx, s.p2pTimeout)
	defer cancel()

	data, err := s.p2pNode.Download(peerCtx, peer.AddrInfo{ID: providerID, Addrs: addrs}, expectedHash)
	if err != nil {
		return nil, fmt.Errorf("fleet peer download: %w", err)
	}

	if err := s.verifyAndCache(data, expectedHash, path); err != nil {
		s.scorer.Blacklist(providerID, "fleet hash mismatch", 24*time.Hour)
		s.metrics.PeersBlacklisted.Inc()
		s.audit.Log(audit.NewPeerBlacklistedEvent(providerID.String(), "fleet hash mismatch"))
		return nil, fmt.Errorf("fleet peer hash mismatch")
	}
	return data, nil
}

// processDownloadSuccess processes a successful parallel download result
func (s *Server) processDownloadSuccess(ctx context.Context, result *downloader.DownloadResult, expectedHash, path string) *packageDownloadResult {
	log := requestid.LoggerFromContext(ctx, s.logger)
	reqID := requestid.FromContext(ctx)

	// Update stats
	atomic.AddInt64(&s.bytesFromP2P, result.PeerBytes)
	atomic.AddInt64(&s.bytesFromMirror, result.MirrorBytes)

	if result.PeerBytes > result.MirrorBytes {
		atomic.AddInt64(&s.requestsP2P, 1)
	} else {
		atomic.AddInt64(&s.requestsMirror, 1)
	}

	log.Info("Download complete",
		zap.String("hash", expectedHash[:16]+"..."),
		zap.Int64("size", result.Size),
		zap.String("source", result.Source),
		zap.Int64("peerBytes", result.PeerBytes),
		zap.Int64("mirrorBytes", result.MirrorBytes),
		zap.Int("chunks", result.ChunksTotal),
		zap.Int("chunksP2P", result.ChunksFromP2P),
		zap.Duration("duration", result.Duration))

	// Audit log download complete
	s.audit.Log(audit.NewDownloadCompleteEvent(
		expectedHash,
		path,
		result.Size,
		result.Source,
		result.Duration.Milliseconds(),
		result.PeerBytes,
		result.MirrorBytes,
	).WithRequestID(reqID))

	// Handle file-based result (chunked download - streaming)
	if result.FilePath != "" {
		// The assembly file lives in a per-download directory (the resumable
		// partial/{hash} dir, or a temp dir when resume is disabled). Once we're
		// done with it, remove that directory so it does not leak as an empty dir
		// after PutFile renames the file away.
		assemblyDir := filepath.Dir(result.FilePath)

		// Move verified file directly to cache (no memory copy)
		if err := s.cache.PutFile(result.FilePath, expectedHash, path, result.Size); err != nil {
			// Caching failed (e.g. cache full). The package is fully downloaded and
			// verified, so serve it anyway instead of returning 500 to APT. Read it
			// into memory — consistent with the racing/mirror-fallback paths — and
			// drop the on-disk copy so it does not leak. PutFile fails before its
			// rename on a full cache, so the source file is still present here.
			log.Warn("Failed to cache file, serving directly without caching", zap.Error(err))
			data, readErr := os.ReadFile(result.FilePath) // #nosec G304 -- path is our own assembled download file
			_ = os.RemoveAll(assemblyDir)
			if readErr == nil {
				return &packageDownloadResult{
					data:        data,
					hash:        expectedHash,
					size:        result.Size,
					source:      result.Source,
					contentType: "application/vnd.debian.binary-package",
				}
			}
			// Could not re-read the file (e.g. PutFile failed after its rename);
			// fall through to the cache-serve path, which reports the error to APT.
			log.Error("Failed to read downloaded file after cache failure", zap.Error(readErr))
		} else {
			s.announceAsync(expectedHash)
			_ = os.RemoveAll(assemblyDir)
		}

		return &packageDownloadResult{
			hash:           expectedHash,
			size:           result.Size,
			source:         result.Source,
			contentType:    "application/vnd.debian.binary-package",
			serveFromCache: true,
		}
	}

	// Handle in-memory result (racing download - small files)
	s.cacheAndAnnounce(result.Data, expectedHash, path)

	return &packageDownloadResult{
		data:        result.Data,
		hash:        expectedHash,
		source:      result.Source,
		contentType: "application/vnd.debian.binary-package",
	}
}

// servePackageResult writes a download result to the HTTP response
func (s *Server) servePackageResult(w http.ResponseWriter, result *packageDownloadResult) {
	// Stream from cache for file-based results (chunked downloads)
	if result.serveFromCache {
		reader, _, err := s.cache.Get(result.hash)
		if err != nil {
			s.logger.Error("Failed to read from cache for serving", zap.Error(err))
			http.Error(w, "Cache error", http.StatusInternalServerError)
			return
		}
		defer reader.Close()

		w.Header().Set("Content-Type", result.contentType)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", result.size))
		if result.source != "" {
			w.Header().Set("X-Debswarm-Source", result.source)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, reader)
		return
	}

	// Serve from memory for in-memory results (racing downloads)
	w.Header().Set("Content-Type", result.contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(result.data)))
	if result.source != "" {
		w.Header().Set("X-Debswarm-Source", result.source)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.data)
}

func (s *Server) cacheAndAnnounce(data []byte, hash, path string) {
	if err := s.cache.Put(bytes.NewReader(data), hash, path); err != nil {
		s.logger.Warn("Failed to cache", zap.Error(err))
		return
	}
	s.announceAsync(hash)

	// Asynchronously verify via multi-source query
	if s.verifier != nil {
		s.verifier.VerifyAsync(hash, path)
	}
}

// verifyAndCache verifies data against hash and stores it in the cache,
// hashing the data only once (cache.Put verifies while writing — callers must
// not pre-hash, that was a redundant full pass over every download). If the
// cache cannot store it for storage reasons, the data is verified directly so
// the caller may still serve it uncached. A cache.ErrHashMismatch return means
// the data is corrupt and must not be served.
func (s *Server) verifyAndCache(data []byte, hash, path string) error {
	err := s.cache.Put(bytes.NewReader(data), hash, path)
	if err == nil {
		s.announceAsync(hash)
		if s.verifier != nil {
			s.verifier.VerifyAsync(hash, path)
		}
		return nil
	}
	if errors.Is(err, cache.ErrHashMismatch) {
		return err
	}
	// Storage failure (cache full, disk error): verify manually so unverified
	// bytes are never served, then let the caller serve without caching.
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != hash {
		return fmt.Errorf("%w: expected %s", cache.ErrHashMismatch, hash)
	}
	s.logger.Warn("Failed to cache verified package", zap.Error(err))
	return nil
}

func (s *Server) announceAsync(hash string) {
	if s.p2pNode == nil {
		return
	}
	// Non-blocking send to bounded channel
	select {
	case s.announceChan <- hash:
	default:
		// Channel full, skip this announcement (will be reannounced later)
		s.logger.Debug("Announcement queue full, skipping", zap.String("hash", hash[:16]+"..."))
	}
}

// announcementWorker processes announcements with bounded concurrency
func (s *Server) announcementWorker() {
	// Process up to 4 announcements concurrently
	const maxConcurrent = 4
	const announceTimeout = 30 * time.Second

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	// Loop until the announce context is canceled (shutdown). We select on the
	// context rather than ranging s.announceChan, because the channel is never
	// closed — closing it would race with in-flight announceAsync sends and panic.
	for {
		select {
		case <-s.announceCtx.Done():
			// Wait for all in-flight announcements to complete, then signal done.
			wg.Wait()
			close(s.announceDone)
			return
		case hash := <-s.announceChan:
			sem <- struct{}{} // Acquire semaphore
			wg.Add(1)
			go func(h string) {
				defer func() {
					<-sem // Release semaphore
					wg.Done()
				}()
				// Use server's announce context as parent so announcements stop on shutdown
				ctx, cancel := context.WithTimeout(s.announceCtx, announceTimeout)
				defer cancel()
				if err := s.p2pNode.Provide(ctx, h); err != nil {
					// Don't log context canceled errors during shutdown
					if s.announceCtx.Err() == nil {
						s.logger.Debug("Failed to announce", zap.Error(err))
					}
				}
			}(hash)
		}
	}
}

// retryWorker periodically checks for failed downloads and retries them
func (s *Server) retryWorker() {
	defer close(s.retryDone)

	ticker := time.NewTicker(s.retryInterval)
	defer ticker.Stop()

	s.logger.Info("Retry worker started",
		zap.Duration("interval", s.retryInterval),
		zap.Int("maxAttempts", s.retryMaxAttempts),
		zap.Duration("maxAge", s.retryMaxAge))

	for {
		select {
		case <-s.retryCtx.Done():
			s.logger.Debug("Retry worker stopping")
			return
		case <-ticker.C:
			s.checkAndRetryFailedDownloads()
		}
	}
}

// checkAndRetryFailedDownloads finds failed downloads and retries them
func (s *Server) checkAndRetryFailedDownloads() {
	stateManager := s.downloader.GetStateManager()
	if stateManager == nil {
		return
	}

	// Get retryable downloads (failed but within retry limits)
	failed, err := stateManager.GetRetryableDownloads(s.retryMaxAttempts, s.retryMaxAge)
	if err != nil {
		s.logger.Warn("Failed to get retryable downloads", zap.Error(err))
		return
	}

	if len(failed) == 0 {
		return
	}

	s.logger.Info("Found failed downloads to retry", zap.Int("count", len(failed)))

	for _, state := range failed {
		// Check context before each retry
		select {
		case <-s.retryCtx.Done():
			return
		default:
		}

		// Skip if URL is empty (shouldn't happen but be safe)
		if state.URL == "" {
			s.logger.Debug("Skipping retry - no URL stored",
				zap.String("hash", state.ID[:min(16, len(state.ID))]+"..."))
			continue
		}

		s.logger.Info("Retrying failed download",
			zap.String("hash", state.ID[:min(16, len(state.ID))]+"..."),
			zap.Int("attempt", state.RetryCount+1),
			zap.String("url", sanitize.URL(state.URL)))

		// Mark for retry (resets status to pending, increments retry count)
		if err := stateManager.MarkForRetry(state.ID); err != nil {
			s.logger.Warn("Failed to mark download for retry",
				zap.String("hash", state.ID[:min(16, len(state.ID))]+"..."),
				zap.Error(err))
			continue
		}

		// Extract path from URL for caching
		path := index.ExtractPathFromURL(state.URL)

		// Retry the download in background
		go s.retryDownload(state.ID, state.URL, state.ExpectedSize, path)
	}
}

// retryDownload performs a retry download for a failed package
func (s *Server) retryDownload(expectedHash, url string, expectedSize int64, path string) {
	ctx, cancel := context.WithTimeout(s.retryCtx, 5*time.Minute)
	defer cancel()

	// Coalesce with any concurrent requests for the same package
	coalescingKey := expectedHash
	if coalescingKey == "" {
		coalescingKey = url
	}

	result, err, shared := s.downloadGroup.Do(coalescingKey, func() (interface{}, error) {
		return s.downloadPackage(ctx, url, expectedHash, expectedSize, path)
	})

	if shared {
		s.logger.Debug("Retry coalesced with another download",
			zap.String("hash", expectedHash[:min(16, len(expectedHash))]+"..."))
	}

	if err != nil {
		s.logger.Warn("Retry download failed",
			zap.String("hash", expectedHash[:min(16, len(expectedHash))]+"..."),
			zap.Error(err))
		return
	}

	downloadResult := result.(*packageDownloadResult)
	s.logger.Info("Retry download succeeded",
		zap.String("hash", expectedHash[:min(16, len(expectedHash))]+"..."),
		zap.String("source", downloadResult.source),
		zap.Int("size", len(downloadResult.data)))
}

// logFetchFailure logs an upstream fetch failure at the appropriate level:
// when the request context is already canceled, the CLIENT hung up — APT
// routinely abandons redundant index requests during apt-get update — which is
// not a server error and used to put an ERROR line in the log on every update.
func logFetchFailure(ctx context.Context, log *zap.Logger, msg string, err error) {
	if ctx.Err() != nil {
		log.Debug(msg, zap.Error(err), zap.String("cause", "client canceled request"))
		return
	}
	log.Error(msg, zap.Error(err))
}

// relayValidators forwards upstream revalidation headers so APT can send
// conditional requests next time.
func relayValidators(w http.ResponseWriter, cond *mirror.ConditionalResult) {
	if cond.LastModified != "" {
		w.Header().Set("Last-Modified", cond.LastModified)
	}
	if cond.ETag != "" {
		w.Header().Set("ETag", cond.ETag)
	}
}

func (s *Server) handleIndexRequest(w http.ResponseWriter, r *http.Request, url string) {
	s.serveMetadata(w, r, url, true)
}

func (s *Server) handleReleaseRequest(w http.ResponseWriter, r *http.Request, url string) {
	// A Release/InRelease/Release.gpg request means the suite may have changed;
	// drop any cached verified Release for its base (dist- or flat-layout) so the
	// next index request re-verifies against the copy about to be (re)fetched and
	// cached.
	if s.verificationEnabled() {
		if base := verificationBaseURL(url); base != "" {
			s.releaseStore.invalidate(base)
		}
	}
	s.handlePassthrough(w, r, url)
}

func (s *Server) handlePassthrough(w http.ResponseWriter, r *http.Request, url string) {
	s.serveMetadata(w, r, url, false)
}

// serveMetadata handles a repository metadata request (Release/InRelease,
// Packages/Sources, Translation/Contents/DEP-11). With metadata caching on it
// revalidates the cached copy against the mirror and serves cached bytes on an
// upstream 304 — turning a cold client's full metadata download into a cheap
// conditional GET. isIndex marks Packages/Sources files, whose bytes are also
// parsed into the in-memory index. With caching off it is a pure revalidating
// passthrough (the historical behavior).
func (s *Server) serveMetadata(w http.ResponseWriter, r *http.Request, url string, isIndex bool) {
	ctx := r.Context()
	log := requestid.LoggerFromContext(ctx, s.logger)

	caching := s.cache != nil && s.cache.MetadataEnabled()
	staleOK := caching && s.metadataServeStale

	// Immutable by-hash URLs never change; if cached, serve with no upstream call.
	if caching && cache.IsImmutableMetadataURL(url) {
		if entry, rc, err := s.cache.GetMetadata(url); err == nil {
			log.Debug("Serving immutable metadata from cache", zap.String("url", sanitize.URL(url)))
			s.serveCachedMetadata(w, r, url, isIndex, entry, rc, false)
			return
		}
	}

	// Offline fast-path: when connectivity is known-offline, skip the doomed
	// upstream request and serve the cached copy (stale) directly.
	if staleOK && s.connectivity != nil && s.connectivity.GetMode() == connectivity.ModeOffline {
		if entry, rc, err := s.cache.GetMetadata(url); err == nil {
			s.serveCachedMetadata(w, r, url, isIndex, entry, rc, true)
			return
		}
		// Offline with nothing cached: fall through — the upstream attempt fails
		// and returns 502 (there is nothing to serve).
	}

	// Choose the validators for the upstream conditional GET: ours when we hold a
	// cached copy, otherwise the client's — preserving the historical relay, and
	// for index files only when the in-memory index already has the file (so a
	// cold-index restart can't relay a 304 it cannot back up).
	var ims, inm string
	haveCache := false
	if caching {
		if et, lm, ok := s.cache.MetadataValidators(url); ok {
			inm, ims, haveCache = et, lm, true
		}
	}
	if !haveCache && (!isIndex || s.index.HasIndexFile(url)) {
		ims = r.Header.Get("If-Modified-Since")
		inm = r.Header.Get("If-None-Match")
	}

	cond, err := s.fetcher.StreamConditional(ctx, s.upstreamFetchURL(url), ims, inm)
	if err != nil {
		// Upstream unreachable: serve a stale cached copy if we have one so
		// apt-get update keeps working offline. APT still verifies the signature
		// and Valid-Until of whatever we serve.
		if staleOK {
			if entry, rc, gerr := s.cache.GetMetadata(url); gerr == nil {
				s.serveCachedMetadata(w, r, url, isIndex, entry, rc, true)
				return
			}
		}
		logFetchFailure(ctx, log, "Failed to fetch metadata", err)
		http.Error(w, "Failed to fetch", http.StatusBadGateway)
		return
	}

	if cond.NotModified {
		if haveCache {
			// Our cached copy is current: refresh its validators and serve it.
			s.cache.RevalidateMetadata(url, cond.ETag, cond.LastModified)
			if entry, rc, gerr := s.cache.GetMetadata(url); gerr == nil {
				s.serveCachedMetadata(w, r, url, isIndex, entry, rc, false)
				return
			}
			// The cached file vanished between the validator read and now (rare):
			// fall back to an unconditional refetch.
			s.fetchFreshMetadata(w, r, url, isIndex)
			return
		}
		// Uncached: the client's own copy is current — relay the 304.
		log.Debug("Metadata not modified upstream", zap.String("url", sanitize.URL(url)))
		relayValidators(w, cond)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	s.serveFreshBody(w, r, url, isIndex, cond, caching)
}

// serveFreshBody serves an upstream 200 body, caching it (when enabled) and
// parsing Packages into the index. It takes ownership of cond.Body.
func (s *Server) serveFreshBody(w http.ResponseWriter, r *http.Request, url string, isIndex bool, cond *mirror.ConditionalResult, caching bool) {
	ctx := r.Context()
	log := requestid.LoggerFromContext(ctx, s.logger)
	defer func() { _ = cond.Body.Close() }()

	atomic.AddInt64(&s.metadataMisses, 1)
	if s.metrics != nil {
		s.metrics.MetadataCacheMisses.Inc()
	}

	if isIndex {
		// Index files are read fully so they can be parsed; bound the read.
		data, err := io.ReadAll(io.LimitReader(cond.Body, s.fetcher.MaxResponseSize()+1))
		if err != nil {
			logFetchFailure(ctx, log, "Failed to fetch index", err)
			http.Error(w, "Failed to fetch index", http.StatusBadGateway)
			return
		}
		if int64(len(data)) > s.fetcher.MaxResponseSize() {
			log.Error("Index response exceeds maximum allowed size")
			http.Error(w, "Index too large", http.StatusBadGateway)
			return
		}
		// Verify the index against the signed Release before trusting/serving it.
		// In enforce mode an unverified or mismatched index is refused; in warn
		// mode it is served with an X-Debswarm-Unverified header (APT still checks
		// GPG). Must run before any header/body is written.
		if !s.checkIndexVerification(w, url, data, log) {
			http.Error(w, "index failed upstream signature verification", http.StatusBadGateway)
			return
		}
		if isVerifiableIndexURL(url) {
			if err := s.loadIndexInto(url, data); err != nil {
				log.Debug("Failed to parse index file", zap.Error(err))
			} else {
				log.Debug("Parsed index file",
					zap.Int("totalPackages", s.index.Count()),
					zap.Int("repos", s.index.RepoCount()))
			}
		}
		if caching {
			s.storeMetadata(url, data, cond.ETag, cond.LastModified, "application/octet-stream", log)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		relayValidators(w, cond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}

	// Non-index metadata streams straight through, tee'd into the cache so a
	// large Contents/Translation file is never buffered in memory.
	relayValidators(w, cond)
	if cond.Size >= 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", cond.Size))
	}
	w.WriteHeader(http.StatusOK)

	var dst io.Writer = w
	var mw *cache.MetadataWriter
	if caching {
		if writer, werr := s.cache.NewMetadataWriter(url, cond.ETag, cond.LastModified, ""); werr == nil {
			mw = writer
			dst = io.MultiWriter(w, mw)
		} else {
			log.Debug("Metadata cache write unavailable", zap.Error(werr))
		}
	}

	n, err := io.Copy(dst, cond.Body)
	atomic.AddInt64(&s.requestsMirror, 1)
	atomic.AddInt64(&s.bytesFromMirror, n)
	if err != nil {
		if mw != nil {
			mw.Abort()
		}
		// The 200 header is already on the wire; the client sees a short read.
		log.Warn("Passthrough stream interrupted", zap.Int64("written", n), zap.Error(err))
		return
	}
	if mw != nil {
		if cerr := mw.Commit(); cerr != nil {
			log.Debug("Failed to cache metadata", zap.String("url", sanitize.URL(url)), zap.Error(cerr))
		}
	}
}

// storeMetadata caches an already-buffered metadata body (index files are
// buffered for parsing anyway, so they are stored from the buffer).
func (s *Server) storeMetadata(url string, data []byte, etag, lastModified, contentType string, log *zap.Logger) {
	mw, err := s.cache.NewMetadataWriter(url, etag, lastModified, contentType)
	if err != nil {
		log.Debug("Metadata cache write unavailable", zap.Error(err))
		return
	}
	if _, err := mw.Write(data); err != nil {
		mw.Abort()
		log.Debug("Failed to write metadata to cache", zap.Error(err))
		return
	}
	if err := mw.Commit(); err != nil {
		log.Debug("Failed to cache metadata", zap.String("url", sanitize.URL(url)), zap.Error(err))
	}
}

// serveCachedMetadata serves a metadata body from the cache. It answers with a
// 304 when the client's own validators show it already holds this exact copy,
// and (for index files) warms the in-memory index from the cached bytes if a
// restart left it cold. When stale is true the copy is served without an
// upstream revalidation (mirror unreachable / offline); it is flagged with an
// X-Debswarm-Stale header and counted separately. It takes ownership of rc.
func (s *Server) serveCachedMetadata(w http.ResponseWriter, r *http.Request, url string, isIndex bool, entry *cache.MetadataEntry, rc io.ReadCloser, stale bool) {
	ctx := r.Context()
	log := requestid.LoggerFromContext(ctx, s.logger)
	defer func() { _ = rc.Close() }()

	atomic.AddInt64(&s.metadataHits, 1)
	atomic.AddInt64(&s.metadataBytesSaved, entry.Size)
	if s.metrics != nil {
		s.metrics.MetadataCacheHits.Inc()
		s.metrics.MetadataCacheBytesSaved.Add(entry.Size)
	}
	if stale {
		// Set the marker header before any WriteHeader below.
		w.Header().Set("X-Debswarm-Stale", "true")
		if s.metrics != nil {
			s.metrics.MetadataCacheStaleServed.Inc()
		}
		s.noteStaleServe(log, url)
	}

	warmIndex := isIndex && isVerifiableIndexURL(url) && !s.index.HasIndexFile(url)
	clientHas := clientHasCurrent(r, entry)

	// Fast path: client already has it and there is no index to warm — 304 without
	// touching the body at all.
	if clientHas && !warmIndex {
		setValidatorHeaders(w, entry)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if isIndex {
		// Buffer to parse and/or resend the same bytes.
		data, err := io.ReadAll(rc)
		if err != nil {
			logFetchFailure(ctx, log, "Failed to read cached index", err)
			http.Error(w, "Failed to read cached index", http.StatusBadGateway)
			return
		}
		if warmIndex {
			// Only load a cached index into the in-memory index if it verifies
			// against the signed Release (enforce). warn loads it and flags the
			// response; the cached bytes are still served either way (APT verifies).
			if s.checkIndexVerification(w, url, data, log) {
				if err := s.loadIndexInto(url, data); err != nil {
					log.Debug("Failed to parse cached index file", zap.Error(err))
				}
			}
		}
		if clientHas {
			setValidatorHeaders(w, entry)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		setValidatorHeaders(w, entry)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}

	// Non-index: stream the cached body.
	if entry.ContentType != "" {
		w.Header().Set("Content-Type", entry.ContentType)
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", entry.Size))
	setValidatorHeaders(w, entry)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		log.Warn("Cached metadata stream interrupted", zap.Error(err))
	}
}

// fetchFreshMetadata does an unconditional upstream fetch and serves it. Used
// only when a cached file vanishes mid-request.
func (s *Server) fetchFreshMetadata(w http.ResponseWriter, r *http.Request, url string, isIndex bool) {
	ctx := r.Context()
	log := requestid.LoggerFromContext(ctx, s.logger)
	cond, err := s.fetcher.StreamConditional(ctx, s.upstreamFetchURL(url), "", "")
	if err != nil {
		logFetchFailure(ctx, log, "Failed to fetch metadata", err)
		http.Error(w, "Failed to fetch", http.StatusBadGateway)
		return
	}
	if cond.NotModified {
		relayValidators(w, cond)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.serveFreshBody(w, r, url, isIndex, cond, s.cache != nil && s.cache.MetadataEnabled())
}

// setValidatorHeaders copies a cache entry's ETag/Last-Modified onto a response.
func setValidatorHeaders(w http.ResponseWriter, entry *cache.MetadataEntry) {
	if entry.LastModified != "" {
		w.Header().Set("Last-Modified", entry.LastModified)
	}
	if entry.ETag != "" {
		w.Header().Set("ETag", entry.ETag)
	}
}

// clientHasCurrent reports whether the client's conditional-GET validators match
// the (now-confirmed-current) cached entry, so the client can be answered 304.
func clientHasCurrent(r *http.Request, entry *cache.MetadataEntry) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" && entry.ETag != "" {
		if inm == "*" || inm == entry.ETag {
			return true
		}
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" && entry.LastModified != "" {
		if ims == entry.LastModified {
			return true
		}
	}
	return false
}

// isPackagesIndexURL reports whether an index URL is a Packages file (parsed into
// the index) rather than a Sources file (which debswarm does not parse). Mirrors
// the original handleIndexRequest classification.
func isPackagesIndexURL(url string) bool {
	lower := strings.ToLower(url)
	if strings.Contains(lower, "/packages") && !strings.Contains(lower, "/translation") {
		return true
	}
	if strings.Contains(lower, "/by-hash/") {
		// Dist-layout Packages by-hash, or a flat-layout repo's by-hash (which has
		// no binary-*/ path). Source by-hash is intentionally excluded here (Sources
		// files are not parsed into the Packages index).
		if strings.Contains(lower, "/binary-") || isFlatByHash(lower) {
			return true
		}
	}
	return false
}

// isFlatByHash reports whether a URL already known to contain "/by-hash/" belongs
// to a flat-layout repository (no /dists/ tree) rather than a known non-Packages
// component (i18n translations, cnf command-not-found, dep11 appstream). In a flat
// repo the by-hash directory sits directly under the repo base, so there is no
// binary-*/ path to classify by; such a file is a Packages index. The rare case of
// a flat repo's Translation-by-hash is misclassified as Packages but is harmless:
// it still verifies against the signed Release and parses to zero package entries.
func isFlatByHash(lower string) bool {
	return !strings.Contains(lower, "/dists/") &&
		!strings.Contains(lower, "/i18n/") &&
		!strings.Contains(lower, "/cnf/") &&
		!strings.Contains(lower, "/dep11/")
}

// isSourceArtifactURL reports whether a URL is a Debian source-package artifact —
// a .dsc, .diff.gz, an .orig/.debian/.orig-component tarball, or a native source
// tarball under /pool/. These are content-addressed and verified via the Sources
// index exactly like binary packages. Native tarballs are gated on /pool/ so
// dist-tree tarballs (e.g. installer netboot.tar.gz) are not caught; a false
// positive is harmless anyway — an artifact absent from the index just streams
// uncached, identical to the previous passthrough behaviour. The argument is
// expected to be already lowercased.
func isSourceArtifactURL(lower string) bool {
	switch {
	case strings.HasSuffix(lower, ".dsc"),
		strings.HasSuffix(lower, ".diff.gz"),
		strings.Contains(lower, ".orig.tar."),
		strings.Contains(lower, ".debian.tar."):
		return true
	case strings.Contains(lower, ".orig-") && strings.Contains(lower, ".tar."):
		// Additional-component orig tarball, e.g. foo_1.0.orig-extra.tar.xz.
		return true
	case strings.Contains(lower, "/pool/") &&
		(strings.HasSuffix(lower, ".tar.gz") ||
			strings.HasSuffix(lower, ".tar.xz") ||
			strings.HasSuffix(lower, ".tar.bz2") ||
			strings.HasSuffix(lower, ".tar.lz") ||
			strings.HasSuffix(lower, ".tar.zst")):
		// Native source package: <name>_<ver>.tar.{xz,gz,…} with no orig/debian.
		return true
	}
	return false
}

// isSourcesIndexURL reports whether a URL is a Debian Sources index (the
// source-package counterpart of a Packages index): dists/<suite>/<comp>/source/
// Sources{,.gz,.xz,…} or its Acquire-By-Hash form. Kept separate from
// isPackagesIndexURL because a Sources index must be GPG-verified but parsed by
// the Sources parser, not loaded into the binary Packages index.
func isSourcesIndexURL(url string) bool {
	lower := strings.ToLower(url)
	if strings.Contains(lower, "/pool/") {
		return false
	}
	if !strings.Contains(lower, "/source/") {
		return false
	}
	return strings.Contains(lower, "/sources") || strings.Contains(lower, "/by-hash/")
}

// isVerifiableIndexURL reports whether a URL is an index whose bytes debswarm
// gates against the signature-verified Release — a Packages or a Sources index.
func isVerifiableIndexURL(url string) bool {
	return isPackagesIndexURL(url) || isSourcesIndexURL(url)
}

// loadIndexInto parses an index response into the in-memory index, dispatching to
// the Packages or the Sources parser by URL. It is a no-op for non-index URLs.
func (s *Server) loadIndexInto(url string, data []byte) error {
	switch {
	case isPackagesIndexURL(url):
		return s.index.LoadFromData(data, url)
	case isSourcesIndexURL(url):
		return s.index.LoadSourcesFromData(data, url)
	}
	return nil
}

// noteStaleServe logs, at most once per repository host, that debswarm is serving
// stale cached metadata because the mirror is unreachable. The
// metadata_cache_stale_served_total metric carries the full per-file count;
// repeat serves for an already-logged host are logged at DEBUG.
func (s *Server) noteStaleServe(log *zap.Logger, rawURL string) {
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		host = u.Host
	}
	if _, seen := s.staleHostsSeen.LoadOrStore(host, struct{}{}); seen {
		log.Debug("Serving stale metadata (mirror unreachable)", zap.String("host", host))
		return
	}
	log.Warn("Mirror unreachable: serving stale cached metadata so apt can continue "+
		"from the last-known-good copy (apt still verifies signatures and Valid-Until). "+
		"This host will not log again until restart.",
		zap.String("host", host))
}

// streamUncachedPackage serves a package that has no signed index entry by
// streaming it straight from the mirror to the client. There is no trusted
// hash, so it is never verified, cached, or shared — and therefore nothing is
// held in memory regardless of package size.
func (s *Server) streamUncachedPackage(w http.ResponseWriter, r *http.Request, url, path string) {
	ctx := r.Context()
	log := requestid.LoggerFromContext(ctx, s.logger)
	reqID := requestid.FromContext(ctx)

	body, size, err := s.fetcher.Stream(ctx, s.upstreamFetchURL(url))
	if err != nil {
		logFetchFailure(ctx, log, "Mirror fetch failed", err)
		s.audit.Log(audit.NewDownloadFailedEvent("", path, err.Error()).WithRequestID(reqID))
		http.Error(w, "Failed to fetch package", http.StatusBadGateway)
		return
	}
	defer func() { _ = body.Close() }()

	atomic.AddInt64(&s.requestsMirror, 1)
	s.metrics.DownloadsTotal.WithLabel(downloader.SourceTypeMirror).Inc()

	w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
	if size >= 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	}
	w.Header().Set("X-Debswarm-Source", downloader.SourceTypeMirror)
	w.WriteHeader(http.StatusOK)

	n, copyErr := io.Copy(w, body)
	atomic.AddInt64(&s.bytesFromMirror, n)
	s.metrics.BytesDownloaded.WithLabel(downloader.SourceTypeMirror).Add(n)
	if copyErr != nil {
		// The 200 header is already on the wire; the client sees a short read.
		log.Warn("Uncached package stream interrupted", zap.Int64("written", n), zap.Error(copyErr))
		return
	}
	s.audit.Log(audit.NewDownloadCompleteEvent("", path, n, downloader.SourceTypeMirror, 0, 0, n).WithRequestID(reqID))
}

// noteUncachedServe logs, at most once per repository host, that packages from
// that host are being served directly from the mirror without caching,
// verification, or P2P sharing because no signed index entry was found. The
// packages_served_uncached_total metric carries the full per-package count;
// repeat serves for an already-logged host are logged at DEBUG.
func (s *Server) noteUncachedServe(log *zap.Logger, rawURL string) {
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		host = u.Host
	}
	if _, seen := s.uncachedHostsSeen.LoadOrStore(host, struct{}{}); seen {
		log.Debug("Served package uncached (no signed index entry)", zap.String("host", host))
		return
	}
	log.Info("Serving packages from this repository uncached: no signed index entry was found, "+
		"so they are not verified, cached, or shared over P2P. Run 'apt-get update' through the "+
		"debswarm proxy so it can read the repository's signed Packages index.",
		zap.String("host", host))
}

// serveFromCache streams a cached package to the client. It returns an error
// (without having written a response) when the cache entry cannot be opened —
// notably when database corruption recovery left the package file on disk
// with no metadata row, in which case Has() is true but Get() fails. Callers
// that can re-download must treat that as a cache miss, not a hard failure.
func (s *Server) serveFromCache(w http.ResponseWriter, hash string) error {
	reader, pkg, err := s.cache.Get(hash)
	if err != nil {
		return err
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", pkg.Size))
	w.Header().Set("X-Debswarm-Source", "cache")
	w.WriteHeader(http.StatusOK)

	_, _ = io.Copy(w, reader)
	return nil
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

	// Each Provide is a multi-second DHT walk; done one at a time, a cache of
	// thousands of packages takes hours per reannounce cycle and undermines
	// the announce interval. Announce with bounded concurrency instead — the
	// same width as the async announcement worker.
	const maxConcurrent = 4
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for _, pkg := range packages {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(hash string) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := s.p2pNode.Provide(ctx, hash); err != nil {
				s.logger.Debug("Failed to announce package",
					zap.String("hash", hash[:16]+"..."),
					zap.Error(err))
				return
			}
			if err := s.cache.MarkAnnounced(hash); err != nil {
				s.logger.Warn("Failed to mark as announced", zap.Error(err))
			}
		}(pkg.SHA256)
	}

	wg.Wait()
	return nil
}

// CleanupDownloadState purges failed and abandoned download state rows and
// orphaned partial-download directories. Failed downloads stop being retried
// after the retry window, but their state rows and multi-MB partial assembly
// files were never garbage-collected — unbounded disk and database growth on
// a daemon facing flaky mirrors or peers.
func (s *Server) CleanupDownloadState(maxAge time.Duration) {
	if s.stateManager != nil {
		if n, err := s.stateManager.CleanupStale(maxAge); err != nil {
			s.logger.Warn("Failed to clean up stale download state", zap.Error(err))
		} else if n > 0 {
			s.logger.Info("Cleaned up stale download state", zap.Int("removed", n))
		}
	}

	n, err := s.cache.SweepStalePartials(maxAge, func(hash string) bool {
		if s.stateManager == nil {
			return false
		}
		state, err := s.stateManager.GetDownload(hash)
		return err == nil && state != nil
	})
	if err != nil {
		s.logger.Warn("Failed to sweep stale partial downloads", zap.Error(err))
	} else if n > 0 {
		s.logger.Info("Swept stale partial download directories", zap.Int("removed", n))
	}
}

// UpdateMetrics updates metrics from current state
func (s *Server) UpdateMetrics() {
	if s.metrics == nil {
		return
	}

	s.metrics.CacheSize.Set(float64(s.cache.Size()))
	s.metrics.CacheCount.Set(float64(s.cache.Count()))
	s.metrics.MetadataCacheSize.Set(float64(s.cache.MetadataSize()))

	if s.p2pNode != nil {
		s.metrics.ConnectedPeers.Set(float64(s.p2pNode.ConnectedPeers()))
		s.metrics.RoutingTableSize.Set(float64(s.p2pNode.RoutingTableSize()))
	}

	// Update scheduler metrics
	if s.scheduler != nil {
		if s.scheduler.IsInWindow() {
			s.metrics.SchedulerWindowActive.Set(1)
		} else {
			s.metrics.SchedulerWindowActive.Set(0)
		}
		s.metrics.SchedulerCurrentRate.Set(float64(s.scheduler.GetCurrentRate(false)))
	}

	// Update fleet metrics
	if s.fleet != nil {
		status := s.fleet.Status()
		s.metrics.FleetPeers.Set(float64(status.PeerCount))
		s.metrics.FleetInFlight.Set(float64(status.InFlightCount))
	}
}

// handleConnect handles HTTP CONNECT requests for HTTPS tunneling.
// This allows APT to use HTTPS repositories through the proxy.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := requestid.LoggerFromContext(ctx, s.logger)
	reqID := requestid.FromContext(ctx)

	atomic.AddInt64(&s.connectTotal, 1)
	s.metrics.ConnectRequestsTotal.Inc()

	// Parse target host:port from request
	targetHost := r.Host
	if targetHost == "" {
		targetHost = r.URL.Host
	}

	host, port, err := net.SplitHostPort(targetHost)
	if err != nil {
		// If no port, assume 443 for CONNECT
		host = targetHost
		port = "443"
		targetHost = net.JoinHostPort(host, port)
	}

	log.Debug("CONNECT request",
		zap.String("target", targetHost),
		zap.String("remoteAddr", r.RemoteAddr))

	// Security: Validate target against allowed patterns
	if !security.IsAllowedConnectTargetWithHosts(targetHost, s.allowedHosts) {
		log.Warn("Blocked CONNECT to non-allowed target",
			zap.String("target", targetHost),
			zap.String("remoteAddr", r.RemoteAddr))
		atomic.AddInt64(&s.connectFailed, 1)
		s.metrics.ConnectRequestsFailed.Inc()
		s.audit.Log(audit.NewConnectTunnelBlockedEvent(host, port, "not_allowed").WithRequestID(reqID))
		http.Error(w, fmt.Sprintf(
			"debswarm: HTTPS repository host %q is not in the allowed list. If this is a legitimate repository, "+
				"add its host to proxy.allowed_hosts in your debswarm config.", host),
			http.StatusForbidden)
		return
	}

	// Establish connection to target using context-aware dialer
	dialTimeout := s.timeouts.Get(timeouts.OpTunnelConnect)
	dialer := &net.Dialer{Timeout: dialTimeout}
	dialCtx, dialCancel := context.WithTimeout(ctx, dialTimeout)
	defer dialCancel()
	targetConn, err := dialer.DialContext(dialCtx, "tcp", targetHost)
	if err != nil {
		log.Error("Failed to connect to target",
			zap.String("target", targetHost),
			zap.Error(err))
		atomic.AddInt64(&s.connectFailed, 1)
		s.metrics.ConnectRequestsFailed.Inc()
		s.audit.Log(audit.NewConnectTunnelBlockedEvent(host, port, err.Error()).WithRequestID(reqID))
		http.Error(w, "Failed to connect to target", http.StatusBadGateway)
		return
	}

	// Check resolved IP to prevent DNS rebinding attacks.
	// The hostname passed validation above, but DNS could resolve to a private IP
	// between our check and the actual connection.
	if tcpAddr, ok := targetConn.RemoteAddr().(*net.TCPAddr); ok {
		if security.IsBlockedIP(tcpAddr.IP) {
			_ = targetConn.Close()
			log.Warn("Blocked CONNECT tunnel - target resolved to private IP (DNS rebinding)",
				zap.String("target", targetHost),
				zap.String("resolvedIP", tcpAddr.IP.String()))
			atomic.AddInt64(&s.connectFailed, 1)
			s.metrics.ConnectRequestsFailed.Inc()
			s.audit.Log(audit.NewConnectTunnelBlockedEvent(host, port, "dns_rebinding_blocked").WithRequestID(reqID))
			http.Error(w, "CONNECT target resolved to blocked address", http.StatusForbidden)
			return
		}
	}

	// Hijack the client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		log.Error("ResponseWriter does not support hijacking")
		_ = targetConn.Close()
		atomic.AddInt64(&s.connectFailed, 1)
		s.metrics.ConnectRequestsFailed.Inc()
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Error("Failed to hijack connection", zap.Error(err))
		_ = targetConn.Close()
		atomic.AddInt64(&s.connectFailed, 1)
		s.metrics.ConnectRequestsFailed.Inc()
		http.Error(w, "Failed to hijack connection", http.StatusInternalServerError)
		return
	}

	// Send 200 Connection Established
	_, err = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if err != nil {
		log.Error("Failed to send 200 response", zap.Error(err))
		_ = clientConn.Close()
		_ = targetConn.Close()
		atomic.AddInt64(&s.connectFailed, 1)
		s.metrics.ConnectRequestsFailed.Inc()
		return
	}

	// Audit log tunnel start
	s.audit.Log(audit.NewConnectTunnelStartEvent(host, port).WithRequestID(reqID))

	// Track active tunnels
	atomic.AddInt64(&s.activeTunnels, 1)
	s.metrics.ActiveTunnels.Inc()

	// Start tunnel - bidirectional copy
	startTime := time.Now()
	bytesIn, bytesOut := s.tunnel(clientConn, targetConn)
	duration := time.Since(startTime)

	// Update stats
	atomic.AddInt64(&s.activeTunnels, -1)
	s.metrics.ActiveTunnels.Dec()
	atomic.AddInt64(&s.tunnelBytesIn, bytesIn)
	atomic.AddInt64(&s.tunnelBytesOut, bytesOut)
	s.metrics.TunnelBytesIn.Add(bytesIn)
	s.metrics.TunnelBytesOut.Add(bytesOut)
	s.metrics.TunnelDuration.Observe(duration.Seconds())

	// Audit log tunnel end
	s.audit.Log(audit.NewConnectTunnelEndEvent(host, port, bytesIn+bytesOut, duration.Milliseconds()).WithRequestID(reqID))

	log.Debug("CONNECT tunnel closed",
		zap.String("target", targetHost),
		zap.Int64("bytesIn", bytesIn),
		zap.Int64("bytesOut", bytesOut),
		zap.Duration("duration", duration))
}

// tunnel copies data bidirectionally between client and target connections.
// Returns bytes transferred in each direction.
func (s *Server) tunnel(client, target net.Conn) (int64, int64) {
	idleTimeout := s.timeouts.Get(timeouts.OpTunnelIdle)

	var bytesIn, bytesOut int64
	var wg sync.WaitGroup
	wg.Add(2)

	// Client -> Target
	go func() {
		defer wg.Done()
		n := s.copyWithIdleTimeout(target, client, idleTimeout)
		atomic.AddInt64(&bytesOut, n)
		// Signal other goroutine to stop by closing write side
		if tcpConn, ok := target.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		} else {
			_ = target.SetDeadline(time.Now())
		}
	}()

	// Target -> Client
	go func() {
		defer wg.Done()
		n := s.copyWithIdleTimeout(client, target, idleTimeout)
		atomic.AddInt64(&bytesIn, n)
		// Signal other goroutine to stop by closing write side
		if tcpConn, ok := client.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		} else {
			_ = client.SetDeadline(time.Now())
		}
	}()

	wg.Wait()

	// Close connections
	_ = client.Close()
	_ = target.Close()

	return bytesIn, bytesOut
}

// copyWithIdleTimeout copies data from src to dst, resetting deadline on each read.
// This keeps the connection alive during active transfer but times out idle connections.
func (s *Server) copyWithIdleTimeout(dst, src net.Conn, idleTimeout time.Duration) int64 {
	buf := make([]byte, 32*1024) // 32KB buffer
	var total int64

	for {
		// Reset deadline on each read
		_ = src.SetReadDeadline(time.Now().Add(idleTimeout))

		nr, readErr := src.Read(buf)
		if nr > 0 {
			nw, writeErr := dst.Write(buf[:nr])
			if nw > 0 {
				total += int64(nw)
			}
			if writeErr != nil {
				return total
			}
			if nr != nw {
				return total
			}
		}
		if readErr != nil {
			return total
		}
	}
}
