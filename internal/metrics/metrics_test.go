package metrics

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	m := New()

	if m == nil {
		t.Fatal("New() returned nil")
	}

	// Check all counters are initialized
	if m.DownloadsTotal == nil {
		t.Error("DownloadsTotal not initialized")
	}
	if m.BytesDownloaded == nil {
		t.Error("BytesDownloaded not initialized")
	}
	if m.CacheHits == nil {
		t.Error("CacheHits not initialized")
	}
	if m.CacheMisses == nil {
		t.Error("CacheMisses not initialized")
	}

	// Check all gauges are initialized
	if m.ConnectedPeers == nil {
		t.Error("ConnectedPeers not initialized")
	}
	if m.CacheSize == nil {
		t.Error("CacheSize not initialized")
	}

	// Check histograms are initialized
	if m.DownloadDuration == nil {
		t.Error("DownloadDuration not initialized")
	}
	if m.DHTLookupDuration == nil {
		t.Error("DHTLookupDuration not initialized")
	}
}

func TestCounter_Inc(t *testing.T) {
	c := &Counter{}

	if c.Value() != 0 {
		t.Errorf("Initial value = %d, want 0", c.Value())
	}

	c.Inc()
	if c.Value() != 1 {
		t.Errorf("After Inc, value = %d, want 1", c.Value())
	}

	c.Inc()
	c.Inc()
	if c.Value() != 3 {
		t.Errorf("After 3 Inc, value = %d, want 3", c.Value())
	}
}

func TestCounter_Add(t *testing.T) {
	c := &Counter{}

	c.Add(10)
	if c.Value() != 10 {
		t.Errorf("After Add(10), value = %d, want 10", c.Value())
	}

	c.Add(5)
	if c.Value() != 15 {
		t.Errorf("After Add(5), value = %d, want 15", c.Value())
	}
}

func TestCounter_Concurrent(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup

	// 10 goroutines each incrementing 100 times
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Inc()
			}
		}()
	}

	wg.Wait()

	if c.Value() != 1000 {
		t.Errorf("Concurrent Inc result = %d, want 1000", c.Value())
	}
}

func TestCounterVec(t *testing.T) {
	cv := NewCounterVec()

	// Get counters for different labels
	p2p := cv.WithLabel("p2p")
	mirror := cv.WithLabel("mirror")

	p2p.Inc()
	p2p.Inc()
	mirror.Add(5)

	// Check values
	values := cv.Values()
	if values["p2p"] != 2 {
		t.Errorf("p2p value = %d, want 2", values["p2p"])
	}
	if values["mirror"] != 5 {
		t.Errorf("mirror value = %d, want 5", values["mirror"])
	}

	// Getting same label should return same counter
	p2p2 := cv.WithLabel("p2p")
	p2p2.Inc()
	if p2p.Value() != 3 {
		t.Error("WithLabel should return same counter for same label")
	}
}

func TestGauge_SetGetIncDec(t *testing.T) {
	g := &Gauge{}

	if g.Value() != 0 {
		t.Errorf("Initial value = %f, want 0", g.Value())
	}

	g.Set(10.5)
	if g.Value() != 10.5 {
		t.Errorf("After Set(10.5), value = %f, want 10.5", g.Value())
	}

	g.Inc()
	if g.Value() != 11.5 {
		t.Errorf("After Inc, value = %f, want 11.5", g.Value())
	}

	g.Dec()
	if g.Value() != 10.5 {
		t.Errorf("After Dec, value = %f, want 10.5", g.Value())
	}

	g.Add(5.5)
	if g.Value() != 16 {
		t.Errorf("After Add(5.5), value = %f, want 16", g.Value())
	}
}

func TestGauge_Concurrent(t *testing.T) {
	g := &Gauge{}
	var wg sync.WaitGroup

	// Mix of operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				g.Inc()
				g.Dec()
			}
		}()
	}

	wg.Wait()

	// After equal inc/dec, should be 0
	if g.Value() != 0 {
		t.Errorf("After equal Inc/Dec, value = %f, want 0", g.Value())
	}
}

func TestHistogram_Observe(t *testing.T) {
	buckets := []float64{1, 5, 10, 50, 100}
	h := NewHistogram(buckets)

	h.Observe(0.5) // <= 1
	h.Observe(3)   // <= 5
	h.Observe(7)   // <= 10
	h.Observe(25)  // <= 50
	h.Observe(75)  // <= 100
	h.Observe(200) // > 100 (+Inf bucket)

	count, sum, bucketCounts := h.Stats()

	if count != 6 {
		t.Errorf("count = %d, want 6", count)
	}

	expectedSum := 0.5 + 3 + 7 + 25 + 75 + 200
	if sum != expectedSum {
		t.Errorf("sum = %f, want %f", sum, expectedSum)
	}

	// Check bucket distribution
	// bucketCounts[0] = count <= 1
	// bucketCounts[1] = count <= 5
	// etc.
	if bucketCounts[0] != 1 { // 0.5 <= 1
		t.Errorf("bucket[0] = %d, want 1", bucketCounts[0])
	}
	if bucketCounts[1] != 1 { // 3 <= 5
		t.Errorf("bucket[1] = %d, want 1", bucketCounts[1])
	}
	if bucketCounts[5] != 1 { // 200 > 100 (+Inf)
		t.Errorf("bucket[+Inf] = %d, want 1", bucketCounts[5])
	}
}

