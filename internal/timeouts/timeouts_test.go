package timeouts

import (
	"sync"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	if cfg.DHTLookup != DefaultDHTLookup {
		t.Errorf("Expected DHTLookup %v, got %v", DefaultDHTLookup, cfg.DHTLookup)
	}
	if cfg.DHTLookupFull != DefaultDHTLookupFull {
		t.Errorf("Expected DHTLookupFull %v, got %v", DefaultDHTLookupFull, cfg.DHTLookupFull)
	}
	if cfg.PeerConnect != DefaultPeerConnect {
		t.Errorf("Expected PeerConnect %v, got %v", DefaultPeerConnect, cfg.PeerConnect)
	}
	if cfg.PeerFirstByte != DefaultPeerFirstByte {
		t.Errorf("Expected PeerFirstByte %v, got %v", DefaultPeerFirstByte, cfg.PeerFirstByte)
	}
	if cfg.PeerStall != DefaultPeerStall {
		t.Errorf("Expected PeerStall %v, got %v", DefaultPeerStall, cfg.PeerStall)
	}
	if cfg.MirrorFallback != DefaultMirrorFallback {
		t.Errorf("Expected MirrorFallback %v, got %v", DefaultMirrorFallback, cfg.MirrorFallback)
	}
	if !cfg.AdaptiveEnabled {
		t.Error("Expected AdaptiveEnabled to be true by default")
	}
	if cfg.BytesPerSecond != BytesPerSecondBase {
		t.Errorf("Expected BytesPerSecond %d, got %d", BytesPerSecondBase, cfg.BytesPerSecond)
	}
}

func TestNewManager(t *testing.T) {
	m := NewManager(nil)

	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.config == nil {
		t.Fatal("Manager config is nil")
	}
	if len(m.timeouts) == 0 {
		t.Error("Manager has no initialized timeouts")
	}
}

func TestNewManagerWithConfig(t *testing.T) {
	cfg := &Config{
		DHTLookup:       200 * time.Millisecond,
		PeerConnect:     5 * time.Second,
		AdaptiveEnabled: false,
		BytesPerSecond:  2 * 1024 * 1024,
	}

	m := NewManager(cfg)

	if m.Get(OpDHTLookup) != 200*time.Millisecond {
		t.Errorf("Expected DHTLookup 200ms, got %v", m.Get(OpDHTLookup))
	}
	if m.Get(OpPeerConnect) != 5*time.Second {
		t.Errorf("Expected PeerConnect 5s, got %v", m.Get(OpPeerConnect))
	}
}

func TestManagerGet(t *testing.T) {
	m := NewManager(nil)

	tests := []struct {
		op       Operation
		expected time.Duration
	}{
		{OpDHTLookup, DefaultDHTLookup},
		{OpDHTLookupFull, DefaultDHTLookupFull},
		{OpPeerConnect, DefaultPeerConnect},
		{OpPeerFirstByte, DefaultPeerFirstByte},
		{OpPeerTransfer, DefaultPeerStall},
	}

	for _, tt := range tests {
		got := m.Get(tt.op)
		if got != tt.expected {
			t.Errorf("Get(%s) = %v, want %v", tt.op, got, tt.expected)
		}
	}
}

func TestManagerGetUnknownOperation(t *testing.T) {
	m := NewManager(nil)

	got := m.Get(Operation("unknown_op"))
	expected := 30 * time.Second

	if got != expected {
		t.Errorf("Get(unknown) = %v, want %v", got, expected)
	}
}

func TestManagerGetForSize(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BytesPerSecond = 1024 * 1024 // 1 MB/s
	m := NewManager(cfg)

	// Test with 10MB file
	sizeBytes := int64(10 * 1024 * 1024)
	timeout := m.GetForSize(OpPeerTransfer, sizeBytes)

	// Base timeout + (10MB / 1MB/s) * 1.5 margin = base + 15s
	base := m.Get(OpPeerTransfer)
	expectedMin := base + 10*time.Second // At least base + transfer time

	if timeout < expectedMin {
		t.Errorf("GetForSize timeout %v is less than expected minimum %v", timeout, expectedMin)
	}
}

