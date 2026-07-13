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

	"github.com/debswarm/debswarm/internal/httpclient"
	"github.com/debswarm/debswarm/internal/retry"
	"github.com/debswarm/debswarm/internal/security"
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
	client          *http.Client
	stats           map[string]*Stats
	statsMu         sync.RWMutex
	logger          *zap.Logger
	userAgent       string
	maxRetries      int
	maxResponseSize int64
	stallWindow     time.Duration
}

// Config holds mirror fetcher configuration
type Config struct {
	// Timeout bounds LACK OF PROGRESS, not the whole transfer: it is the
	// response-header wait and the per-read stall window. A whole-request
	// deadline was used historically, but it killed any healthy download that
	// simply took longer than the limit (a large package on a slow link),
	// then re-downloaded it from byte zero on each retry.
	Timeout         time.Duration
	MaxRetries      int
	UserAgent       string
	MaxIdleConn     int
	MaxResponseSize int64 // Maximum response size in bytes (0 = default 500MB)
}

// DefaultMaxResponseSize is the default maximum response size (500MB)
// This prevents memory exhaustion from malicious or misconfigured mirrors
const DefaultMaxResponseSize = 500 * 1024 * 1024

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		Timeout:         60 * time.Second,
		MaxRetries:      3,
		UserAgent:       "debswarm/1.0",
		MaxIdleConn:     10,
		MaxResponseSize: DefaultMaxResponseSize,
	}
}

// NewFetcher creates a new mirror fetcher
func NewFetcher(cfg *Config, logger *zap.Logger) *Fetcher {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	client := httpclient.New(&httpclient.Config{
		Timeout:               -1, // no whole-request deadline; stalls are bounded per-read below
		ResponseHeaderTimeout: cfg.Timeout,
		MaxIdleConnsPerHost:   cfg.MaxIdleConn,
		CheckRedirect:         checkRedirectSafety,
	})

	maxResponseSize := cfg.MaxResponseSize
	if maxResponseSize <= 0 {
		maxResponseSize = DefaultMaxResponseSize
	}

	stallWindow := cfg.Timeout
	if stallWindow <= 0 {
		stallWindow = 60 * time.Second
	}

	return &Fetcher{
		client:          client,
		stats:           make(map[string]*Stats),
		logger:          logger,
		userAgent:       cfg.UserAgent,
		maxRetries:      cfg.MaxRetries,
		maxResponseSize: maxResponseSize,
		stallWindow:     stallWindow,
	}
}

// stallReader aborts a transfer that stops making progress: every successful
// read re-arms a timer, and if no bytes arrive within the stall window the
// request context is canceled, unblocking the pending read with an error.
// Close releases the derived context.
type stallReader struct {
	r      io.ReadCloser
	timer  *time.Timer
	window time.Duration
	cancel context.CancelFunc
}

func newStallReader(body io.ReadCloser, window time.Duration, cancel context.CancelFunc) *stallReader {
	return &stallReader{
		r:      body,
		timer:  time.AfterFunc(window, cancel),
		window: window,
		cancel: cancel,
	}
}

func (sr *stallReader) Read(p []byte) (int, error) {
	n, err := sr.r.Read(p)
	if err == nil {
		sr.timer.Reset(sr.window)
	} else {
		sr.timer.Stop()
	}
	return n, err
}

func (sr *stallReader) Close() error {
	sr.timer.Stop()
	sr.cancel()
	return sr.r.Close()
}

