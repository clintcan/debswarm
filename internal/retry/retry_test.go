package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDo_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	result, err := Do(context.Background(), Config{MaxAttempts: 3}, func() (string, error) {
		calls++
		return "success", nil
	})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result != "success" {
		t.Errorf("expected 'success', got %q", result)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestDo_SuccessAfterRetries(t *testing.T) {
	calls := 0
	result, err := Do(context.Background(), Config{
		MaxAttempts: 5,
		Backoff:     Constant(time.Millisecond), // Fast backoff for testing
	}, func() (int, error) {
		calls++
		if calls < 3 {
			return 0, errors.New("temporary error")
		}
		return 42, nil
	})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result != 42 {
		t.Errorf("expected 42, got %d", result)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestDo_MaxAttemptsExhausted(t *testing.T) {
	calls := 0
	testErr := errors.New("persistent error")

	_, err := Do(context.Background(), Config{
		MaxAttempts: 3,
		Backoff:     Constant(time.Millisecond),
	}, func() (string, error) {
		calls++
		return "", testErr
	})

	if err == nil {
		t.Error("expected error, got nil")
	}
	if !errors.Is(err, testErr) {
		t.Errorf("expected error to wrap testErr, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestDo_ContextCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	// Cancel context after first attempt
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := Do(ctx, Config{
		MaxAttempts: 5,
		Backoff:     Constant(100 * time.Millisecond), // Long backoff to ensure cancellation
	}, func() (string, error) {
		calls++
		return "", errors.New("error")
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call before cancellation, got %d", calls)
	}
}

func TestDo_ContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	calls := 0
	_, err := Do(ctx, Config{
		MaxAttempts: 3,
		Backoff:     Constant(time.Millisecond),
	}, func() (string, error) {
		calls++
		return "", errors.New("error")
	})

	// First attempt runs, then context check fails during backoff
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestExponentialBackoff(t *testing.T) {
	backoff := Exponential(time.Second)

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 0},
		{1, time.Second},
		{2, 4 * time.Second},
		{3, 9 * time.Second},
		{4, 16 * time.Second},
	}

	for _, tc := range tests {
		got := backoff(tc.attempt)
		if got != tc.expected {
			t.Errorf("Exponential(1s)(%d) = %v, want %v", tc.attempt, got, tc.expected)
		}
	}
}

func TestLinearBackoff(t *testing.T) {
	backoff := Linear(2 * time.Second)

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 0},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 6 * time.Second},
	}

	for _, tc := range tests {
		got := backoff(tc.attempt)
		if got != tc.expected {
			t.Errorf("Linear(2s)(%d) = %v, want %v", tc.attempt, got, tc.expected)
		}
	}
}

func TestConstantBackoff(t *testing.T) {
	backoff := Constant(5 * time.Second)

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 0}, // No delay on first attempt
		{1, 5 * time.Second},
		{2, 5 * time.Second},
		{3, 5 * time.Second},
	}

	for _, tc := range tests {
		got := backoff(tc.attempt)
		if got != tc.expected {
			t.Errorf("Constant(5s)(%d) = %v, want %v", tc.attempt, got, tc.expected)
		}
	}
}

func TestDo_DefaultBackoff(t *testing.T) {
	// When Backoff is nil, should use Exponential(time.Second)
	calls := 0
	start := time.Now()

	_, err := Do(context.Background(), Config{
		MaxAttempts: 2,
		// Backoff is nil - should default to Exponential(time.Second)
	}, func() (string, error) {
		calls++
		return "", errors.New("error")
	})

	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected error")
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
	// With exponential backoff, delay between attempt 0 and 1 is 1² = 1 second
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected ~1s delay, got %v", elapsed)
	}
}

func TestDo_ZeroMaxAttempts(t *testing.T) {
	// MaxAttempts < 1 should be treated as 1
	calls := 0
	_, err := Do(context.Background(), Config{
		MaxAttempts: 0,
	}, func() (string, error) {
		calls++
		return "", errors.New("error")
	})

	if calls != 1 {
		t.Errorf("expected 1 call when MaxAttempts=0, got %d", calls)
	}
	if err == nil {
		t.Error("expected error")
	}
}

func TestDo_VoidFunction(t *testing.T) {
	// Test using struct{} for void functions
	calls := 0
	_, err := Do(context.Background(), Config{MaxAttempts: 1}, func() (struct{}, error) {
		calls++
		return struct{}{}, nil
	})

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestDo_NonRetryableError(t *testing.T) {
	calls := 0
	originalErr := errors.New("client error")

	_, err := Do(context.Background(), Config{
		MaxAttempts: 5,
		Backoff:     Constant(time.Millisecond),
	}, func() (string, error) {
		calls++
		// Return non-retryable error on second attempt
		if calls == 2 {
			return "", NonRetryable(originalErr)
		}
		return "", errors.New("temporary error")
	})

	if !errors.Is(err, originalErr) {
		t.Errorf("expected original error, got %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (should stop on non-retryable), got %d", calls)
	}
}

func TestNonRetryable_Nil(t *testing.T) {
	err := NonRetryable(nil)
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestNonRetryableError_Unwrap(t *testing.T) {
	originalErr := errors.New("original error")
	wrapped := NonRetryable(originalErr)

	if !errors.Is(wrapped, originalErr) {
		t.Error("Unwrap should return original error")
	}
}
