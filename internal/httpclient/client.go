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
	// Timeout is the maximum time for the entire request (default: 60s)
	Timeout time.Duration

	// MaxIdleConnsPerHost controls the maximum idle connections per host (default: 10)
	MaxIdleConnsPerHost int

	// IdleConnTimeout is how long idle connections stay open (default: 90s)
	IdleConnTimeout time.Duration
}

// New creates a new HTTP client with the given configuration.
// If cfg is nil, default values are used.
func New(cfg *Config) *http.Client {
	if cfg == nil {
		cfg = &Config{}
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
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
		MaxIdleConnsPerHost: maxIdleConns,
		IdleConnTimeout:     idleConnTimeout,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
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
