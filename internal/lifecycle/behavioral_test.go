package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Behavioral tests to verify lifecycle manager matches original patterns

// TestBehavior_MatchesOriginalPattern verifies the lifecycle manager
// behaves identically to the original ctx/cancel/wg pattern
func TestBehavior_MatchesOriginalPattern(t *testing.T) {
	// Original pattern:
	// ctx, cancel := context.WithCancel(context.Background())
	// wg.Add(1)
	// go func() { defer wg.Done(); <-ctx.Done() }()
	// cancel()
	// wg.Wait()

	m := New(nil)

	var started, stopped int32
	m.Go(func(ctx context.Context) {
		atomic.StoreInt32(&started, 1)
		<-ctx.Done()
		atomic.StoreInt32(&stopped, 1)
	})

	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&started) != 1 {
		t.Fatal("goroutine should have started")
	}

	m.Stop()

	if atomic.LoadInt32(&stopped) != 1 {
		t.Fatal("goroutine should have stopped")
	}
}

// TestBehavior_TickerMatchesOriginal verifies RunTicker behaves like
// the original ticker loop pattern
func TestBehavior_TickerMatchesOriginal(t *testing.T) {
	// Original pattern:
	// ticker := time.NewTicker(interval)
	// defer ticker.Stop()
	// for {
	//     select {
	//     case <-ctx.Done():
	//         return
	//     case <-ticker.C:
	//         fn()
	//     }
	// }

	m := New(nil)

	var tickCount int32
	interval := 20 * time.Millisecond

	m.RunTicker(interval, func() {
		atomic.AddInt32(&tickCount, 1)
	})

	// Wait for approximately 3 ticks
	time.Sleep(70 * time.Millisecond)
	m.Stop()

	count := atomic.LoadInt32(&tickCount)
	if count < 2 || count > 4 {
		t.Errorf("expected 2-4 ticks, got %d", count)
	}
}

// TestBehavior_MultipleTickersIndependent verifies multiple tickers
// run independently (like cleanupLoop and adaptiveLoop)
func TestBehavior_MultipleTickersIndependent(t *testing.T) {
	m := New(nil)

	var count1, count2 int32

	// Faster ticker (like cleanup)
	m.RunTicker(15*time.Millisecond, func() {
		atomic.AddInt32(&count1, 1)
	})

	// Slower ticker (like adaptive)
	m.RunTicker(30*time.Millisecond, func() {
		atomic.AddInt32(&count2, 1)
	})

	time.Sleep(100 * time.Millisecond)
	m.Stop()

	c1 := atomic.LoadInt32(&count1)
	c2 := atomic.LoadInt32(&count2)

	// Faster ticker should have more ticks
	if c1 <= c2 {
		t.Errorf("faster ticker should tick more: count1=%d, count2=%d", c1, c2)
	}
	if c1 < 4 {
		t.Errorf("ticker1 should have ~6 ticks, got %d", c1)
	}
	if c2 < 2 {
		t.Errorf("ticker2 should have ~3 ticks, got %d", c2)
	}
}

// TestBehavior_StopWaitsForAllGoroutines verifies Stop blocks until
// all goroutines complete (like wg.Wait())
func TestBehavior_StopWaitsForAllGoroutines(t *testing.T) {
	m := New(nil)

	var completed int32
	numGoroutines := 5

	for i := 0; i < numGoroutines; i++ {
		m.Go(func(ctx context.Context) {
			<-ctx.Done()
			time.Sleep(30 * time.Millisecond) // Simulate cleanup
			atomic.AddInt32(&completed, 1)
		})
	}

	time.Sleep(10 * time.Millisecond) // Let goroutines start

	start := time.Now()
	m.Stop()
	elapsed := time.Since(start)

	if atomic.LoadInt32(&completed) != int32(numGoroutines) {
		t.Errorf("not all goroutines completed: %d/%d", completed, numGoroutines)
	}
	if elapsed < 25*time.Millisecond {
		t.Errorf("Stop should have waited for cleanup, elapsed: %v", elapsed)
	}
}

// TestBehavior_ContextCancellationOrder verifies context is cancelled
// before waiting (like cancel() before wg.Wait())
func TestBehavior_ContextCancellationOrder(t *testing.T) {
	m := New(nil)

	var contextCancelledFirst int32

	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		// If context is cancelled first, this should run
		atomic.StoreInt32(&contextCancelledFirst, 1)
	})

	time.Sleep(10 * time.Millisecond)
	m.Stop()

	if atomic.LoadInt32(&contextCancelledFirst) != 1 {
		t.Error("context should be cancelled before goroutine completes")
	}
}

// TestBehavior_ConcurrentAccess verifies thread-safe operations
func TestBehavior_ConcurrentAccess(t *testing.T) {
	m := New(nil)

	var wg sync.WaitGroup
	var started int32

	// Start multiple goroutines concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Go(func(ctx context.Context) {
				atomic.AddInt32(&started, 1)
				<-ctx.Done()
			})
		}()
	}

	wg.Wait()
	time.Sleep(20 * time.Millisecond)

	if atomic.LoadInt32(&started) != 10 {
		t.Errorf("expected 10 goroutines started, got %d", started)
	}

	m.Stop()
}

