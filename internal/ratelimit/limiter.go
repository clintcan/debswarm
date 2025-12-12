// Package ratelimit provides rate-limited io.Reader/Writer wrappers
package ratelimit

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

// Limiter provides rate-limited readers and writers
type Limiter struct {
	limiter *rate.Limiter
	enabled bool
}

// New creates a new rate limiter.
// bytesPerSecond of 0 or negative means unlimited.
func New(bytesPerSecond int64) *Limiter {
	if bytesPerSecond <= 0 {
		return &Limiter{enabled: false}
	}

	// Use burst size of 64KB or 1 second worth, whichever is larger
	burst := bytesPerSecond
	if burst < 64*1024 {
		burst = 64 * 1024
	}
	if burst > 4*1024*1024 {
		burst = 4 * 1024 * 1024 // Cap at 4MB burst
	}

	return &Limiter{
		limiter: rate.NewLimiter(rate.Limit(bytesPerSecond), int(burst)),
		enabled: true,
	}
}

// Enabled returns whether rate limiting is active
func (l *Limiter) Enabled() bool {
	return l != nil && l.enabled
}

// Reader returns a rate-limited reader
func (l *Limiter) Reader(r io.Reader) io.Reader {
	if !l.Enabled() {
		return r
	}
	return &LimitedReader{
		r:       r,
		limiter: l.limiter,
		ctx:     context.Background(),
	}
}

// ReaderContext returns a rate-limited reader with context
func (l *Limiter) ReaderContext(ctx context.Context, r io.Reader) io.Reader {
	if !l.Enabled() {
		return r
	}
	return &LimitedReader{
		r:       r,
		limiter: l.limiter,
		ctx:     ctx,
	}
}

// Writer returns a rate-limited writer
func (l *Limiter) Writer(w io.Writer) io.Writer {
	if !l.Enabled() {
		return w
	}
	return &LimitedWriter{
		w:       w,
		limiter: l.limiter,
		ctx:     context.Background(),
	}
}

// WriterContext returns a rate-limited writer with context
func (l *Limiter) WriterContext(ctx context.Context, w io.Writer) io.Writer {
	if !l.Enabled() {
		return w
	}
	return &LimitedWriter{
		w:       w,
		limiter: l.limiter,
		ctx:     ctx,
	}
}

// LimitedReader wraps io.Reader with rate limiting
type LimitedReader struct {
	r       io.Reader
	limiter *rate.Limiter
	ctx     context.Context
}

// Read implements io.Reader with rate limiting
func (lr *LimitedReader) Read(p []byte) (n int, err error) {
	n, err = lr.r.Read(p)
	if n > 0 {
		// Wait for permission to have read n bytes
		if waitErr := lr.limiter.WaitN(lr.ctx, n); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

// LimitedWriter wraps io.Writer with rate limiting
type LimitedWriter struct {
	w       io.Writer
	limiter *rate.Limiter
	ctx     context.Context
}

// Write implements io.Writer with rate limiting
func (lw *LimitedWriter) Write(p []byte) (n int, err error) {
	// Wait for permission before writing
	if err := lw.limiter.WaitN(lw.ctx, len(p)); err != nil {
		return 0, err
	}
	return lw.w.Write(p)
}