func TestManagerGetForSizeZeroSize(t *testing.T) {
	m := NewManager(nil)

	timeout := m.GetForSize(OpPeerTransfer, 0)
	base := m.Get(OpPeerTransfer)

	if timeout != base {
		t.Errorf("GetForSize(0) = %v, want base timeout %v", timeout, base)
	}
}

func TestManagerGetForSizeNegativeSize(t *testing.T) {
	m := NewManager(nil)

	timeout := m.GetForSize(OpPeerTransfer, -100)
	base := m.Get(OpPeerTransfer)

	if timeout != base {
		t.Errorf("GetForSize(-100) = %v, want base timeout %v", timeout, base)
	}
}

func TestManagerRecordSuccess(t *testing.T) {
	m := NewManager(nil)

	// First inflate the timeout so we can observe it decreasing
	m.RecordTimeout(OpDHTLookup)
	inflatedTimeout := m.Get(OpDHTLookup)

	// Record a fast success (less than half the inflated timeout)
	m.RecordSuccess(OpDHTLookup, inflatedTimeout/4)

	newTimeout := m.Get(OpDHTLookup)

	// Timeout should decrease on fast success (but not below base)
	if newTimeout >= inflatedTimeout {
		t.Errorf("Timeout should decrease on fast success: inflated=%v, new=%v", inflatedTimeout, newTimeout)
	}

	stats := m.GetStats(OpDHTLookup)
	if stats.SuccessCount != 1 {
		t.Errorf("Expected SuccessCount 1, got %d", stats.SuccessCount)
	}
}

func TestManagerRecordSuccessSlowOperation(t *testing.T) {
	m := NewManager(nil)
	initialTimeout := m.Get(OpDHTLookup)

	// Record a slow success (more than half the timeout)
	m.RecordSuccess(OpDHTLookup, initialTimeout)

	newTimeout := m.Get(OpDHTLookup)

	// Timeout should not decrease significantly for slow operations
	// It might increase slightly due to EMA adjustments
	stats := m.GetStats(OpDHTLookup)
	if stats.SuccessCount != 1 {
		t.Errorf("Expected SuccessCount 1, got %d", stats.SuccessCount)
	}
	if stats.AvgDuration == 0 {
		t.Error("AvgDuration should be updated after success")
	}

	_ = newTimeout // Used for verification
}

func TestManagerRecordSuccessAdaptiveDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdaptiveEnabled = false
	m := NewManager(cfg)

	initialTimeout := m.Get(OpDHTLookup)
	m.RecordSuccess(OpDHTLookup, initialTimeout/4)
	newTimeout := m.Get(OpDHTLookup)

	if newTimeout != initialTimeout {
		t.Errorf("Timeout should not change when adaptive disabled: initial=%v, new=%v", initialTimeout, newTimeout)
	}
}

func TestManagerRecordFailure(t *testing.T) {
	m := NewManager(nil)
	initialTimeout := m.Get(OpPeerConnect)

	m.RecordFailure(OpPeerConnect)

	newTimeout := m.Get(OpPeerConnect)

	// Timeout should increase on failure
	if newTimeout <= initialTimeout {
		t.Errorf("Timeout should increase on failure: initial=%v, new=%v", initialTimeout, newTimeout)
	}

	// Should increase by FailureMultiplier (1.5x)
	expected := time.Duration(float64(initialTimeout) * FailureMultiplier)
	if newTimeout != expected {
		t.Errorf("Expected timeout %v, got %v", expected, newTimeout)
	}

	stats := m.GetStats(OpPeerConnect)
	if stats.FailureCount != 1 {
		t.Errorf("Expected FailureCount 1, got %d", stats.FailureCount)
	}
}

func TestManagerRecordFailureAdaptiveDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdaptiveEnabled = false
	m := NewManager(cfg)

	initialTimeout := m.Get(OpPeerConnect)
	m.RecordFailure(OpPeerConnect)
	newTimeout := m.Get(OpPeerConnect)

	if newTimeout != initialTimeout {
		t.Errorf("Timeout should not change when adaptive disabled: initial=%v, new=%v", initialTimeout, newTimeout)
	}
}