// doStallGuarded issues the request on a cancelable child context and replaces
// the response body with a stall-guarded reader, so a transfer that makes no
// progress for the stall window is aborted (and, being retryable, re-attempted
// by callers that retry) instead of hanging or — with the old whole-request
// timeout — killing healthy long transfers.
func (f *Fetcher) doStallGuarded(req *http.Request) (*http.Response, error) {
	guardCtx, cancel := context.WithCancel(req.Context())
	resp, err := f.client.Do(req.WithContext(guardCtx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = newStallReader(resp.Body, f.stallWindow, cancel)
	return resp, nil
}

// checkRedirectSafety validates each redirect hop before it is followed.
// The initial URL is validated against the mirror allowlist by the proxy, but
// a malicious or compromised mirror could redirect to an internal address
// (SSRF). Loopback, private, link-local and metadata targets are refused;
// public cross-host redirects (e.g. PPA -> CDN) remain allowed.
func checkRedirectSafety(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("redirect to disallowed scheme %q refused", req.URL.Scheme)
	}
	if security.IsBlockedHost(req.URL.String()) {
		return fmt.Errorf("redirect to blocked host %q refused", req.URL.Hostname())
	}
	return nil
}

// Fetch downloads content from a URL
func (f *Fetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	data, err := retry.Do(ctx, retry.Config{
		MaxAttempts: f.maxRetries,
		Backoff:     retry.Exponential(time.Second),
	}, func() ([]byte, error) {
		resp, err := f.doStallGuarded(req)
		if err != nil {
			f.recordError(url)
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			if closeErr := resp.Body.Close(); closeErr != nil {
				f.logger.Debug("Failed to close response body", zap.Error(closeErr))
			}
			httpErr := fmt.Errorf("http %d: %s", resp.StatusCode, resp.Status)
			f.recordError(url)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				// Don't retry client errors
				return nil, retry.NonRetryable(httpErr)
			}
			return nil, httpErr
		}

		// Limit response size to prevent memory exhaustion attacks
		limitedReader := io.LimitReader(resp.Body, f.maxResponseSize+1)
		data, err := io.ReadAll(limitedReader)
		if closeErr := resp.Body.Close(); closeErr != nil {
			f.logger.Debug("Failed to close response body", zap.Error(closeErr))
		}
		if err != nil {
			f.recordError(url)
			return nil, err
		}

		// Check if we hit the size limit
		if int64(len(data)) > f.maxResponseSize {
			sizeErr := fmt.Errorf("response size exceeds maximum allowed (%d bytes)", f.maxResponseSize)
			f.recordError(url)
			return nil, retry.NonRetryable(sizeErr)
		}

		return data, nil
	})

	if err != nil {
		return nil, err
	}

	// Record success
	duration := time.Since(start)
	f.recordSuccess(url, int64(len(data)), duration)

	return data, nil
}

// FetchToWriter downloads content and writes to a writer.
// Unlike Fetch, this does NOT retry because the writer cannot be rewound.
// Callers that need retry should handle it themselves with a seekable writer.
func (f *Fetcher) FetchToWriter(ctx context.Context, url string, w io.Writer) (int64, error) {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	resp, err := f.doStallGuarded(req)
	if err != nil {
		f.recordError(url)
		return 0, err
	}

	if resp.StatusCode != http.StatusOK {
		if closeErr := resp.Body.Close(); closeErr != nil {
			f.logger.Debug("Failed to close response body", zap.Error(closeErr))
		}
		f.recordError(url)
		return 0, fmt.Errorf("http %d: %s", resp.StatusCode, resp.Status)
	}

	// Limit response size to prevent disk exhaustion
	limitedReader := io.LimitReader(resp.Body, f.maxResponseSize+1)
	written, err := io.Copy(w, limitedReader)
	if closeErr := resp.Body.Close(); closeErr != nil {
		f.logger.Debug("Failed to close response body", zap.Error(closeErr))
	}
	if err != nil {
		f.recordError(url)
		return 0, err
	}

	if written > f.maxResponseSize {
		f.recordError(url)
		return 0, fmt.Errorf("response size exceeds maximum allowed (%d bytes)", f.maxResponseSize)
	}

	duration := time.Since(start)
	f.recordSuccess(url, written, duration)
	return written, nil
}

// Stream returns a reader for streaming content
func (f *Fetcher) Stream(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	resp, err := f.doStallGuarded(req)
	if err != nil {
		f.recordError(url)
		return nil, 0, err
	}

	if resp.StatusCode != http.StatusOK {
		if closeErr := resp.Body.Close(); closeErr != nil {
			f.logger.Debug("Failed to close response body", zap.Error(closeErr))
		}
		f.recordError(url)
		return nil, 0, fmt.Errorf("http %d: %s", resp.StatusCode, resp.Status)
	}

	return resp.Body, resp.ContentLength, nil
}

// ConditionalResult carries the outcome of a conditional GET. On upstream 304
// Body is nil and NotModified is true; on 200 Body streams the content.
// LastModified and ETag are relayed so clients can revalidate next time.
type ConditionalResult struct {
	Body         io.ReadCloser
	Size         int64
	NotModified  bool
	LastModified string
	ETag         string
}

// StreamConditional performs a GET forwarding the given revalidation values
// (empty strings are omitted). It lets APT's If-Modified-Since/If-None-Match
// reach the mirror, so unchanged index and Release files cost a 304 instead of
// a full re-download on every apt-get update.
func (f *Fetcher) StreamConditional(ctx context.Context, url, ifModifiedSince, ifNoneMatch string) (*ConditionalResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	if ifModifiedSince != "" {
		req.Header.Set("If-Modified-Since", ifModifiedSince)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := f.doStallGuarded(req)
	if err != nil {
		f.recordError(url)
		return nil, err
	}

	result := &ConditionalResult{
		Size:         resp.ContentLength,
		LastModified: resp.Header.Get("Last-Modified"),
		ETag:         resp.Header.Get("ETag"),
	}

	switch resp.StatusCode {
	case http.StatusNotModified:
		if closeErr := resp.Body.Close(); closeErr != nil {
			f.logger.Debug("Failed to close response body", zap.Error(closeErr))
		}
		result.NotModified = true
		return result, nil
	case http.StatusOK:
		result.Body = resp.Body
		return result, nil
	default:
		if closeErr := resp.Body.Close(); closeErr != nil {
			f.logger.Debug("Failed to close response body", zap.Error(closeErr))
		}
		f.recordError(url)
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, resp.Status)
	}
}

