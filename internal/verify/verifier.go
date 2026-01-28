// Package verify provides multi-source verification for downloaded packages.
// It queries the DHT to find other providers of the same content hash,
// providing confidence that the package hasn't been tampered with.
package verify

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/audit"
	"github.com/debswarm/debswarm/internal/metrics"
)

// ProviderFinder finds providers for a given hash
type ProviderFinder interface {
	FindProviders(ctx context.Context, sha256Hash string, limit int) ([]peer.AddrInfo, error)
	ID() peer.ID // Our own peer ID
}

// Config holds verifier configuration
type Config struct {
	Enabled       bool          // Enable multi-source verification
	MinProviders  int           // Minimum providers for "verified" status (default: 2)
	QueryTimeout  time.Duration // Timeout for DHT queries (default: 10s)
	MaxConcurrent int           // Max concurrent verifications (default: 4)
	QueryLimit    int           // Max providers to query for (default: 5)
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:       true,
		MinProviders:  2,
		QueryTimeout:  10 * time.Second,
		MaxConcurrent: 4,
		QueryLimit:    5,
	}
}

// Result represents a verification result
type Result struct {
	Hash          string
	ProviderCount int
	Verified      bool
	SelfOnly      bool // True if we're the only provider
	Error         error
	Duration      time.Duration
}

// Verifier performs multi-source verification
type Verifier struct {
	config  *Config
	finder  ProviderFinder
	logger  *zap.Logger
	metrics *metrics.Metrics
	audit   audit.Logger

	// Concurrency control
	sem     chan struct{}
	pending sync.WaitGroup

	// Shutdown
	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a new Verifier
func New(cfg *Config, finder ProviderFinder, logger *zap.Logger, m *metrics.Metrics, auditLog audit.Logger) *Verifier {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.MinProviders <= 0 {
		cfg.MinProviders = 2
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = 10 * time.Second
	}
	if cfg.QueryLimit <= 0 {
		cfg.QueryLimit = 5
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Verifier{
		config:  cfg,
		finder:  finder,
		logger:  logger,
		metrics: m,
		audit:   auditLog,
		sem:     make(chan struct{}, cfg.MaxConcurrent),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// VerifyAsync asynchronously verifies a package by querying for other providers.
// This is non-blocking and meant to be called after a successful download.
func (v *Verifier) VerifyAsync(hash, filename string) {
	if !v.config.Enabled || v.finder == nil {
		return
	}

	// Non-blocking acquire of semaphore
	select {
	case v.sem <- struct{}{}:
	default:
		// Too many concurrent verifications, skip
		v.logger.Debug("Verification queue full, skipping",
			zap.String("hash", truncateHash(hash)))
		return
	}

	v.pending.Add(1)
	go func() {
		defer func() {
			<-v.sem
			v.pending.Done()
		}()

		result := v.verify(hash)
		v.logResult(result, filename)
		v.recordMetrics(result)
		v.recordAudit(result, filename)
	}()
}

// verify performs the actual verification
func (v *Verifier) verify(hash string) *Result {
	start := time.Now()

	ctx, cancel := context.WithTimeout(v.ctx, v.config.QueryTimeout)
	defer cancel()

	providers, err := v.finder.FindProviders(ctx, hash, v.config.QueryLimit)
	duration := time.Since(start)

	if err != nil {
		return &Result{
			Hash:     hash,
			Error:    err,
			Duration: duration,
		}
	}

	// Count providers excluding ourselves
	ourID := v.finder.ID()
	otherProviders := 0
	for _, p := range providers {
		if p.ID != ourID {
			otherProviders++
		}
	}

	return &Result{
		Hash:          hash,
		ProviderCount: otherProviders,
		Verified:      otherProviders >= v.config.MinProviders,
		SelfOnly:      otherProviders == 0 && len(providers) > 0,
		Duration:      duration,
	}
}

// logResult logs the verification result
func (v *Verifier) logResult(r *Result, filename string) {
	hashStr := truncateHash(r.Hash)

	if r.Error != nil {
		v.logger.Debug("Verification query failed",
			zap.String("hash", hashStr),
			zap.String("file", filename),
			zap.Error(r.Error))
		return
	}

	if r.Verified {
		v.logger.Debug("Package verified by multiple sources",
			zap.String("hash", hashStr),
			zap.String("file", filename),
			zap.Int("providers", r.ProviderCount),
			zap.Duration("duration", r.Duration))
	} else if r.SelfOnly {
		v.logger.Info("Package unverified - no other providers found",
			zap.String("hash", hashStr),
			zap.String("file", filename),
			zap.Duration("duration", r.Duration))
	} else {
		v.logger.Debug("Package partially verified",
			zap.String("hash", hashStr),
			zap.String("file", filename),
			zap.Int("providers", r.ProviderCount),
			zap.Int("required", v.config.MinProviders),
			zap.Duration("duration", r.Duration))
	}
}

// recordMetrics records verification metrics
func (v *Verifier) recordMetrics(r *Result) {
	if v.metrics == nil {
		return
	}

	if r.Error != nil {
		v.metrics.VerificationResults.WithLabel("error").Inc()
		return
	}

	if r.Verified {
		v.metrics.VerificationResults.WithLabel("verified").Inc()
	} else if r.SelfOnly {
		v.metrics.VerificationResults.WithLabel("unverified").Inc()
	} else {
		v.metrics.VerificationResults.WithLabel("partial").Inc()
	}

	v.metrics.VerificationProviders.Observe(float64(r.ProviderCount))
	v.metrics.VerificationDuration.Observe(r.Duration.Seconds())
}

// recordAudit records an audit log event
func (v *Verifier) recordAudit(r *Result, filename string) {
	if v.audit == nil || r.Error != nil {
		return
	}

	eventType := audit.EventMultiSourceVerified
	if r.SelfOnly || !r.Verified {
		eventType = audit.EventMultiSourceUnverified
	}

	v.audit.Log(audit.Event{
		EventType:   eventType,
		PackageName: filename,
		PackageHash: r.Hash,
		Timestamp:   time.Now(),
		DurationMs:  r.Duration.Milliseconds(),
		ChunksTotal: r.ProviderCount, // Reuse ChunksTotal for provider count
	})
}

// Close shuts down the verifier and waits for pending verifications
func (v *Verifier) Close() error {
	v.cancel()
	v.pending.Wait()
	return nil
}

// truncateHash returns a truncated hash for logging
func truncateHash(hash string) string {
	if len(hash) > 16 {
		return hash[:16] + "..."
	}
	return hash
}
