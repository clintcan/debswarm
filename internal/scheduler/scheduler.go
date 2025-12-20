package scheduler

import (
	"strings"
	"time"

	"go.uber.org/zap"
)

// Scheduler controls download rates based on configured time windows.
// During sync windows, downloads run at full speed (or configured inside rate).
// Outside windows, downloads are rate-limited to the outside rate.
// Security updates can optionally bypass rate limits entirely.
type Scheduler struct {
	windows         []*ParsedWindow
	timezone        *time.Location
	outsideRate     int64 // bytes/sec outside window (0 = unlimited)
	insideRate      int64 // bytes/sec inside window (0 = unlimited)
	urgentFullSpeed bool
	logger          *zap.Logger
}

// Config holds scheduler configuration.
type Config struct {
	Enabled           bool
	Windows           []Window
	Timezone          string // IANA timezone name (e.g., "America/New_York")
	OutsideWindowRate int64  // bytes/sec, 0 = unlimited
	InsideWindowRate  int64  // bytes/sec, 0 = unlimited
	UrgentFullSpeed   bool   // security updates always get full speed
}

// New creates a new Scheduler from configuration.
// Returns nil if scheduler is disabled.
func New(cfg *Config, logger *zap.Logger) (*Scheduler, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}

	// Parse timezone
	tz := time.UTC
	if cfg.Timezone != "" {
		loc, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			logger.Warn("Invalid timezone, using UTC",
				zap.String("timezone", cfg.Timezone),
				zap.Error(err))
		} else {
			tz = loc
		}
	}

	// Parse windows
	windows := make([]*ParsedWindow, 0, len(cfg.Windows))
	for i, w := range cfg.Windows {
		pw, err := ParseWindow(w)
		if err != nil {
			logger.Warn("Invalid sync window, skipping",
				zap.Int("index", i),
				zap.Error(err))
			continue
		}
		windows = append(windows, pw)
	}

	if len(windows) == 0 {
		logger.Warn("No valid sync windows configured, scheduler will not rate limit")
	}

	return &Scheduler{
		windows:         windows,
		timezone:        tz,
		outsideRate:     cfg.OutsideWindowRate,
		insideRate:      cfg.InsideWindowRate,
		urgentFullSpeed: cfg.UrgentFullSpeed,
		logger:          logger,
	}, nil
}

// IsInWindow returns true if the current time is within any configured sync window.
func (s *Scheduler) IsInWindow() bool {
	if s == nil || len(s.windows) == 0 {
		return true // No windows = always in window (no restrictions)
	}

	now := time.Now().In(s.timezone)
	for _, w := range s.windows {
		if w.Contains(now) {
			return true
		}
	}
	return false
}

// GetCurrentRate returns the current rate limit in bytes/sec.
// Returns 0 for unlimited rate.
// If isUrgent is true and UrgentFullSpeed is enabled, returns 0 (unlimited).
func (s *Scheduler) GetCurrentRate(isUrgent bool) int64 {
	if s == nil {
		return 0 // No scheduler = unlimited
	}

	// Security updates bypass rate limits if configured
	if isUrgent && s.urgentFullSpeed {
		return 0
	}

	if s.IsInWindow() {
		return s.insideRate
	}
	return s.outsideRate
}

// NextWindowStart returns when the next sync window opens.
// Returns zero time if already in a window or no windows configured.
func (s *Scheduler) NextWindowStart() time.Time {
	if s == nil || len(s.windows) == 0 {
		return time.Time{}
	}

	now := time.Now().In(s.timezone)

	// Check if already in a window
	if s.IsInWindow() {
		return time.Time{}
	}

	// Find the earliest next window start
	var earliest time.Time
	for _, w := range s.windows {
		next := w.NextStart(now)
		if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
			earliest = next
		}
	}
	return earliest
}

// IsSecurityUpdate checks if a URL appears to be a security update.
func IsSecurityUpdate(url string) bool {
	lowerURL := strings.ToLower(url)
	return strings.Contains(lowerURL, "-security") ||
		strings.Contains(lowerURL, "/security/") ||
		strings.Contains(lowerURL, "-updates") ||
		strings.Contains(lowerURL, "/updates/")
}

// Status returns the current scheduler status for monitoring.
type Status struct {
	InWindow       bool
	CurrentRate    int64     // bytes/sec, 0 = unlimited
	NextWindowOpen time.Time // zero if in window or no windows
	Timezone       string
	WindowCount    int
}

// Status returns the current scheduler status.
func (s *Scheduler) Status() Status {
	if s == nil {
		return Status{
			InWindow:    true,
			CurrentRate: 0,
		}
	}

	inWindow := s.IsInWindow()
	var currentRate int64
	if inWindow {
		currentRate = s.insideRate
	} else {
		currentRate = s.outsideRate
	}

	return Status{
		InWindow:       inWindow,
		CurrentRate:    currentRate,
		NextWindowOpen: s.NextWindowStart(),
		Timezone:       s.timezone.String(),
		WindowCount:    len(s.windows),
	}
}
