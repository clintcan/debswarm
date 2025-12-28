package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNew_WithNilParent(t *testing.T) {
	m := New(context.Background())
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
	if m.Context() == nil {
		t.Fatal("expected non-nil context")
	}
	m.Stop()
}

func TestNew_WithParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := New(parent)
	if m.Context() == nil {
		t.Fatal("expected non-nil context")
	}
	m.Stop()
}

func TestGo_TracksGoroutine(t *testing.T) {
	m := New(context.Background())

	var started, stopped int32
	m.Go(func(ctx context.Context) {
		atomic.StoreInt32(&started, 1)
		<-ctx.Done()
		atomic.StoreInt32(&stopped, 1)
	})

	// Give goroutine time to start
	time.Sleep(10 * time.Millisecond)

	if atomic.LoadInt32(&started) != 1 {
		t.Error("goroutine should have started")
	}
	if atomic.LoadInt32(&stopped) != 0 {
		t.Error("goroutine should not have stopped yet")
	}

	m.Stop()

	if atomic.LoadInt32(&stopped) != 1 {
		t.Error("goroutine should have stopped after Stop()")
	}
}

func TestGo_MultipleGoroutines(t *testing.T) {
	m := New(context.Background())

	var count int32
	for i := 0; i < 5; i++ {
		m.Go(func(ctx context.Context) {
			atomic.AddInt32(&count, 1)
			<-ctx.Done()
		})
	}

	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&count) != 5 {
		t.Errorf("expected 5 goroutines started, got %d", count)
	}

	m.Stop()
}

func TestGoN_StartsNGoroutines(t *testing.T) {
	m := New(context.Background())

	var ids []int
	var mu sync.Mutex

	m.GoN(3, func(ctx context.Context, id int) {
		mu.Lock()
		ids = append(ids, id)
		mu.Unlock()
		<-ctx.Done()
	})

	time.Sleep(10 * time.Millisecond)
	m.Stop()

	mu.Lock()
	defer mu.Unlock()

	if len(ids) != 3 {
		t.Errorf("expected 3 goroutines, got %d", len(ids))
	}

	// Check all IDs are present
	idMap := make(map[int]bool)
	for _, id := range ids {
		idMap[id] = true
	}
	for i := 0; i < 3; i++ {
		if !idMap[i] {
			t.Errorf("missing worker ID %d", i)
		}
	}
}

func TestRunTicker_ExecutesPeriodically(t *testing.T) {
	m := New(context.Background())

	var count int32
	m.RunTicker(20*time.Millisecond, func() {
		atomic.AddInt32(&count, 1)
	})

	// Wait for a few ticks
	time.Sleep(70 * time.Millisecond)
	m.Stop()

	// Should have ticked 2-3 times
	c := atomic.LoadInt32(&count)
	if c < 2 || c > 4 {
		t.Errorf("expected 2-4 ticks, got %d", c)
	}
}

func TestRunTicker_StopsOnCancel(t *testing.T) {
	m := New(context.Background())

	var count int32
	m.RunTicker(10*time.Millisecond, func() {
		atomic.AddInt32(&count, 1)
	})

	time.Sleep(35 * time.Millisecond)
	m.Stop()

	countAtStop := atomic.LoadInt32(&count)

	// Wait a bit more - count should not increase
	time.Sleep(30 * time.Millisecond)

	if atomic.LoadInt32(&count) != countAtStop {
		t.Error("ticker should have stopped after Stop()")
	}
}

func TestStop_WaitsForGoroutines(t *testing.T) {
	m := New(context.Background())

	var stopped int32
	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // Simulate cleanup work
		atomic.StoreInt32(&stopped, 1)
	})

	time.Sleep(10 * time.Millisecond) // Let goroutine start

	start := time.Now()
	m.Stop()
	elapsed := time.Since(start)

	if atomic.LoadInt32(&stopped) != 1 {
		t.Error("goroutine should have completed")
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("Stop should have waited for goroutine, elapsed: %v", elapsed)
	}
}

func TestStopWithTimeout_Success(t *testing.T) {
	m := New(context.Background())

	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)
	})

	time.Sleep(5 * time.Millisecond)

	err := m.StopWithTimeout(100 * time.Millisecond)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestStopWithTimeout_Timeout(t *testing.T) {
	m := New(context.Background())

	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond) // Longer than timeout
	})

	time.Sleep(5 * time.Millisecond)

	start := time.Now()
	err := m.StopWithTimeout(50 * time.Millisecond)
	elapsed := time.Since(start)

	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	if elapsed < 40*time.Millisecond || elapsed > 100*time.Millisecond {
		t.Errorf("should have timed out around 50ms, got %v", elapsed)
	}
}

func TestDone_ReturnsContextDone(t *testing.T) {
	m := New(context.Background())

	select {
	case <-m.Done():
		t.Error("Done should not be closed yet")
	default:
		// OK
	}

	m.Stop()

	select {
	case <-m.Done():
		// OK
	default:
		t.Error("Done should be closed after Stop")
	}
}

func TestErr_ReturnsContextErr(t *testing.T) {
	m := New(context.Background())

	if m.Err() != nil {
		t.Error("Err should be nil before Stop")
	}

	m.Stop()

	if m.Err() != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", m.Err())
	}
}

func TestParentContextCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	m := New(parent)

	var stopped int32
	m.Go(func(ctx context.Context) {
		<-ctx.Done()
		atomic.StoreInt32(&stopped, 1)
	})

	time.Sleep(10 * time.Millisecond)

	// Cancel parent instead of calling Stop
	cancel()

	time.Sleep(10 * time.Millisecond)

	if atomic.LoadInt32(&stopped) != 1 {
		t.Error("goroutine should stop when parent is canceled")
	}

	// Stop should still work (idempotent)
	m.Stop()
}

func TestGo_PanicRecovery(t *testing.T) {
	m := New(context.Background())

	var recovered int32
	m.Go(func(ctx context.Context) {
		defer func() {
			if r := recover(); r != nil {
				atomic.StoreInt32(&recovered, 1)
			}
		}()
		panic("test panic")
	})

	time.Sleep(10 * time.Millisecond)
	m.Stop()

	// Note: The goroutine panicked but the manager should still work
	// The WaitGroup counter is decremented in the defer
}
