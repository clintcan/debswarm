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

// UpdateRate changes the rate limit dynamically.
// bytesPerSecond of 0 or negative disables rate limiting.
func (l *Limiter) UpdateRate(bytesPerSecond int64) {
	if l == nil {
		return
	}
	if bytesPerSecond <= 0 {
		l.enabled = false
		return
	}

	burst := bytesPerSecond
	if burst < 64*1024 {
		burst = 64 * 1024
	}
	if burst > 4*1024*1024 {
		burst = 4 * 1024 * 1024
	}

	if l.limiter == nil {
		l.limiter = rate.NewLimiter(rate.Limit(bytesPerSecond), int(burst))
	} else {
		l.limiter.SetLimit(rate.Limit(bytesPerSecond))
		l.limiter.SetBurst(int(burst))
	}
	l.enabled = true
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

// Read implements io.Reader with rate limiting.
// Splits large reads into burst-sized waits to avoid panicking when n > burst.
func (lr *LimitedReader) Read(p []byte) (n int, err error) {
	n, err = lr.r.Read(p)
	if n > 0 {
		// Split into burst-sized waits to avoid WaitN panic when n > burst
		burst := lr.limiter.Burst()
		remaining := n
		for remaining > 0 {
			wait := remaining
			if wait > burst {
				wait = burst
			}
			if waitErr := lr.limiter.WaitN(lr.ctx, wait); waitErr != nil {
				return n, waitErr
			}
			remaining -= wait
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

// Write implements io.Writer with rate limiting.
// Splits large writes into burst-sized chunks to avoid WaitN panic when len(p) > burst.
func (lw *LimitedWriter) Write(p []byte) (n int, err error) {
	burst := lw.limiter.Burst()
	for n < len(p) {
		// Determine chunk size (at most burst)
		end := n + burst
		if end > len(p) {
			end = len(p)
		}
		chunk := p[n:end]

		// Wait for permission before writing this chunk
		if err := lw.limiter.WaitN(lw.ctx, len(chunk)); err != nil {
			return n, err
		}
		written, err := lw.w.Write(chunk)
		n += written
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
