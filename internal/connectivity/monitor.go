// Package connectivity provides network connectivity monitoring for debswarm
package connectivity

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Mode represents the current connectivity state
type Mode int32

const (
	// ModeOnline indicates full internet connectivity (DHT + mDNS + mirrors)
	ModeOnline Mode = iota
	// ModeLANOnly indicates only local network available (mDNS peers only)
	ModeLANOnly
	// ModeOffline indicates no network connectivity (cache only)
	ModeOffline
)

// String returns a human-readable name for the mode
func (m Mode) String() string {
	switch m {
	case ModeOnline:
		return "online"
	case ModeLANOnly:
		return "lan_only"
	case ModeOffline:
		return "offline"
	default:
		return "unknown"
	}
}

// Config holds connectivity monitor configuration
type Config struct {
	// Mode is the configured connectivity mode ("auto", "lan_only", "online_only")
	Mode string

	// CheckInterval is how often to check connectivity when in auto mode
	CheckInterval time.Duration

	// CheckURL is the URL to use for connectivity checks (typically a mirror)
	CheckURL string

	// CheckTimeout is the timeout for connectivity checks
	CheckTimeout time.Duration

	// OnModeChange is called when connectivity mode changes
	OnModeChange func(old, new Mode)

	// GetMDNSPeerCount returns the number of connected mDNS peers
	GetMDNSPeerCount func() int
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		Mode:          "auto",
		CheckInterval: 30 * time.Second,
		CheckURL:      "http://deb.debian.org/debian/",
		CheckTimeout:  5 * time.Second,
	}
}

// Monitor monitors network connectivity and determines the best operating mode
type Monitor struct {
	mode          atomic.Int32
	configMode    string
	checkInterval time.Duration
	checkURL      string
	checkTimeout  time.Duration
	onModeChange  func(old, new Mode)
	getMDNSPeers  func() int
	logger        *zap.Logger
	client        *http.Client
}

// NewMonitor creates a new connectivity monitor
func NewMonitor(cfg *Config, logger *zap.Logger) *Monitor {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// Set defaults
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 30 * time.Second
	}
	if cfg.CheckURL == "" {
		cfg.CheckURL = "http://deb.debian.org/debian/"
	}
	if cfg.CheckTimeout <= 0 {
		cfg.CheckTimeout = 5 * time.Second
	}

	m := &Monitor{
		configMode:    cfg.Mode,
		checkInterval: cfg.CheckInterval,
		checkURL:      cfg.CheckURL,
		checkTimeout:  cfg.CheckTimeout,
		onModeChange:  cfg.OnModeChange,
		getMDNSPeers:  cfg.GetMDNSPeerCount,
		logger:        logger,
		client: &http.Client{
			Timeout: cfg.CheckTimeout,
		},
	}

	// Set initial mode based on configuration
	switch cfg.Mode {
	case "lan_only":
		m.mode.Store(int32(ModeLANOnly))
	case "online_only":
		m.mode.Store(int32(ModeOnline))
	default: // "auto" or unset
		m.mode.Store(int32(ModeOnline)) // Assume online until proven otherwise
	}

	return m
}

// GetMode returns the current connectivity mode
func (m *Monitor) GetMode() Mode {
	return Mode(m.mode.Load())
}

// Start starts the connectivity monitor
// It runs periodic checks in the background when in auto mode
func (m *Monitor) Start(ctx context.Context) {
	// Only run periodic checks in auto mode
	if m.configMode != "auto" && m.configMode != "" {
		m.logger.Info("Connectivity monitor in static mode",
			zap.String("mode", m.configMode))
		return
	}

	m.logger.Info("Starting connectivity monitor",
		zap.Duration("checkInterval", m.checkInterval),
		zap.String("checkURL", m.checkURL))

	// Run initial check
	m.checkAndUpdate(ctx)

	// Start periodic checks
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Debug("Connectivity monitor stopping")
			return
		case <-ticker.C:
			m.checkAndUpdate(ctx)
		}
	}
}

// checkAndUpdate performs a connectivity check and updates the mode
func (m *Monitor) checkAndUpdate(ctx context.Context) {
	newMode := m.checkConnectivity(ctx)
	oldMode := Mode(m.mode.Swap(int32(newMode)))

	if oldMode != newMode {
		m.logger.Info("Connectivity mode changed",
			zap.String("from", oldMode.String()),
			zap.String("to", newMode.String()))

		if m.onModeChange != nil {
			m.onModeChange(oldMode, newMode)
		}
	}
}

// checkConnectivity performs a connectivity check and returns the appropriate mode
func (m *Monitor) checkConnectivity(ctx context.Context) Mode {
	// Try to reach the configured URL
	checkCtx, cancel := context.WithTimeout(ctx, m.checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, "HEAD", m.checkURL, nil)
	if err != nil {
		m.logger.Debug("Failed to create connectivity check request", zap.Error(err))
		return m.determineOfflineMode()
	}

	resp, err := m.client.Do(req)
	if err != nil {
		m.logger.Debug("Connectivity check failed",
			zap.String("url", m.checkURL),
			zap.Error(err))
		return m.determineOfflineMode()
	}
	defer resp.Body.Close()

	// Any response (even error codes) indicates internet connectivity
	m.logger.Debug("Connectivity check succeeded",
		zap.String("url", m.checkURL),
		zap.Int("statusCode", resp.StatusCode))
	return ModeOnline
}

// determineOfflineMode determines whether we're LAN_ONLY or fully OFFLINE
func (m *Monitor) determineOfflineMode() Mode {
	// Check if we have any mDNS peers
	if m.getMDNSPeers != nil && m.getMDNSPeers() > 0 {
		return ModeLANOnly
	}
	return ModeOffline
}

// ForceMode forces a specific connectivity mode (useful for testing)
func (m *Monitor) ForceMode(mode Mode) {
	oldMode := Mode(m.mode.Swap(int32(mode)))
	if oldMode != mode {
		m.logger.Info("Connectivity mode forced",
			zap.String("from", oldMode.String()),
			zap.String("to", mode.String()))
		if m.onModeChange != nil {
			m.onModeChange(oldMode, mode)
		}
	}
}
