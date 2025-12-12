// Package mirror handles fetching packages from HTTP mirrors
package mirror

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Stats holds statistics for a mirror
type Stats struct {
	URL              string
	AvgLatencyMs     float64
	AvgThroughputBps float64
	ErrorCount       int
	SuccessCount     int
	LastContact      time.Time
}

// Fetcher handles downloading from HTTP mirrors
type Fetcher struct {
	client     *http.Client
	stats      map[string]*Stats
	statsMu    sync.RWMutex
	logger     *zap.Logger
	userAgent  string
	maxRetries int
}

// Config holds mirror fetcher configuration
type Config struct {
	Timeout     time.Duration
	MaxRetries  int
	UserAgent   string
	MaxIdleConn int
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		Timeout:     60 * time.Second,
		MaxRetries:  3,
		UserAgent:   "debswarm/1.0",
		MaxIdleConn: 10,
	}
}

// NewFetcher creates a new mirror fetcher
func NewFetcher(cfg *Config, logger *zap.Logger) *Fetcher {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	transport := &http.Transport{
		MaxIdleConnsPerHost: cfg.MaxIdleConn,
		IdleConnTimeout:     90 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
	}

	return &Fetcher{
		client:     client,
		stats:      make(map[string]*Stats),
		logger:     logger,
		userAgent:  cfg.UserAgent,
		maxRetries: cfg.MaxRetries,
	}
}

// Fetch downloads content from a URL
func (f *Fetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	var lastErr error
	for attempt := 0; attempt < f.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with context check
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}

		resp, err := f.client.Do(req)
		if err != nil {
			lastErr = err
			f.recordError(url)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				// Don't retry client errors
				f.recordError(url)
				return nil, lastErr
			}
			f.recordError(url)
			continue
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			f.recordError(url)
			continue
		}

		// Record success
		duration := time.Since(start)
		f.recordSuccess(url, int64(len(data)), duration)

		return data, nil
	}

	return nil, fmt.Errorf("failed after %d retries: %w", f.maxRetries, lastErr)
}

// FetchToWriter downloads content and writes to a writer
func (f *Fetcher) FetchToWriter(ctx context.Context, url string, w io.Writer) (int64, error) {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	var lastErr error
	for attempt := 0; attempt < f.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with context check
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}

		resp, err := f.client.Do(req)
		if err != nil {
			lastErr = err
			f.recordError(url)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				f.recordError(url)
				return 0, lastErr
			}
			f.recordError(url)
			continue
		}

		written, err := io.Copy(w, resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			f.recordError(url)
			continue
		}

		duration := time.Since(start)
		f.recordSuccess(url, written, duration)
		return written, nil
	}

	return 0, fmt.Errorf("failed after %d retries: %w", f.maxRetries, lastErr)
}

// Stream returns a reader for streaming content
func (f *Fetcher) Stream(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		f.recordError(url)
		return nil, 0, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		f.recordError(url)
		return nil, 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	return resp.Body, resp.ContentLength, nil
}

// Head performs a HEAD request to get content info without downloading
func (f *Fetcher) Head(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	return f.client.Do(req)
}

func (f *Fetcher) recordSuccess(url string, bytes int64, duration time.Duration) {
	host := extractHost(url)

	f.statsMu.Lock()
	defer f.statsMu.Unlock()

	stats, ok := f.stats[host]
	if !ok {
		stats = &Stats{URL: host}
		f.stats[host] = stats
	}

	stats.SuccessCount++
	stats.LastContact = time.Now()

	// Update running averages
	n := float64(stats.SuccessCount)
	latencyMs := float64(duration.Milliseconds())
	throughputBps := float64(bytes) / duration.Seconds()

	stats.AvgLatencyMs = stats.AvgLatencyMs*(n-1)/n + latencyMs/n
	stats.AvgThroughputBps = stats.AvgThroughputBps*(n-1)/n + throughputBps/n
}

func (f *Fetcher) recordError(url string) {
	host := extractHost(url)

	f.statsMu.Lock()
	defer f.statsMu.Unlock()

	stats, ok := f.stats[host]
	if !ok {
		stats = &Stats{URL: host}
		f.stats[host] = stats
	}

	stats.ErrorCount++
	stats.LastContact = time.Now()
}

// GetStats returns statistics for all mirrors
func (f *Fetcher) GetStats() []*Stats {
	f.statsMu.RLock()
	defer f.statsMu.RUnlock()

	result := make([]*Stats, 0, len(f.stats))
	for _, s := range f.stats {
		// Create a copy
		statsCopy := *s
		result = append(result, &statsCopy)
	}
	return result
}

// GetMirrorStats returns statistics for a specific mirror
func (f *Fetcher) GetMirrorStats(url string) *Stats {
	host := extractHost(url)

	f.statsMu.RLock()
	defer f.statsMu.RUnlock()

	if s, ok := f.stats[host]; ok {
		statsCopy := *s
		return &statsCopy
	}
	return nil
}

func extractHost(url string) string {
	// Simple extraction of host from URL
	start := 0
	if len(url) > 8 && url[:8] == "https://" {
		start = 8
	} else if len(url) > 7 && url[:7] == "http://" {
		start = 7
	}

	end := start
	for end < len(url) && url[end] != '/' && url[end] != ':' {
		end++
	}

	return url[start:end]
}
