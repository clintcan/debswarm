package verify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// mockProviderFinder is a mock implementation of ProviderFinder for testing
type mockProviderFinder struct {
	mu        sync.Mutex
	providers map[string][]peer.AddrInfo
	id        peer.ID
	err       error
	delay     time.Duration
}

func newMockProviderFinder(id peer.ID) *mockProviderFinder {
	return &mockProviderFinder{
		providers: make(map[string][]peer.AddrInfo),
		id:        id,
	}
}

func (m *mockProviderFinder) FindProviders(ctx context.Context, sha256Hash string, limit int) ([]peer.AddrInfo, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return nil, m.err
	}

	providers := m.providers[sha256Hash]
	if len(providers) > limit {
		return providers[:limit], nil
	}
	return providers, nil
}

func (m *mockProviderFinder) ID() peer.ID {
	return m.id
}

func (m *mockProviderFinder) setProviders(hash string, providers []peer.AddrInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[hash] = providers
}

func (m *mockProviderFinder) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func TestNew(t *testing.T) {
	logger := zap.NewNop()
	finder := newMockProviderFinder("12D3KooWTest")

	v := New(nil, finder, logger, nil, nil)
	defer v.Close()

	// Check defaults applied
	if v.config.MaxConcurrent != 4 {
		t.Errorf("Expected MaxConcurrent=4, got %d", v.config.MaxConcurrent)
	}
	if v.config.MinProviders != 2 {
		t.Errorf("Expected MinProviders=2, got %d", v.config.MinProviders)
	}
	if v.config.QueryTimeout != 10*time.Second {
		t.Errorf("Expected QueryTimeout=10s, got %v", v.config.QueryTimeout)
	}
}

func TestNew_CustomConfig(t *testing.T) {
	logger := zap.NewNop()
	finder := newMockProviderFinder("12D3KooWTest")

	cfg := &Config{
		Enabled:       true,
		MinProviders:  3,
		QueryTimeout:  5 * time.Second,
		MaxConcurrent: 2,
		QueryLimit:    10,
	}

	v := New(cfg, finder, logger, nil, nil)
	defer v.Close()

	if v.config.MinProviders != 3 {
		t.Errorf("Expected MinProviders=3, got %d", v.config.MinProviders)
	}
	if v.config.MaxConcurrent != 2 {
		t.Errorf("Expected MaxConcurrent=2, got %d", v.config.MaxConcurrent)
	}
}

func TestVerify_MultipleProviders(t *testing.T) {
	logger := zap.NewNop()
	ourID := peer.ID("12D3KooWOurself")
	finder := newMockProviderFinder(ourID)

	// Set up multiple providers (including ourselves)
	hash := "abc123def456"
	finder.setProviders(hash, []peer.AddrInfo{
		{ID: ourID},
		{ID: peer.ID("12D3KooWPeer1")},
		{ID: peer.ID("12D3KooWPeer2")},
	})

	v := New(nil, finder, logger, nil, nil)
	defer v.Close()

	result := v.verify(hash)

	if result.Error != nil {
		t.Fatalf("Unexpected error: %v", result.Error)
	}
	if result.ProviderCount != 2 {
		t.Errorf("Expected 2 other providers, got %d", result.ProviderCount)
	}
	if !result.Verified {
		t.Error("Expected Verified=true")
	}
	if result.SelfOnly {
		t.Error("Expected SelfOnly=false")
	}
}

func TestVerify_SelfOnly(t *testing.T) {
	logger := zap.NewNop()
	ourID := peer.ID("12D3KooWOurself")
	finder := newMockProviderFinder(ourID)

	// Only ourselves as provider
	hash := "abc123def456"
	finder.setProviders(hash, []peer.AddrInfo{
		{ID: ourID},
	})

	v := New(nil, finder, logger, nil, nil)
	defer v.Close()

	result := v.verify(hash)

	if result.Error != nil {
		t.Fatalf("Unexpected error: %v", result.Error)
	}
	if result.ProviderCount != 0 {
		t.Errorf("Expected 0 other providers, got %d", result.ProviderCount)
	}
	if result.Verified {
		t.Error("Expected Verified=false")
	}
	if !result.SelfOnly {
		t.Error("Expected SelfOnly=true")
	}
}

func TestVerify_PartialVerification(t *testing.T) {
	logger := zap.NewNop()
	ourID := peer.ID("12D3KooWOurself")
	finder := newMockProviderFinder(ourID)

	// Only one other provider (less than min_providers=2)
	hash := "abc123def456"
	finder.setProviders(hash, []peer.AddrInfo{
		{ID: ourID},
		{ID: peer.ID("12D3KooWPeer1")},
	})

	v := New(nil, finder, logger, nil, nil)
	defer v.Close()

	result := v.verify(hash)

	if result.Error != nil {
		t.Fatalf("Unexpected error: %v", result.Error)
	}
	if result.ProviderCount != 1 {
		t.Errorf("Expected 1 other provider, got %d", result.ProviderCount)
	}
	if result.Verified {
		t.Error("Expected Verified=false (partial)")
	}
	if result.SelfOnly {
		t.Error("Expected SelfOnly=false")
	}
}

func TestVerify_Error(t *testing.T) {
	logger := zap.NewNop()
	finder := newMockProviderFinder("12D3KooWTest")
	finder.setError(errors.New("network error"))

	v := New(nil, finder, logger, nil, nil)
	defer v.Close()

	result := v.verify("abc123")

	if result.Error == nil {
		t.Fatal("Expected error")
	}
	if result.Error.Error() != "network error" {
		t.Errorf("Unexpected error: %v", result.Error)
	}
}

