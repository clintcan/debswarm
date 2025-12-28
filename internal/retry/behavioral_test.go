package retry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestBehavioralEquivalence verifies the retry library behaves like the original
// manual retry loops in mirror/fetcher.go

func TestBackoffTiming_MatchesOriginal(t *testing.T) {
	// Original code: time.Duration(attempt*attempt) * time.Second
	// This test verifies Exponential(time.Second) produces the same values

	backoff := Exponential(time.Second)

	// Original loop: for attempt := 0; attempt < maxRetries; attempt++
	// Backoff only applied when attempt > 0
	expectedDelays := []time.Duration{
		0,               // attempt 0: no delay (first attempt)
		1 * time.Second, // attempt 1: 1*1 = 1 second
		4 * time.Second, // attempt 2: 2*2 = 4 seconds
		9 * time.Second, // attempt 3: 3*3 = 9 seconds
	}

	for attempt, expected := range expectedDelays {
		got := backoff(attempt)
		if got != expected {
			t.Errorf("attempt %d: expected %v, got %v", attempt, expected, got)
		}
	}
}

func TestErrorRecordingOnEachAttempt(t *testing.T) {
	// Original behavior: recordError called on each failed attempt
	// Verify our fn is called the correct number of times

	var attemptCount int32
	maxAttempts := 3

	_, err := Do(context.Background(), Config{
		MaxAttempts: maxAttempts,
		Backoff:     Constant(time.Millisecond), // Fast for testing
	}, func() (string, error) {
		atomic.AddInt32(&attemptCount, 1)
		return "", errors.New("simulated error")
	})

	if err == nil {
		t.Fatal("expected error")
	}

	if atomic.LoadInt32(&attemptCount) != int32(maxAttempts) {
		t.Errorf("expected %d attempts, got %d", maxAttempts, attemptCount)
	}
}

func TestNonRetryableStopsImmediately(t *testing.T) {
	// Original behavior: 4xx errors return immediately without retry
	// if resp.StatusCode >= 400 && resp.StatusCode < 500 {
	//     return nil, lastErr  // No retry
	// }

	var attemptCount int32
	clientErr := errors.New("HTTP 404: Not Found")

	_, err := Do(context.Background(), Config{
		MaxAttempts: 5,
		Backoff:     Constant(time.Millisecond),
	}, func() (string, error) {
		atomic.AddInt32(&attemptCount, 1)
		// Simulate 4xx error - should not retry
		return "", NonRetryable(clientErr)
	})

	if !errors.Is(err, clientErr) {
		t.Errorf("expected clientErr, got %v", err)
	}

	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Errorf("NonRetryable should stop after 1 attempt, got %d", attemptCount)
	}
}

func TestServerErrorRetries(t *testing.T) {
	// Original behavior: 5xx errors trigger retry
	// if resp.StatusCode >= 400 && resp.StatusCode < 500 {
	//     return nil, lastErr  // This is 4xx, no retry
	// }
	// continue  // This is 5xx, retry

	var attemptCount int32
	serverErr := errors.New("HTTP 500: Internal Server Error")

	_, err := Do(context.Background(), Config{
		MaxAttempts: 3,
		Backoff:     Constant(time.Millisecond),
	}, func() (string, error) {
		atomic.AddInt32(&attemptCount, 1)
		// 5xx error - should retry (not wrapped with NonRetryable)
		return "", serverErr
	})

	if err == nil {
		t.Fatal("expected error")
	}

	// Should have tried all 3 attempts
	if atomic.LoadInt32(&attemptCount) != 3 {
		t.Errorf("expected 3 attempts for server error, got %d", attemptCount)
	}
}

