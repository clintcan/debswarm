// Package lifecycle provides utilities for managing goroutine lifecycles.
package lifecycle

import (
	"context"
	"sync"
	"time"
)

// Manager coordinates the lifecycle of background goroutines.
// It provides a context that is cancelled on Stop, and tracks
// goroutines via a WaitGroup for graceful shutdown.
type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new lifecycle Manager with a cancellable context
// derived from the provided parent context.
func New(parent context.Context) *Manager {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Manager{
		ctx:    ctx,
		cancel: cancel,
	}
}

// Context returns the manager's context, which is cancelled when Stop is called.
func (m *Manager) Context() context.Context {
	return m.ctx
}

// Go starts a goroutine that is tracked by the manager.
// The function receives the manager's context and should exit when ctx.Done() is signaled.
func (m *Manager) Go(fn func(ctx context.Context)) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		fn(m.ctx)
	}()
}

// GoN starts n goroutines, each receiving a unique worker ID (0 to n-1).
func (m *Manager) GoN(n int, fn func(ctx context.Context, id int)) {
	for i := 0; i < n; i++ {
		id := i
		m.Go(func(ctx context.Context) {
			fn(ctx, id)
		})
	}
}

// RunTicker starts a goroutine that executes fn on each tick.
// The goroutine exits when the manager's context is cancelled.
func (m *Manager) RunTicker(interval time.Duration, fn func()) {
	m.Go(func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fn()
			}
		}
	})
}

// Stop cancels the context and waits for all tracked goroutines to finish.
func (m *Manager) Stop() {
	m.cancel()
	m.wg.Wait()
}

// StopWithTimeout cancels the context and waits for goroutines to finish
// up to the specified timeout. Returns context.DeadlineExceeded if timeout is reached.
func (m *Manager) StopWithTimeout(timeout time.Duration) error {
	m.cancel()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

// Done returns a channel that is closed when the manager's context is cancelled.
// This is a convenience method equivalent to m.Context().Done().
func (m *Manager) Done() <-chan struct{} {
	return m.ctx.Done()
}

// Err returns the context's error after it has been cancelled.
// This is a convenience method equivalent to m.Context().Err().
func (m *Manager) Err() error {
	return m.ctx.Err()
}
