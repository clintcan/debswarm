package benchmark

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/debswarm/debswarm/internal/downloader"
)

func TestGenerateTestData(t *testing.T) {
	tests := []struct {
		name string
		size int64
	}{
		{"zero bytes", 0},
		{"one byte", 1},
		{"small", 100},
		{"medium", 1024},
		{"large", 10 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := GenerateTestData(tt.size)
			if int64(len(data)) != tt.size {
				t.Errorf("GenerateTestData(%d) returned %d bytes", tt.size, len(data))
			}

			// Verify determinism - same input should produce same output
			data2 := GenerateTestData(tt.size)
			if !bytes.Equal(data, data2) {
				t.Error("GenerateTestData is not deterministic")
			}
		})
	}
}

func TestGenerateTestData_Pattern(t *testing.T) {
	// Verify the pattern is applied correctly
	data := GenerateTestData(10)
	for i := range data {
		expected := byte(i*7 + i/256)
		if data[i] != expected {
			t.Errorf("data[%d] = %d, expected %d", i, data[i], expected)
		}
	}
}

func TestDefaultPeerConfig(t *testing.T) {
	cfg := DefaultPeerConfig("test-peer-1")

	if cfg.ID != "test-peer-1" {
		t.Errorf("ID = %q, want %q", cfg.ID, "test-peer-1")
	}
	if cfg.LatencyMin != 10*time.Millisecond {
		t.Errorf("LatencyMin = %v, want %v", cfg.LatencyMin, 10*time.Millisecond)
	}
	if cfg.LatencyMax != 30*time.Millisecond {
		t.Errorf("LatencyMax = %v, want %v", cfg.LatencyMax, 30*time.Millisecond)
	}
	if cfg.ThroughputBps != 50*1024*1024 {
		t.Errorf("ThroughputBps = %d, want %d", cfg.ThroughputBps, 50*1024*1024)
	}
	if cfg.ErrorRate != 0.0 {
		t.Errorf("ErrorRate = %f, want 0.0", cfg.ErrorRate)
	}
	if cfg.TimeoutRate != 0.0 {
		t.Errorf("TimeoutRate = %f, want 0.0", cfg.TimeoutRate)
	}
}

func TestSortDurations(t *testing.T) {
	tests := []struct {
		name     string
		input    []time.Duration
		expected []time.Duration
	}{
		{
			name:     "empty",
			input:    []time.Duration{},
			expected: []time.Duration{},
		},
		{
			name:     "single element",
			input:    []time.Duration{5 * time.Second},
			expected: []time.Duration{5 * time.Second},
		},
		{
			name:     "already sorted",
			input:    []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second},
			expected: []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second},
		},
		{
			name:     "reverse sorted",
			input:    []time.Duration{3 * time.Second, 2 * time.Second, 1 * time.Second},
			expected: []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second},
		},
		{
			name:     "random order",
			input:    []time.Duration{5 * time.Millisecond, 1 * time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond},
			expected: []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 4 * time.Millisecond, 5 * time.Millisecond},
		},
		{
			name:     "with duplicates",
			input:    []time.Duration{2 * time.Second, 1 * time.Second, 2 * time.Second, 1 * time.Second},
			expected: []time.Duration{1 * time.Second, 1 * time.Second, 2 * time.Second, 2 * time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy since sortDurations modifies in place
			input := make([]time.Duration, len(tt.input))
			copy(input, tt.input)

			sortDurations(input)

			if len(input) != len(tt.expected) {
				t.Fatalf("length mismatch: got %d, want %d", len(input), len(tt.expected))
			}
			for i := range input {
				if input[i] != tt.expected[i] {
					t.Errorf("index %d: got %v, want %v", i, input[i], tt.expected[i])
				}
			}
		})
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"timeout", errors.New("connection timeout"), "timeout"},
		{"timeout with context", errors.New("context deadline exceeded timeout"), "timeout"},
		{"connection refused", errors.New("dial tcp: connection refused"), "connection_refused"},
		{"eof", errors.New("unexpected EOF"), "eof"},
		{"context canceled", errors.New("context canceled"), "canceled"},
		{"other error", errors.New("some random error"), "other"},
		{"empty error", errors.New(""), "other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyError(tt.err)
			if result != tt.expected {
				t.Errorf("classifyError(%q) = %q, want %q", tt.err.Error(), result, tt.expected)
			}
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		s        string
		substr   string
		expected bool
	}{
		{"hello world", "world", true},
		{"hello world", "hello", true},
		{"hello world", "o w", true},
		{"hello world", "xyz", false},
		{"hello", "hello world", false}, // substr longer than s
		{"", "", true},
		{"hello", "", true},
		{"", "hello", false},
		{"a", "a", true},
		{"ab", "b", true},
		{"ab", "a", true},
	}

	for _, tt := range tests {
		t.Run(tt.s+"_"+tt.substr, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			if result != tt.expected {
				t.Errorf("contains(%q, %q) = %v, want %v", tt.s, tt.substr, result, tt.expected)
			}
		})
	}
}

