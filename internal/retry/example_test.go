package retry_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/debswarm/debswarm/internal/retry"
)

func ExampleDo() {
	ctx := context.Background()

	// Simulate an operation that fails twice then succeeds
	attempts := 0
	result, err := retry.Do(ctx, retry.Config{
		MaxAttempts: 3,
		Backoff:     retry.Constant(10 * time.Millisecond),
	}, func() (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("temporary error")
		}
		return "success", nil
	})

	fmt.Printf("Result: %s, Attempts: %d, Error: %v\n", result, attempts, err)
	// Output: Result: success, Attempts: 3, Error: <nil>
}

func ExampleDo_withExponentialBackoff() {
	ctx := context.Background()

	// Use exponential backoff: 0s, 100ms, 400ms, 900ms...
	_, _ = retry.Do(ctx, retry.Config{
		MaxAttempts: 5,
		Backoff:     retry.Exponential(100 * time.Millisecond),
	}, func() ([]byte, error) {
		// Your operation here
		return []byte("data"), nil
	})
}

func ExampleDo_withLinearBackoff() {
	ctx := context.Background()

	// Use linear backoff: 0s, 500ms, 1s, 1.5s...
	_, _ = retry.Do(ctx, retry.Config{
		MaxAttempts: 4,
		Backoff:     retry.Linear(500 * time.Millisecond),
	}, func() (int, error) {
		// Your operation here
		return 42, nil
	})
}

func ExampleNonRetryable() {
	ctx := context.Background()

	// Mark certain errors as non-retryable
	attempts := 0
	_, err := retry.Do(ctx, retry.Config{
		MaxAttempts: 5,
	}, func() (string, error) {
		attempts++
		// Don't retry on permanent errors
		return "", retry.NonRetryable(errors.New("permanent error"))
	})

	fmt.Printf("Attempts: %d, Error: %v\n", attempts, err)
	// Output: Attempts: 1, Error: permanent error
}

func ExampleExponential() {
	backoff := retry.Exponential(time.Second)

	// Demonstrates the exponential pattern: attempt² * base
	fmt.Printf("Attempt 0: %v\n", backoff(0))
	fmt.Printf("Attempt 1: %v\n", backoff(1))
	fmt.Printf("Attempt 2: %v\n", backoff(2))
	fmt.Printf("Attempt 3: %v\n", backoff(3))
	// Output:
	// Attempt 0: 0s
	// Attempt 1: 1s
	// Attempt 2: 4s
	// Attempt 3: 9s
}

func ExampleLinear() {
	backoff := retry.Linear(time.Second)

	// Demonstrates the linear pattern: attempt * base
	fmt.Printf("Attempt 0: %v\n", backoff(0))
	fmt.Printf("Attempt 1: %v\n", backoff(1))
	fmt.Printf("Attempt 2: %v\n", backoff(2))
	fmt.Printf("Attempt 3: %v\n", backoff(3))
	// Output:
	// Attempt 0: 0s
	// Attempt 1: 1s
	// Attempt 2: 2s
	// Attempt 3: 3s
}

func ExampleConstant() {
	backoff := retry.Constant(5 * time.Second)

	// Demonstrates constant delay (except first attempt)
	fmt.Printf("Attempt 0: %v\n", backoff(0))
	fmt.Printf("Attempt 1: %v\n", backoff(1))
	fmt.Printf("Attempt 2: %v\n", backoff(2))
	// Output:
	// Attempt 0: 0s
	// Attempt 1: 5s
	// Attempt 2: 5s
}