// MaxResponseSize returns the configured response size cap, for callers that
// buffer a streamed body and need to enforce the same bound as Fetch.
func (f *Fetcher) MaxResponseSize() int64 {
	return f.maxResponseSize
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

// FetchRange downloads a specific byte range from a URL using HTTP Range headers.
// If start is 0 and end is -1, it fetches the entire content.
// The range is inclusive: bytes start-end (both included).
func (f *Fetcher) FetchRange(ctx context.Context, url string, rangeStart, rangeEnd int64) ([]byte, error) {
	// If fetching full file, use regular Fetch
	if rangeStart == 0 && rangeEnd < 0 {
		return f.Fetch(ctx, url)
	}

	startTime := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	// Set Range header (HTTP ranges are inclusive)
	if rangeEnd < 0 {
		// Open-ended range: from start to end of file
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", rangeStart))
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd))
	}

	data, err := retry.Do(ctx, retry.Config{
		MaxAttempts: f.maxRetries,
		Backoff:     retry.Exponential(time.Second),
	}, func() ([]byte, error) {
		resp, err := f.doStallGuarded(req)
		if err != nil {
			f.recordError(url)
			return nil, err
		}

		// Accept both 200 OK (server doesn't support range) and 206 Partial Content
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			if closeErr := resp.Body.Close(); closeErr != nil {
				f.logger.Debug("Failed to close response body", zap.Error(closeErr))
			}
			httpErr := fmt.Errorf("http %d: %s", resp.StatusCode, resp.Status)
			f.recordError(url)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return nil, retry.NonRetryable(httpErr)
			}
			return nil, httpErr
		}

		// If server returned 200 instead of 206, it doesn't support ranges
		// We need to read and discard bytes before start, then read until end
		if resp.StatusCode == http.StatusOK {
			data, handleErr := f.handleNonRangeResponse(resp, rangeStart, rangeEnd)
			if handleErr != nil {
				f.recordError(url)
				return nil, handleErr
			}
			return data, nil
		}

		// Server supports ranges - read the response
		expectedSize := rangeEnd - rangeStart + 1
		if rangeEnd < 0 {
			expectedSize = f.maxResponseSize // Use max as limit for open-ended ranges
		}

		limitedReader := io.LimitReader(resp.Body, expectedSize+1)
		data, err := io.ReadAll(limitedReader)
		if closeErr := resp.Body.Close(); closeErr != nil {
			f.logger.Debug("Failed to close response body", zap.Error(closeErr))
		}
		if err != nil {
			f.recordError(url)
			return nil, err
		}

		return data, nil
	})

	if err != nil {
		return nil, err
	}

	duration := time.Since(startTime)
	f.recordSuccess(url, int64(len(data)), duration)
	return data, nil
}

// handleNonRangeResponse handles the case where server doesn't support Range requests
func (f *Fetcher) handleNonRangeResponse(resp *http.Response, start, end int64) ([]byte, error) {
	defer resp.Body.Close()

	// Discard bytes before start
	if start > 0 {
		discarded, err := io.CopyN(io.Discard, resp.Body, start)
		if err != nil {
			return nil, fmt.Errorf("failed to skip %d bytes: %w", start, err)
		}
		if discarded < start {
			return nil, fmt.Errorf("file too short: wanted to skip %d bytes, only got %d", start, discarded)
		}
	}

	// Read the requested range
	var toRead int64
	if end < 0 {
		toRead = f.maxResponseSize
	} else {
		toRead = end - start + 1
	}

	limitedReader := io.LimitReader(resp.Body, toRead)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}

	return data, nil
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

	stats.AvgLatencyMs = stats.AvgLatencyMs*(n-1)/n + latencyMs/n

	// Guard against zero/near-zero duration which would produce +Inf throughput
	// and permanently poison the running average
	if duration > 0 {
		throughputBps := float64(bytes) / duration.Seconds()
		stats.AvgThroughputBps = stats.AvgThroughputBps*(n-1)/n + throughputBps/n
	}
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