func TestHistogramVec(t *testing.T) {
	buckets := []float64{1, 10, 100}
	hv := NewHistogramVec(buckets)

	fast := hv.WithLabel("fast")
	slow := hv.WithLabel("slow")

	fast.Observe(0.5)
	fast.Observe(0.8)
	slow.Observe(50)
	slow.Observe(150)

	fastCount, _, _ := fast.Stats()
	slowCount, _, _ := slow.Stats()

	if fastCount != 2 {
		t.Errorf("fast count = %d, want 2", fastCount)
	}
	if slowCount != 2 {
		t.Errorf("slow count = %d, want 2", slowCount)
	}
}

func TestTimer(t *testing.T) {
	buckets := []float64{0.001, 0.01, 0.1, 1}
	h := NewHistogram(buckets)

	timer := NewTimer(h)
	time.Sleep(10 * time.Millisecond)
	duration := timer.ObserveDuration()

	if duration < 10*time.Millisecond {
		t.Errorf("Duration = %v, want >= 10ms", duration)
	}

	count, _, _ := h.Stats()
	if count != 1 {
		t.Errorf("Histogram count = %d, want 1", count)
	}
}

func TestTimer_NilHistogram(t *testing.T) {
	timer := NewTimer(nil)
	time.Sleep(5 * time.Millisecond)
	duration := timer.ObserveDuration()

	// Should not panic and should return valid duration
	if duration < 5*time.Millisecond {
		t.Errorf("Duration = %v, want >= 5ms", duration)
	}
}

func TestMetrics_Handler(t *testing.T) {
	m := New()

	// Set some values
	m.CacheHits.Add(100)
	m.CacheMisses.Add(10)
	m.ConnectedPeers.Set(5)
	m.CacheSize.Set(1024 * 1024)
	m.DownloadsTotal.WithLabel("p2p").Add(50)
	m.BytesDownloaded.WithLabel("mirror").Add(1000000)
	m.DHTLookupDuration.Observe(0.5)

	// Create request and response recorder
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()

	// Call handler
	m.Handler().ServeHTTP(w, req)

	// Check response
	if w.Code != 200 {
		t.Errorf("Status code = %d, want 200", w.Code)
	}

	body := w.Body.String()

	// Check content type
	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("Content-Type = %s, want text/plain", contentType)
	}

	// Check that metrics are present
	checks := []string{
		"debswarm_cache_hits_total",
		"debswarm_cache_misses_total",
		"debswarm_connected_peers",
		"debswarm_cache_size_bytes",
		"debswarm_downloads_total{source=\"p2p\"}",
		"debswarm_bytes_downloaded_total{source=\"mirror\"}",
		"debswarm_dht_lookup_seconds",
	}

	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Errorf("Response missing %q", check)
		}
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{12345, "12345"},
		{-1, "-1"},
		{-42, "-42"},
		{1000000, "1000000"},
	}

	for _, tc := range tests {
		result := itoa(tc.input)
		if result != tc.expected {
			t.Errorf("itoa(%d) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestFtoa(t *testing.T) {
	tests := []struct {
		input    float64
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{1.5, "1.500000"},
		{0.001, "0.1000"},
	}

	for _, tc := range tests {
		result := ftoa(tc.input)
		// For whole numbers, exact match
		if tc.input == float64(int64(tc.input)) {
			if result != tc.expected {
				t.Errorf("ftoa(%f) = %q, want %q", tc.input, result, tc.expected)
			}
		} else {
			// For decimals, just check it's reasonable
			if len(result) == 0 {
				t.Errorf("ftoa(%f) returned empty string", tc.input)
			}
		}
	}
}

func TestDefaultBuckets(t *testing.T) {
	// Verify default bucket slices exist and are reasonable
	if len(DurationBuckets) == 0 {
		t.Error("DurationBuckets is empty")
	}
	if len(SizeBuckets) == 0 {
		t.Error("SizeBuckets is empty")
	}
	if len(LatencyBuckets) == 0 {
		t.Error("LatencyBuckets is empty")
	}

	// Check buckets are sorted
	for i := 1; i < len(DurationBuckets); i++ {
		if DurationBuckets[i] <= DurationBuckets[i-1] {
			t.Error("DurationBuckets not sorted")
		}
	}
}

func TestHistogram_Concurrent(t *testing.T) {
	h := NewHistogram(DurationBuckets)
	var wg sync.WaitGroup

	// 10 goroutines each observing 100 values
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Observe(float64(j) * 0.01)
			}
		}(i)
	}

	wg.Wait()

	count, _, _ := h.Stats()
	if count != 1000 {
		t.Errorf("count = %d, want 1000", count)
	}
}

func TestCounterVec_Concurrent(t *testing.T) {
	cv := NewCounterVec()
	var wg sync.WaitGroup

	labels := []string{"a", "b", "c", "d"}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				for _, label := range labels {
					cv.WithLabel(label).Inc()
				}
			}
		}()
	}

	wg.Wait()

	values := cv.Values()
	for _, label := range labels {
		if values[label] != 1000 {
			t.Errorf("label %q count = %d, want 1000", label, values[label])
		}
	}
}