func TestContainsAt(t *testing.T) {
	tests := []struct {
		s        string
		substr   string
		start    int
		expected bool
	}{
		{"hello world", "world", 0, true},
		{"hello world", "hello", 0, true},
		{"hello world", "hello", 1, false},
		{"hello world", "world", 6, true},
		{"hello world", "ello", 0, true},
		{"hello world", "ello", 1, true},
		{"hello world", "ello", 2, false},
		{"", "", 0, true},
		{"a", "a", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.s+"_"+tt.substr, func(t *testing.T) {
			result := containsAt(tt.s, tt.substr, tt.start)
			if result != tt.expected {
				t.Errorf("containsAt(%q, %q, %d) = %v, want %v", tt.s, tt.substr, tt.start, result, tt.expected)
			}
		})
	}
}

func TestNewSimulatedPeer(t *testing.T) {
	cfg := DefaultPeerConfig("test-peer")
	peer := NewSimulatedPeer(cfg)

	if peer == nil {
		t.Fatal("NewSimulatedPeer returned nil")
	}
	if peer.cfg.ID != "test-peer" {
		t.Errorf("peer.cfg.ID = %q, want %q", peer.cfg.ID, "test-peer")
	}
	if peer.content == nil {
		t.Error("peer.content is nil")
	}
	if peer.rng == nil {
		t.Error("peer.rng is nil")
	}
}

func TestSimulatedPeer_ID(t *testing.T) {
	peer := NewSimulatedPeer(DefaultPeerConfig("my-peer-id"))
	if peer.ID() != "my-peer-id" {
		t.Errorf("ID() = %q, want %q", peer.ID(), "my-peer-id")
	}
}

func TestSimulatedPeer_Type(t *testing.T) {
	peer := NewSimulatedPeer(DefaultPeerConfig("test"))
	if peer.Type() != downloader.SourceTypePeer {
		t.Errorf("Type() = %q, want %q", peer.Type(), downloader.SourceTypePeer)
	}
}

func TestSimulatedPeer_AddContent(t *testing.T) {
	peer := NewSimulatedPeer(DefaultPeerConfig("test"))
	testData := []byte("test content")
	testHash := "abc123"

	peer.AddContent(testHash, testData)

	peer.mu.RLock()
	data, ok := peer.content[testHash]
	peer.mu.RUnlock()

	if !ok {
		t.Fatal("content not found after AddContent")
	}
	if !bytes.Equal(data, testData) {
		t.Errorf("content mismatch: got %q, want %q", data, testData)
	}
}

func TestSimulatedPeer_AddGeneratedContent(t *testing.T) {
	peer := NewSimulatedPeer(DefaultPeerConfig("test"))
	size := int64(1024)

	hash := peer.AddGeneratedContent(size)

	if hash == "" {
		t.Fatal("AddGeneratedContent returned empty hash")
	}
	if len(hash) != 64 { // SHA256 hex = 64 chars
		t.Errorf("hash length = %d, want 64", len(hash))
	}

	peer.mu.RLock()
	data, ok := peer.content[hash]
	peer.mu.RUnlock()

	if !ok {
		t.Fatal("content not found after AddGeneratedContent")
	}
	if int64(len(data)) != size {
		t.Errorf("content size = %d, want %d", len(data), size)
	}
}

func TestSimulatedPeer_Stats_Initial(t *testing.T) {
	peer := NewSimulatedPeer(DefaultPeerConfig("test"))
	stats := peer.Stats()

	if stats.ID != "test" {
		t.Errorf("stats.ID = %q, want %q", stats.ID, "test")
	}
	if stats.RequestCount != 0 {
		t.Errorf("stats.RequestCount = %d, want 0", stats.RequestCount)
	}
	if stats.BytesServed != 0 {
		t.Errorf("stats.BytesServed = %d, want 0", stats.BytesServed)
	}
	if stats.ErrorCount != 0 {
		t.Errorf("stats.ErrorCount = %d, want 0", stats.ErrorCount)
	}
}