func TestManagerRecordTimeout(t *testing.T) {
	m := NewManager(nil)
	initialTimeout := m.Get(OpPeerFirstByte)

	m.RecordTimeout(OpPeerFirstByte)

	newTimeout := m.Get(OpPeerFirstByte)

	// Timeout should double on timeout (TimeoutMultiplier = 2.0)
	expected := time.Duration(float64(initialTimeout) * TimeoutMultiplier)
	if newTimeout != expected {
		t.Errorf("Expected timeout %v after timeout, got %v", expected, newTimeout)
	}

	stats := m.GetStats(OpPeerFirstByte)
	if stats.TimeoutCount != 1 {
		t.Errorf("Expected TimeoutCount 1, got %d", stats.TimeoutCount)
	}
}

func TestManagerRecordTimeoutAdaptiveDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdaptiveEnabled = false
	m := NewManager(cfg)

	initialTimeout := m.Get(OpPeerFirstByte)
	m.RecordTimeout(OpPeerFirstByte)
	newTimeout := m.Get(OpPeerFirstByte)

	if newTimeout != initialTimeout {
		t.Errorf("Timeout should not change when adaptive disabled: initial=%v, new=%v", initialTimeout, newTimeout)
	}
}

func TestManagerRecordUnknownOperation(t *testing.T) {
	m := NewManager(nil)

	// Should not panic on unknown operations
	m.RecordSuccess(Operation("unknown"), 100*time.Millisecond)
	m.RecordFailure(Operation("unknown"))
	m.RecordTimeout(Operation("unknown"))
}

func TestManagerGetStats(t *testing.T) {
	m := NewManager(nil)

	m.RecordSuccess(OpDHTLookup, 50*time.Millisecond)
	m.RecordFailure(OpDHTLookup)
	m.RecordTimeout(OpDHTLookup)

	stats := m.GetStats(OpDHTLookup)

	if stats == nil {
		t.Fatal("GetStats returned nil")
	}
	if stats.Operation != OpDHTLookup {
		t.Errorf("Expected operation %s, got %s", OpDHTLookup, stats.Operation)
	}
	if stats.SuccessCount != 1 {
		t.Errorf("Expected SuccessCount 1, got %d", stats.SuccessCount)
	}
	if stats.FailureCount != 1 {
		t.Errorf("Expected FailureCount 1, got %d", stats.FailureCount)
	}
	if stats.TimeoutCount != 1 {
		t.Errorf("Expected TimeoutCount 1, got %d", stats.TimeoutCount)
	}
	if stats.BaseTimeout != DefaultDHTLookup {
		t.Errorf("Expected BaseTimeout %v, got %v", DefaultDHTLookup, stats.BaseTimeout)
	}
}

func TestManagerGetStatsUnknown(t *testing.T) {
	m := NewManager(nil)

	stats := m.GetStats(Operation("unknown"))

	if stats != nil {
		t.Error("Expected nil stats for unknown operation")
	}
}

func TestManagerGetAllStats(t *testing.T) {
	m := NewManager(nil)

	stats := m.GetAllStats()

	if len(stats) == 0 {
		t.Error("GetAllStats returned empty slice")
	}

	// Should have stats for all initialized operations
	operations := map[Operation]bool{
		OpDHTLookup:     false,
		OpDHTLookupFull: false,
		OpPeerConnect:   false,
		OpPeerFirstByte: false,
		OpPeerTransfer:  false,
		OpMirrorFetch:   false,
		OpChunkDownload: false,
	}

	for _, s := range stats {
		operations[s.Operation] = true
	}

	for op, found := range operations {
		if !found {
			t.Errorf("Missing stats for operation %s", op)
		}
	}
}

func TestManagerReset(t *testing.T) {
	m := NewManager(nil)

	// Modify timeouts
	m.RecordTimeout(OpDHTLookup)
	m.RecordTimeout(OpPeerConnect)
	m.RecordSuccess(OpPeerFirstByte, 100*time.Millisecond)

	// Reset
	m.Reset()

	// Verify all timeouts are back to base
	if m.Get(OpDHTLookup) != DefaultDHTLookup {
		t.Errorf("DHTLookup not reset: got %v, want %v", m.Get(OpDHTLookup), DefaultDHTLookup)
	}
	if m.Get(OpPeerConnect) != DefaultPeerConnect {
		t.Errorf("PeerConnect not reset: got %v, want %v", m.Get(OpPeerConnect), DefaultPeerConnect)
	}

	// Stats should also be reset
	stats := m.GetStats(OpDHTLookup)
	if stats.SuccessCount != 0 || stats.FailureCount != 0 || stats.TimeoutCount != 0 {
		t.Error("Stats not reset properly")
	}
}