// TestBehavior_StopIdempotent verifies Stop can be called multiple times
func TestBehavior_StopIdempotent(t *testing.T) {
	m := New(nil)

	m.Go(func(ctx context.Context) {
		<-ctx.Done()
	})

	time.Sleep(10 * time.Millisecond)

	// Call Stop multiple times - should not panic
	m.Stop()
	m.Stop()
	m.Stop()
}

// TestBehavior_NoGoroutines verifies Stop works with no goroutines started
func TestBehavior_NoGoroutines(t *testing.T) {
	m := New(nil)

	// Stop without starting any goroutines
	start := time.Now()
	m.Stop()
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("Stop should be instant with no goroutines, took %v", elapsed)
	}
}

// TestBehavior_TickerStopsCleanly verifies ticker goroutines stop properly
func TestBehavior_TickerStopsCleanly(t *testing.T) {
	m := New(nil)

	var tickCount int32
	m.RunTicker(10*time.Millisecond, func() {
		atomic.AddInt32(&tickCount, 1)
	})

	time.Sleep(50 * time.Millisecond)
	m.Stop()

	// Record count after Stop completes
	countAtStop := atomic.LoadInt32(&tickCount)

	// Wait and verify no more ticks after Stop
	time.Sleep(50 * time.Millisecond)
	countAfterWait := atomic.LoadInt32(&tickCount)

	if countAfterWait != countAtStop {
		t.Errorf("ticker should stop after Stop(): at_stop=%d, after_wait=%d", countAtStop, countAfterWait)
	}
}

// TestBehavior_ContextDoneBeforeTick verifies ticker doesn't tick after Stop
func TestBehavior_ContextDoneBeforeTick(t *testing.T) {
	m := New(nil)

	var tickCount int32
	m.RunTicker(100*time.Millisecond, func() {
		atomic.AddInt32(&tickCount, 1)
	})

	// Stop before first tick
	time.Sleep(10 * time.Millisecond)
	m.Stop()

	if atomic.LoadInt32(&tickCount) != 0 {
		t.Error("ticker should not have ticked before Stop")
	}
}

// TestBehavior_GoAfterStop verifies behavior when Go is called after Stop
func TestBehavior_GoAfterStop(t *testing.T) {
	m := New(nil)
	m.Stop()

	var executed int32
	m.Go(func(ctx context.Context) {
		// Context is already cancelled, so this should return immediately
		select {
		case <-ctx.Done():
			atomic.StoreInt32(&executed, 1)
		default:
			// Should not reach here
		}
	})

	time.Sleep(20 * time.Millisecond)

	// The goroutine should still run, but context is cancelled
	if atomic.LoadInt32(&executed) != 1 {
		t.Error("goroutine should execute even after Stop, with cancelled context")
	}
}

// TestBehavior_RunTickerAfterStop verifies ticker after Stop
func TestBehavior_RunTickerAfterStop(t *testing.T) {
	m := New(nil)
	m.Stop()

	var tickCount int32
	m.RunTicker(10*time.Millisecond, func() {
		atomic.AddInt32(&tickCount, 1)
	})

	time.Sleep(50 * time.Millisecond)

	// Ticker should not tick because context is cancelled
	if atomic.LoadInt32(&tickCount) != 0 {
		t.Errorf("ticker should not tick after Stop, got %d ticks", tickCount)
	}
}

// TestBehavior_ParentContextCancellation verifies child reacts to parent cancel
func TestBehavior_ParentContextCancellation(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	m := New(parent)

	var stopped int32
	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		atomic.StoreInt32(&stopped, 1)
	})

	time.Sleep(10 * time.Millisecond)

	// Cancel parent instead of calling Stop
	parentCancel()

	time.Sleep(10 * time.Millisecond)

	if atomic.LoadInt32(&stopped) != 1 {
		t.Error("child should stop when parent is cancelled")
	}

	// Clean up
	m.Stop()
}

// TestBehavior_StopWithTimeoutSuccess verifies timeout with fast cleanup
func TestBehavior_StopWithTimeoutSuccess(t *testing.T) {
	m := New(nil)

	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)
	})

	time.Sleep(5 * time.Millisecond)

	err := m.StopWithTimeout(100 * time.Millisecond)
	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
}

// TestBehavior_StopWithTimeoutFailure verifies timeout with slow cleanup
func TestBehavior_StopWithTimeoutFailure(t *testing.T) {
	m := New(nil)

	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(500 * time.Millisecond) // Very slow cleanup
	})

	time.Sleep(5 * time.Millisecond)

	start := time.Now()
	err := m.StopWithTimeout(50 * time.Millisecond)
	elapsed := time.Since(start)

	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	if elapsed < 40*time.Millisecond || elapsed > 100*time.Millisecond {
		t.Errorf("should timeout around 50ms, got %v", elapsed)
	}
}
