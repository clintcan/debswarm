package lifecycle_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/debswarm/debswarm/internal/lifecycle"
)

func ExampleManager() {
	// Create a manager with a background context
	m := lifecycle.New(context.Background())

	var counter int32

	// Start a background goroutine
	m.Go(func(ctx context.Context) {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				atomic.AddInt32(&counter, 1)
				time.Sleep(10 * time.Millisecond)
			}
		}
	})

	// Let it run for a bit
	time.Sleep(50 * time.Millisecond)

	// Stop waits for all goroutines to finish
	m.Stop()

	fmt.Printf("Counter > 0: %v\n", atomic.LoadInt32(&counter) > 0)
	// Output: Counter > 0: true
}

func ExampleManager_Go() {
	m := lifecycle.New(context.Background())

	var finished int32

	// Go starts a tracked goroutine
	m.Go(func(ctx context.Context) {
		<-ctx.Done() // Wait for cancellation
		atomic.StoreInt32(&finished, 1)
	})

	// Stop cancels context and waits for goroutine to finish
	m.Stop()

	fmt.Printf("Goroutine finished: %v\n", atomic.LoadInt32(&finished) == 1)
	// Output: Goroutine finished: true
}

func ExampleManager_GoN() {
	m := lifecycle.New(context.Background())

	var started int32

	// Start 5 worker goroutines
	m.GoN(5, func(ctx context.Context, id int) {
		atomic.AddInt32(&started, 1)
		<-ctx.Done()
	})

	time.Sleep(10 * time.Millisecond)
	m.Stop()

	fmt.Printf("Started workers: %d\n", atomic.LoadInt32(&started))
	// Output: Started workers: 5
}

func ExampleManager_RunTicker() {
	m := lifecycle.New(context.Background())

	var ticks int32

	// Run a periodic task every 20ms
	m.RunTicker(20*time.Millisecond, func() {
		atomic.AddInt32(&ticks, 1)
	})

	// Let it tick a few times
	time.Sleep(70 * time.Millisecond)
	m.Stop()

	// Should have ticked 2-4 times
	fmt.Printf("Ticked: %v\n", atomic.LoadInt32(&ticks) >= 2)
	// Output: Ticked: true
}

func ExampleManager_StopWithTimeout() {
	m := lifecycle.New(context.Background())

	// Start a goroutine that takes time to cleanup
	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond) // Simulate cleanup
	})

	// Stop with a generous timeout
	err := m.StopWithTimeout(100 * time.Millisecond)
	fmt.Printf("Stopped cleanly: %v\n", err == nil)
	// Output: Stopped cleanly: true
}

func ExampleNew_withParentContext() {
	// Create a parent context with cancellation
	parent, cancel := context.WithCancel(context.Background())

	m := lifecycle.New(parent)

	var stopped int32
	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		atomic.StoreInt32(&stopped, 1)
	})

	// Canceling parent also cancels the manager's context
	cancel()
	time.Sleep(10 * time.Millisecond)

	fmt.Printf("Stopped: %v\n", atomic.LoadInt32(&stopped) == 1)
	// Output: Stopped: true
}
