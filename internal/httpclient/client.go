// Package httpclient provides a factory for creating configured HTTP clients.
package httpclient

import (
	"net/http"
	"time"
)

// Default configuration values
const (
	DefaultTimeout             = 60 * time.Second
	DefaultMaxIdleConnsPerHost = 10
	DefaultIdleConnTimeout     = 90 * time.Second
)

// Config holds HTTP client configuration options.
type Config struct {
	// Timeout is the maximum time for the entire request (default: 60s).
	// Set to a negative value for NO whole-request timeout — callers that
	// legitimately transfer large bodies on slow links (e.g. the mirror
	// fetcher) must bound stalls per-read instead, because a blanket timeout
	// kills a healthy transfer mid-body once it simply takes long enough.
	Timeout time.Duration

	// ResponseHeaderTimeout bounds the wait for upstream response headers
	// (time to first byte) at the transport level. Zero means no bound.
	ResponseHeaderTimeout time.Duration

	// MaxIdleConnsPerHost controls the maximum idle connections per host (default: 10)
	MaxIdleConnsPerHost int

	// IdleConnTimeout is how long idle connections stay open (default: 90s)
	IdleConnTimeout time.Duration

	// CheckRedirect controls redirect-following policy. If nil, Go's default
	// policy applies (follow up to 10 redirects without validation).
	CheckRedirect func(req *http.Request, via []*http.Request) error
}

// New creates a new HTTP client with the given configuration.
// If cfg is nil, default values are used.
func New(cfg *Config) *http.Client {
	if cfg == nil {
		cfg = &Config{}
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	} else if timeout < 0 {
		timeout = 0 // negative sentinel: no whole-request timeout
	}

	maxIdleConns := cfg.MaxIdleConnsPerHost
	if maxIdleConns <= 0 {
		maxIdleConns = DefaultMaxIdleConnsPerHost
	}

	idleConnTimeout := cfg.IdleConnTimeout
	if idleConnTimeout <= 0 {
		idleConnTimeout = DefaultIdleConnTimeout
	}

	transport := &http.Transport{
		MaxIdleConnsPerHost:   maxIdleConns,
		IdleConnTimeout:       idleConnTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
	}

	return &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: cfg.CheckRedirect,
	}
}

// Default returns an HTTP client with default configuration.
func Default() *http.Client {
	return New(nil)
}

// WithTimeout creates a simple HTTP client with only a timeout configured.
// This is useful for simple use cases that don't need connection pooling.
func WithTimeout(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
	}
}