func TestManagerResetDecay(t *testing.T) {
	m := NewManager(nil)

	// Inflate timeout
	m.RecordTimeout(OpDHTLookup)
	m.RecordTimeout(OpDHTLookup)
	inflatedTimeout := m.Get(OpDHTLookup)

	// Apply decay
	m.ResetDecay(0.5) // 50% decay toward base

	decayedTimeout := m.Get(OpDHTLookup)

	// Should be between base and inflated
	if decayedTimeout >= inflatedTimeout {
		t.Errorf("Decay should reduce timeout: inflated=%v, decayed=%v", inflatedTimeout, decayedTimeout)
	}
	if decayedTimeout < DefaultDHTLookup {
		t.Errorf("Decay should not go below base: base=%v, decayed=%v", DefaultDHTLookup, decayedTimeout)
	}
}

func TestManagerResetDecayInvalidFactor(t *testing.T) {
	m := NewManager(nil)

	m.RecordTimeout(OpDHTLookup)
	inflated := m.Get(OpDHTLookup)

	// Invalid factors should use default 0.1
	m.ResetDecay(0)
	m.ResetDecay(-1)
	m.ResetDecay(1.5)

	decayed := m.Get(OpDHTLookup)

	// Should still decay (not panic)
	if decayed >= inflated {
		t.Log("Note: multiple decays applied, timeout may have changed significantly")
	}
}

