// Package retry provides configurable retry logic with backoff strategies.
package retry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// NonRetryableError wraps an error to indicate it should not be retried.
type NonRetryableError struct {
	Err error
}

func (e *NonRetryableError) Error() string {
	return e.Err.Error()
}

func (e *NonRetryableError) Unwrap() error {
	return e.Err
}

// NonRetryable wraps an error to indicate it should not be retried.
func NonRetryable(err error) error {
	if err == nil {
		return nil
	}
	return &NonRetryableError{Err: err}
}

// Config controls retry behavior.
type Config struct {
	// MaxAttempts is the maximum number of attempts (not retries).
	// Must be at least 1.
	MaxAttempts int

	// Backoff returns the delay before the nth attempt (0-indexed).
	// If nil, defaults to Exponential(time.Second).
	Backoff func(attempt int) time.Duration
}

// Exponential returns a backoff function that waits attempt² * base.
// For example with base=1s: 0s, 1s, 4s, 9s, 16s...
func Exponential(base time.Duration) func(int) time.Duration {
	return func(attempt int) time.Duration {
		return time.Duration(attempt*attempt) * base
	}
}

// Linear returns a backoff function that waits attempt * base.
// For example with base=1s: 0s, 1s, 2s, 3s, 4s...
func Linear(base time.Duration) func(int) time.Duration {
	return func(attempt int) time.Duration {
		return time.Duration(attempt) * base
	}
}

// Constant returns a backoff function that always waits d.
// The first attempt (attempt=0) still has no delay.
func Constant(d time.Duration) func(int) time.Duration {
	return func(attempt int) time.Duration {
		if attempt == 0 {
			return 0
		}
		return d
	}
}

// Do executes fn until it succeeds or max attempts are exhausted.
// It waits between attempts according to the backoff strategy.
// If context is cancelled during backoff, it returns ctx.Err().
func Do[T any](ctx context.Context, cfg Config, fn func() (T, error)) (T, error) {
	var zero T

	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}

	backoff := cfg.Backoff
	if backoff == nil {
		backoff = Exponential(time.Second)
	}

	var lastErr error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		// Wait before retry (no wait on first attempt)
		if attempt > 0 {
			delay := backoff(attempt)
			if delay > 0 {
				select {
				case <-ctx.Done():
					return zero, ctx.Err()
				case <-time.After(delay):
				}
			}
		}

		result, err := fn()
		if err == nil {
			return result, nil
		}

		// Check if error is non-retryable
		var nonRetryable *NonRetryableError
		if errors.As(err, &nonRetryable) {
			return zero, nonRetryable.Err
		}

		lastErr = err
	}

	return zero, fmt.Errorf("failed after %d attempts: %w", cfg.MaxAttempts, lastErr)
}