func TestContextCancellationDuringBackoff(t *testing.T) {
	// Original behavior:
	// select {
	// case <-ctx.Done():
	//     return nil, ctx.Err()
	// case <-time.After(backoff):
	// }

	ctx, cancel := context.WithCancel(context.Background())
	var attemptCount int32

	// Cancel after first attempt, during backoff wait
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := Do(ctx, Config{
		MaxAttempts: 5,
		Backoff:     Constant(1 * time.Second), // Long enough to be canceled
	}, func() (string, error) {
		atomic.AddInt32(&attemptCount, 1)
		return "", errors.New("error")
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Errorf("expected 1 attempt before cancellation, got %d", attemptCount)
	}
}

func TestSuccessOnFirstAttempt_NoBackoff(t *testing.T) {
	// Original behavior: no backoff delay on first attempt
	start := time.Now()

	result, err := Do(context.Background(), Config{
		MaxAttempts: 3,
		Backoff:     Exponential(time.Second),
	}, func() (string, error) {
		return "success", nil
	})

	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result != "success" {
		t.Errorf("expected 'success', got %q", result)
	}
	// Should complete nearly instantly (no backoff on first attempt)
	if elapsed > 100*time.Millisecond {
		t.Errorf("first attempt should be instant, took %v", elapsed)
	}
}

func TestSuccessAfterRetry_CorrectBackoff(t *testing.T) {
	// Verify backoff is applied between attempts
	var attemptCount int32
	start := time.Now()

	result, err := Do(context.Background(), Config{
		MaxAttempts: 3,
		Backoff:     Constant(100 * time.Millisecond),
	}, func() (string, error) {
		count := atomic.AddInt32(&attemptCount, 1)
		if count < 2 {
			return "", errors.New("temporary error")
		}
		return "success", nil
	})

	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result != "success" {
		t.Errorf("expected 'success', got %q", result)
	}

	// Should have waited ~100ms for the backoff between attempt 1 and 2
	if elapsed < 90*time.Millisecond {
		t.Errorf("expected ~100ms backoff, got %v", elapsed)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("backoff took too long: %v", elapsed)
	}
}

func TestFinalErrorWrapping(t *testing.T) {
	// Original: fmt.Errorf("failed after %d retries: %w", f.maxRetries, lastErr)
	// New:      fmt.Errorf("failed after %d attempts: %w", cfg.MaxAttempts, lastErr)

	originalErr := errors.New("original error")

	_, err := Do(context.Background(), Config{
		MaxAttempts: 2,
		Backoff:     Constant(time.Millisecond),
	}, func() (string, error) {
		return "", originalErr
	})

	// Verify error wrapping preserves original error
	if !errors.Is(err, originalErr) {
		t.Errorf("error should wrap original: %v", err)
	}

	// Verify message format
	expected := "failed after 2 attempts: original error"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestNonRetryablePreservesOriginalError(t *testing.T) {
	// When NonRetryable is returned, the original error should be unwrapped
	originalErr := errors.New("client error")

	_, err := Do(context.Background(), Config{
		MaxAttempts: 5,
		Backoff:     Constant(time.Millisecond),
	}, func() (string, error) {
		return "", NonRetryable(originalErr)
	})

	// Should get the original error, not wrapped
	if err != originalErr {
		t.Errorf("expected original error directly, got %v", err)
	}
}

func TestZeroMaxAttempts_DefaultsToOne(t *testing.T) {
	var attemptCount int32

	_, err := Do(context.Background(), Config{
		MaxAttempts: 0, // Invalid, should default to 1
	}, func() (string, error) {
		atomic.AddInt32(&attemptCount, 1)
		return "", errors.New("error")
	})

	if err == nil {
		t.Fatal("expected error")
	}

	if atomic.LoadInt32(&attemptCount) != 1 {
		t.Errorf("MaxAttempts=0 should default to 1, got %d attempts", attemptCount)
	}
}

func TestNilBackoff_DefaultsToExponential(t *testing.T) {
	var attemptCount int32
	start := time.Now()

	_, err := Do(context.Background(), Config{
		MaxAttempts: 2,
		Backoff:     nil, // Should default to Exponential(time.Second)
	}, func() (string, error) {
		atomic.AddInt32(&attemptCount, 1)
		return "", errors.New("error")
	})

	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}

	// With default Exponential(time.Second), delay between attempt 0 and 1 is 1*1=1 second
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected ~1s delay with default backoff, got %v", elapsed)
	}
}

func TestGenericTypes(t *testing.T) {
	// Test that generics work with different types

	// int
	intResult, err := Do(context.Background(), Config{MaxAttempts: 1}, func() (int, error) {
		return 42, nil
	})
	if err != nil || intResult != 42 {
		t.Errorf("int: expected 42, got %d, err=%v", intResult, err)
	}

	// struct
	type result struct {
		Value string
	}
	structResult, err := Do(context.Background(), Config{MaxAttempts: 1}, func() (result, error) {
		return result{Value: "test"}, nil
	})
	if err != nil || structResult.Value != "test" {
		t.Errorf("struct: expected test, got %s, err=%v", structResult.Value, err)
	}

	// slice
	sliceResult, err := Do(context.Background(), Config{MaxAttempts: 1}, func() ([]byte, error) {
		return []byte("data"), nil
	})
	if err != nil || string(sliceResult) != "data" {
		t.Errorf("slice: expected data, got %s, err=%v", sliceResult, err)
	}
}