func TestSimulatedPeer_Download_Success(t *testing.T) {
	cfg := PeerConfig{
		ID:            "fast-peer",
		LatencyMin:    1 * time.Millisecond,
		LatencyMax:    2 * time.Millisecond,
		ThroughputBps: 100 * 1024 * 1024, // 100 MB/s
		ErrorRate:     0,
		TimeoutRate:   0,
	}
	peer := NewSimulatedPeer(cfg)

	testData := []byte("hello world")
	testHash := peer.AddGeneratedContent(int64(len(testData)))
	// Use the generated content instead
	peer.mu.RLock()
	expectedData := peer.content[testHash]
	peer.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	data, err := peer.DownloadFull(ctx, testHash)
	if err != nil {
		t.Fatalf("DownloadFull failed: %v", err)
	}

	if !bytes.Equal(data, expectedData) {
		t.Error("downloaded data doesn't match expected")
	}

	stats := peer.Stats()
	if stats.RequestCount != 1 {
		t.Errorf("RequestCount = %d, want 1", stats.RequestCount)
	}
	if stats.BytesServed != int64(len(expectedData)) {
		t.Errorf("BytesServed = %d, want %d", stats.BytesServed, len(expectedData))
	}
}

func TestSimulatedPeer_Download_ContentNotFound(t *testing.T) {
	peer := NewSimulatedPeer(DefaultPeerConfig("test"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := peer.DownloadFull(ctx, "nonexistent-hash-1234567890")
	if err == nil {
		t.Fatal("expected error for nonexistent content")
	}
	if !contains(err.Error(), "content not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSimulatedPeer_Download_RangeRequest(t *testing.T) {
	cfg := PeerConfig{
		ID:            "test",
		LatencyMin:    1 * time.Millisecond,
		LatencyMax:    1 * time.Millisecond,
		ThroughputBps: 100 * 1024 * 1024,
	}
	peer := NewSimulatedPeer(cfg)

	// Add 100 bytes of content
	hash := peer.AddGeneratedContent(100)

	ctx := context.Background()

	// Request bytes 10-20
	data, err := peer.Download(ctx, hash, 10, 20)
	if err != nil {
		t.Fatalf("Download range failed: %v", err)
	}
	if len(data) != 10 {
		t.Errorf("data length = %d, want 10", len(data))
	}
}

func TestSimulatedPeer_Download_ContextCanceled(t *testing.T) {
	cfg := PeerConfig{
		ID:            "slow-peer",
		LatencyMin:    1 * time.Second, // Slow peer
		LatencyMax:    2 * time.Second,
		ThroughputBps: 1024,
	}
	peer := NewSimulatedPeer(cfg)
	peer.AddGeneratedContent(100)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := peer.DownloadFull(ctx, "somehash")
	if err == nil {
		t.Fatal("expected context canceled error")
	}
}

func TestPrintProxyLoadResult(t *testing.T) {
	result := &ProxyLoadResult{
		TotalRequests:  100,
		SuccessCount:   95,
		ErrorCount:     5,
		TotalBytes:     1024 * 1024,
		Duration:       10 * time.Second,
		RequestsPerSec: 10.0,
		AvgLatency:     50 * time.Millisecond,
		MinLatency:     10 * time.Millisecond,
		MaxLatency:     100 * time.Millisecond,
		P50Latency:     45 * time.Millisecond,
		P95Latency:     90 * time.Millisecond,
		P99Latency:     95 * time.Millisecond,
		ThroughputMBps: 0.1,
		StatusCodeDist: map[int]int64{200: 90, 404: 5},
		ErrorsByType:   map[string]int64{"timeout": 3, "other": 2},
	}

	var buf bytes.Buffer
	PrintProxyLoadResult(&buf, result)

	output := buf.String()

	// Verify key sections are present
	checks := []string{
		"Proxy Load Test Results",
		"Duration:",
		"Total requests:",
		"Successful:",
		"Failed:",
		"Requests/sec:",
		"Throughput:",
		"Latency:",
		"Avg:",
		"P50:",
		"P95:",
		"P99:",
		"Status codes:",
		"200:",
		"404:",
		"Errors by type:",
		"timeout:",
	}

	for _, check := range checks {
		if !contains(output, check) {
			t.Errorf("output missing %q", check)
		}
	}
}

func TestProxyLoadResult_EmptyMaps(t *testing.T) {
	result := &ProxyLoadResult{
		TotalRequests:  0,
		StatusCodeDist: map[int]int64{},
		ErrorsByType:   map[string]int64{},
	}

	var buf bytes.Buffer
	PrintProxyLoadResult(&buf, result)

	// Should not panic with empty maps
	if buf.Len() == 0 {
		t.Error("expected some output even with empty result")
	}
}