func TestVerifyAsync_Disabled(t *testing.T) {
	logger := zap.NewNop()
	finder := newMockProviderFinder("12D3KooWTest")

	cfg := &Config{
		Enabled: false,
	}

	v := New(cfg, finder, logger, nil, nil)
	defer v.Close()

	// Should return immediately without doing anything
	v.VerifyAsync("abc123", "test.deb")

	// Give it a moment
	time.Sleep(10 * time.Millisecond)

	// Close should be fast since nothing is pending
	start := time.Now()
	v.Close()
	if time.Since(start) > 100*time.Millisecond {
		t.Error("Close took too long, verification should not have been started")
	}
}

func TestVerifyAsync_NilFinder(t *testing.T) {
	logger := zap.NewNop()

	v := New(nil, nil, logger, nil, nil)
	defer v.Close()

	// Should return immediately without panicking
	v.VerifyAsync("abc123", "test.deb")
}

func TestVerifyAsync_Concurrent(t *testing.T) {
	logger := zap.NewNop()
	ourID := peer.ID("12D3KooWOurself")
	finder := newMockProviderFinder(ourID)

	// Add a small delay to simulate network latency
	finder.delay = 10 * time.Millisecond

	hash := "abc123def456"
	finder.setProviders(hash, []peer.AddrInfo{
		{ID: ourID},
		{ID: peer.ID("12D3KooWPeer1")},
		{ID: peer.ID("12D3KooWPeer2")},
	})

	cfg := &Config{
		Enabled:       true,
		MinProviders:  2,
		QueryTimeout:  5 * time.Second,
		MaxConcurrent: 2,
		QueryLimit:    5,
	}

	v := New(cfg, finder, logger, nil, nil)

	// Start multiple concurrent verifications
	for i := 0; i < 5; i++ {
		v.VerifyAsync(hash, "test.deb")
	}

	// Close waits for all pending verifications
	v.Close()
}

func TestVerifyAsync_QueueFull(t *testing.T) {
	logger := zap.NewNop()
	ourID := peer.ID("12D3KooWOurself")
	finder := newMockProviderFinder(ourID)

	// Add significant delay
	finder.delay = 100 * time.Millisecond

	cfg := &Config{
		Enabled:       true,
		MinProviders:  2,
		QueryTimeout:  5 * time.Second,
		MaxConcurrent: 1, // Only allow 1 concurrent
		QueryLimit:    5,
	}

	v := New(cfg, finder, logger, nil, nil)

	// Start first verification (will occupy the single slot)
	v.VerifyAsync("hash1", "test1.deb")

	// This should be skipped (queue full)
	v.VerifyAsync("hash2", "test2.deb")

	v.Close()
}

func TestClose_WaitsForPending(t *testing.T) {
	logger := zap.NewNop()
	ourID := peer.ID("12D3KooWOurself")

	// Use channels to coordinate
	started := make(chan struct{})
	canFinish := make(chan struct{})

	// Custom finder that blocks until told to finish
	customFinder := &blockingFinder{
		id:        ourID,
		started:   started,
		canFinish: canFinish,
		providers: map[string][]peer.AddrInfo{
			"hash1": {{ID: ourID}},
		},
	}

	v := New(nil, customFinder, logger, nil, nil)

	// Start verification
	v.VerifyAsync("hash1", "test.deb")

	// Wait for verification to start
	select {
	case <-started:
		// Good, verification started
	case <-time.After(time.Second):
		t.Fatal("Verification did not start in time")
	}

	// Start Close in goroutine - it should block until verification finishes
	closeDone := make(chan struct{})
	go func() {
		v.Close()
		close(closeDone)
	}()

	// Verify Close is still waiting
	select {
	case <-closeDone:
		t.Fatal("Close returned before verification finished")
	case <-time.After(50 * time.Millisecond):
		// Good, Close is still waiting
	}

	// Allow verification to finish
	close(canFinish)

	// Now Close should complete
	select {
	case <-closeDone:
		// Good
	case <-time.After(time.Second):
		t.Fatal("Close did not return after verification finished")
	}
}

// blockingFinder blocks until explicitly told to finish (ignores context cancellation for blocking)
type blockingFinder struct {
	id        peer.ID
	started   chan struct{}
	canFinish chan struct{}
	providers map[string][]peer.AddrInfo
	once      sync.Once
}

func (s *blockingFinder) FindProviders(_ context.Context, sha256Hash string, limit int) ([]peer.AddrInfo, error) {
	s.once.Do(func() {
		close(s.started)
	})

	// Block until canFinish is closed (ignoring context for test purposes)
	<-s.canFinish

	providers := s.providers[sha256Hash]
	if len(providers) > limit {
		return providers[:limit], nil
	}
	return providers, nil
}

func (s *blockingFinder) ID() peer.ID {
	return s.id
}

func TestTruncateHash(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"abc123", "abc123"},
		{"1234567890123456", "1234567890123456"},
		{"12345678901234567", "1234567890123456..."},
		{"abcdef1234567890abcdef", "abcdef1234567890..."},
	}

	for _, tc := range tests {
		got := truncateHash(tc.input)
		if got != tc.expected {
			t.Errorf("truncateHash(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.Enabled {
		t.Error("Expected Enabled=true")
	}
	if cfg.MinProviders != 2 {
		t.Errorf("Expected MinProviders=2, got %d", cfg.MinProviders)
	}
	if cfg.QueryTimeout != 10*time.Second {
		t.Errorf("Expected QueryTimeout=10s, got %v", cfg.QueryTimeout)
	}
	if cfg.MaxConcurrent != 4 {
		t.Errorf("Expected MaxConcurrent=4, got %d", cfg.MaxConcurrent)
	}
	if cfg.QueryLimit != 5 {
		t.Errorf("Expected QueryLimit=5, got %d", cfg.QueryLimit)
	}
}