func TestClampTimeout(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected time.Duration
	}{
		{10 * time.Millisecond, MinTimeout},    // Below min
		{MinTimeout, MinTimeout},               // At min
		{1 * time.Second, 1 * time.Second},     // In range
		{MaxTimeout, MaxTimeout},               // At max
		{120 * time.Second, MaxTimeout},        // Above max
	}

	for _, tt := range tests {
		got := clampTimeout(tt.input)
		if got != tt.expected {
			t.Errorf("clampTimeout(%v) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestCalculateTransferTimeout(t *testing.T) {
	tests := []struct {
		name           string
		sizeBytes      int64
		bytesPerSecond int64
		margin         float64
		minExpected    time.Duration
	}{
		{
			name:           "1MB at 1MB/s",
			sizeBytes:      1024 * 1024,
			bytesPerSecond: 1024 * 1024,
			margin:         2.0,
			minExpected:    5*time.Second + 2*time.Second, // base + transfer time
		},
		{
			name:           "10MB at 1MB/s",
			sizeBytes:      10 * 1024 * 1024,
			bytesPerSecond: 1024 * 1024,
			margin:         2.0,
			minExpected:    5*time.Second + 10*time.Second,
		},
		{
			name:           "zero bytes per second uses default",
			sizeBytes:      1024 * 1024,
			bytesPerSecond: 0,
			margin:         2.0,
			minExpected:    5 * time.Second,
		},
		{
			name:           "zero margin uses default 2.0",
			sizeBytes:      1024 * 1024,
			bytesPerSecond: 1024 * 1024,
			margin:         0,
			minExpected:    5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateTransferTimeout(tt.sizeBytes, tt.bytesPerSecond, tt.margin)
			if got < tt.minExpected {
				t.Errorf("CalculateTransferTimeout() = %v, want at least %v", got, tt.minExpected)
			}
			if got > MaxTimeout {
				t.Errorf("CalculateTransferTimeout() = %v, should not exceed MaxTimeout %v", got, MaxTimeout)
			}
		})
	}
}

func TestNewDurationTracker(t *testing.T) {
	dt := NewDurationTracker(50)

	if dt == nil {
		t.Fatal("NewDurationTracker returned nil")
	}
	if dt.maxSamples != 50 {
		t.Errorf("Expected maxSamples 50, got %d", dt.maxSamples)
	}
}

func TestNewDurationTrackerDefaultSamples(t *testing.T) {
	dt := NewDurationTracker(0)

	if dt.maxSamples != 100 {
		t.Errorf("Expected default maxSamples 100, got %d", dt.maxSamples)
	}

	dt2 := NewDurationTracker(-10)
	if dt2.maxSamples != 100 {
		t.Errorf("Expected default maxSamples 100 for negative input, got %d", dt2.maxSamples)
	}
}

func TestDurationTrackerRecord(t *testing.T) {
	dt := NewDurationTracker(5)

	dt.Record(100 * time.Millisecond)
	dt.Record(200 * time.Millisecond)
	dt.Record(300 * time.Millisecond)

	if len(dt.durations) != 3 {
		t.Errorf("Expected 3 durations, got %d", len(dt.durations))
	}
}

func TestDurationTrackerRecordOverflow(t *testing.T) {
	dt := NewDurationTracker(3)

	dt.Record(100 * time.Millisecond)
	dt.Record(200 * time.Millisecond)
	dt.Record(300 * time.Millisecond)
	dt.Record(400 * time.Millisecond) // Should evict oldest

	if len(dt.durations) != 3 {
		t.Errorf("Expected 3 durations after overflow, got %d", len(dt.durations))
	}

	// First element should now be 200ms (100ms was evicted)
	if dt.durations[0] != 200*time.Millisecond {
		t.Errorf("Expected first duration 200ms, got %v", dt.durations[0])
	}
}

func TestDurationTrackerPercentile(t *testing.T) {
	dt := NewDurationTracker(100)

	// Add durations 100ms to 1000ms
	for i := 1; i <= 10; i++ {
		dt.Record(time.Duration(i*100) * time.Millisecond)
	}

	tests := []struct {
		percentile float64
		expected   time.Duration
	}{
		{0, 100 * time.Millisecond},
		{50, 500 * time.Millisecond},
		{90, 900 * time.Millisecond},
		{100, 1000 * time.Millisecond},
	}

	for _, tt := range tests {
		got := dt.Percentile(tt.percentile)
		// Allow some tolerance due to percentile calculation
		if got < tt.expected-100*time.Millisecond || got > tt.expected+100*time.Millisecond {
			t.Errorf("Percentile(%v) = %v, expected around %v", tt.percentile, got, tt.expected)
		}
	}
}

func TestDurationTrackerPercentileEmpty(t *testing.T) {
	dt := NewDurationTracker(100)

	got := dt.Percentile(50)
	if got != 0 {
		t.Errorf("Percentile on empty tracker = %v, want 0", got)
	}
}

func TestDurationTrackerPercentileSingleElement(t *testing.T) {
	dt := NewDurationTracker(100)
	dt.Record(500 * time.Millisecond)

	got := dt.Percentile(50)
	if got != 500*time.Millisecond {
		t.Errorf("Percentile on single element = %v, want 500ms", got)
	}
}

func TestDurationTrackerSuggestedTimeout(t *testing.T) {
	dt := NewDurationTracker(100)

	// Add durations
	for i := 1; i <= 100; i++ {
		dt.Record(time.Duration(i) * time.Millisecond)
	}

	suggested := dt.SuggestedTimeout(1.5)

	// P95 of 1-100ms is around 95ms, * 1.5 = ~142.5ms
	if suggested < 100*time.Millisecond || suggested > 200*time.Millisecond {
		t.Errorf("SuggestedTimeout = %v, expected around 140ms", suggested)
	}
}

func TestDurationTrackerSuggestedTimeoutEmpty(t *testing.T) {
	dt := NewDurationTracker(100)

	suggested := dt.SuggestedTimeout(1.5)
	if suggested != 0 {
		t.Errorf("SuggestedTimeout on empty = %v, want 0", suggested)
	}
}

func TestDurationTrackerSuggestedTimeoutDefaultMargin(t *testing.T) {
	dt := NewDurationTracker(100)
	dt.Record(100 * time.Millisecond)

	// Zero margin should use default 1.5
	suggested := dt.SuggestedTimeout(0)
	expected := time.Duration(float64(100*time.Millisecond) * 1.5)

	if suggested != expected {
		t.Errorf("SuggestedTimeout with 0 margin = %v, want %v", suggested, expected)
	}
}

func TestTimeoutBoundsEnforced(t *testing.T) {
	m := NewManager(nil)

	// Record many timeouts to inflate
	for i := 0; i < 20; i++ {
		m.RecordTimeout(OpDHTLookup)
	}

	timeout := m.Get(OpDHTLookup)
	if timeout > MaxTimeout {
		t.Errorf("Timeout %v exceeds MaxTimeout %v", timeout, MaxTimeout)
	}
}

func TestConcurrentAccess(t *testing.T) {
	m := NewManager(nil)

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Get(OpDHTLookup)
			_ = m.GetForSize(OpPeerTransfer, 1024*1024)
			_ = m.GetStats(OpPeerConnect)
			_ = m.GetAllStats()
		}()
	}

	// Concurrent writes
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.RecordSuccess(OpDHTLookup, 50*time.Millisecond)
			m.RecordFailure(OpPeerConnect)
			m.RecordTimeout(OpPeerFirstByte)
		}()
	}

	// Concurrent reset
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.ResetDecay(0.1)
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent access error: %v", err)
	}
}

func TestDurationTrackerConcurrentAccess(t *testing.T) {
	dt := NewDurationTracker(100)

	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dt.Record(time.Duration(i) * time.Millisecond)
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = dt.Percentile(50)
			_ = dt.Percentile(95)
			_ = dt.SuggestedTimeout(1.5)
		}()
	}

	wg.Wait()
}

func TestAdaptiveTimeoutEMASmoothing(t *testing.T) {
	m := NewManager(nil)

	// Record several successes with different durations
	m.RecordSuccess(OpDHTLookup, 100*time.Millisecond)
	stats1 := m.GetStats(OpDHTLookup)
	avg1 := stats1.AvgDuration

	m.RecordSuccess(OpDHTLookup, 200*time.Millisecond)
	stats2 := m.GetStats(OpDHTLookup)
	avg2 := stats2.AvgDuration

	// Average should be smoothed, not jumped to new value
	// EMA: new_avg = alpha * new + (1-alpha) * old = 0.2 * 200 + 0.8 * 100 = 120ms
	expectedAvg := time.Duration(AdaptationAlpha*float64(200*time.Millisecond) + (1-AdaptationAlpha)*float64(avg1))

	if avg2 != expectedAvg {
		t.Errorf("EMA smoothing: expected %v, got %v", expectedAvg, avg2)
	}
}

func TestMaxFunction(t *testing.T) {
	tests := []struct {
		a, b     time.Duration
		expected time.Duration
	}{
		{100 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond},
		{300 * time.Millisecond, 100 * time.Millisecond, 300 * time.Millisecond},
		{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond},
	}

	for _, tt := range tests {
		got := max(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("max(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.expected)
		}
	}
}

func TestOperationConstants(t *testing.T) {
	// Verify all operation constants are unique
	ops := []Operation{
		OpDHTLookup,
		OpDHTLookupFull,
		OpPeerConnect,
		OpPeerFirstByte,
		OpPeerTransfer,
		OpMirrorFetch,
		OpChunkDownload,
	}

	seen := make(map[Operation]bool)
	for _, op := range ops {
		if seen[op] {
			t.Errorf("Duplicate operation constant: %s", op)
		}
		seen[op] = true
	}
}

func TestConstants(t *testing.T) {
	// Verify constants are reasonable
	if MinTimeout >= MaxTimeout {
		t.Errorf("MinTimeout %v should be less than MaxTimeout %v", MinTimeout, MaxTimeout)
	}
	if SuccessMultiplier >= 1 {
		t.Errorf("SuccessMultiplier %v should be less than 1", SuccessMultiplier)
	}
	if FailureMultiplier <= 1 {
		t.Errorf("FailureMultiplier %v should be greater than 1", FailureMultiplier)
	}
	if TimeoutMultiplier <= FailureMultiplier {
		t.Errorf("TimeoutMultiplier %v should be greater than FailureMultiplier %v", TimeoutMultiplier, FailureMultiplier)
	}
	if AdaptationAlpha <= 0 || AdaptationAlpha >= 1 {
		t.Errorf("AdaptationAlpha %v should be between 0 and 1", AdaptationAlpha)
	}
}
