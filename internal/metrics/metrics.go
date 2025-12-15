// Package metrics provides Prometheus metrics for debswarm
package metrics

import (
	"net/http"
	"sync"
	"time"
)

// Metrics holds all application metrics
type Metrics struct {
	// Counters
	DownloadsTotal       *CounterVec
	BytesDownloaded      *CounterVec
	BytesUploaded        *CounterVec
	PeerConnections      *CounterVec
	DHTQueries           *CounterVec
	CacheHits            *Counter
	CacheMisses          *Counter
	VerificationFailures *Counter

	// Resume metrics
	DownloadsResumed *Counter
	ChunksRecovered  *Counter

	// Error breakdown
	Errors *CounterVec // labels: type (timeout, connection, verification)

	// Peer churn
	PeersJoined *Counter
	PeersLeft   *Counter

	// Gauges
	ConnectedPeers   *Gauge
	RoutingTableSize *Gauge
	CacheSize        *Gauge
	CacheCount       *Gauge
	ActiveDownloads  *Gauge
	ActiveUploads    *Gauge

	// Bandwidth rates (bytes per second, updated periodically)
	UploadRate   *Gauge
	DownloadRate *Gauge

	// Histograms
	DownloadDuration  *HistogramVec
	PeerLatency       *HistogramVec
	ChunkDownloadTime *Histogram
	DHTLookupDuration *Histogram
}

// Counter is a simple counter metric
type Counter struct {
	value int64
	mu    sync.Mutex
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	c.mu.Lock()
	c.value++
	c.mu.Unlock()
}

// Add adds the given value to the counter.
func (c *Counter) Add(v int64) {
	c.mu.Lock()
	c.value += v
	c.mu.Unlock()
}

// Value returns the current counter value.
func (c *Counter) Value() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

// CounterVec is a counter with labels for multi-dimensional metrics.
type CounterVec struct {
	counters map[string]*Counter
	mu       sync.RWMutex
}

// NewCounterVec creates a new labeled counter vector.
func NewCounterVec() *CounterVec {
	return &CounterVec{
		counters: make(map[string]*Counter),
	}
}

// WithLabel returns the counter for the given label, creating it if needed.
func (cv *CounterVec) WithLabel(label string) *Counter {
	cv.mu.Lock()
	defer cv.mu.Unlock()
	if c, ok := cv.counters[label]; ok {
		return c
	}
	c := &Counter{}
	cv.counters[label] = c
	return c
}

// Values returns all label-value pairs in the counter vector.
func (cv *CounterVec) Values() map[string]int64 {
	cv.mu.RLock()
	defer cv.mu.RUnlock()
	result := make(map[string]int64)
	for k, v := range cv.counters {
		result[k] = v.Value()
	}
	return result
}

// Gauge is a metric that can go up and down.
type Gauge struct {
	value float64
	mu    sync.Mutex
}

// Set sets the gauge to the given value.
func (g *Gauge) Set(v float64) {
	g.mu.Lock()
	g.value = v
	g.mu.Unlock()
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() {
	g.mu.Lock()
	g.value++
	g.mu.Unlock()
}

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() {
	g.mu.Lock()
	g.value--
	g.mu.Unlock()
}

// Add adds the given value to the gauge.
func (g *Gauge) Add(v float64) {
	g.mu.Lock()
	g.value += v
	g.mu.Unlock()
}

// Value returns the current gauge value.
func (g *Gauge) Value() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.value
}

// Histogram tracks distribution of values across buckets.
type Histogram struct {
	buckets []float64
	counts  []int64
	sum     float64
	count   int64
	mu      sync.Mutex
}

// NewHistogram creates a new histogram with the given bucket boundaries.
func NewHistogram(buckets []float64) *Histogram {
	return &Histogram{
		buckets: buckets,
		counts:  make([]int64, len(buckets)+1),
	}
}

// Observe records a value in the histogram.
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.count++
	for i, b := range h.buckets {
		if v <= b {
			h.counts[i]++
			return
		}
	}
	h.counts[len(h.buckets)]++
}

// Stats returns the current histogram statistics.
func (h *Histogram) Stats() (count int64, sum float64, buckets []int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bucketsCopy := make([]int64, len(h.counts))
	copy(bucketsCopy, h.counts)
	return h.count, h.sum, bucketsCopy
}

// HistogramVec is a histogram with labels for multi-dimensional metrics.
type HistogramVec struct {
	histograms map[string]*Histogram
	buckets    []float64
	mu         sync.RWMutex
}

// NewHistogramVec creates a new labeled histogram vector.
func NewHistogramVec(buckets []float64) *HistogramVec {
	return &HistogramVec{
		histograms: make(map[string]*Histogram),
		buckets:    buckets,
	}
}

// WithLabel returns the histogram for the given label, creating it if needed.
func (hv *HistogramVec) WithLabel(label string) *Histogram {
	hv.mu.Lock()
	defer hv.mu.Unlock()
	if h, ok := hv.histograms[label]; ok {
		return h
	}
	h := NewHistogram(hv.buckets)
	hv.histograms[label] = h
	return h
}

// Default buckets for different metric types
var (
	DurationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
	SizeBuckets     = []float64{1024, 10240, 102400, 1048576, 10485760, 104857600, 1073741824}
	LatencyBuckets  = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}
)

// New creates a new Metrics instance
func New() *Metrics {
	return &Metrics{
		DownloadsTotal:       NewCounterVec(),
		BytesDownloaded:      NewCounterVec(),
		BytesUploaded:        NewCounterVec(),
		PeerConnections:      NewCounterVec(),
		DHTQueries:           NewCounterVec(),
		CacheHits:            &Counter{},
		CacheMisses:          &Counter{},
		VerificationFailures: &Counter{},

		// Resume metrics
		DownloadsResumed: &Counter{},
		ChunksRecovered:  &Counter{},

		// Error breakdown
		Errors: NewCounterVec(),

		// Peer churn
		PeersJoined: &Counter{},
		PeersLeft:   &Counter{},

		ConnectedPeers:   &Gauge{},
		RoutingTableSize: &Gauge{},
		CacheSize:        &Gauge{},
		CacheCount:       &Gauge{},
		ActiveDownloads:  &Gauge{},
		ActiveUploads:    &Gauge{},

		// Bandwidth rates
		UploadRate:   &Gauge{},
		DownloadRate: &Gauge{},

		DownloadDuration:  NewHistogramVec(DurationBuckets),
		PeerLatency:       NewHistogramVec(LatencyBuckets),
		ChunkDownloadTime: NewHistogram(DurationBuckets),
		DHTLookupDuration: NewHistogram(DurationBuckets),
	}
}

// Handler returns an HTTP handler for Prometheus metrics endpoint
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")

		// Counters
		writeCounter(w, "debswarm_cache_hits_total", m.CacheHits.Value())
		writeCounter(w, "debswarm_cache_misses_total", m.CacheMisses.Value())
		writeCounter(w, "debswarm_verification_failures_total", m.VerificationFailures.Value())

		// Resume metrics
		writeCounter(w, "debswarm_downloads_resumed_total", m.DownloadsResumed.Value())
		writeCounter(w, "debswarm_chunks_recovered_total", m.ChunksRecovered.Value())

		// Peer churn
		writeCounter(w, "debswarm_peers_joined_total", m.PeersJoined.Value())
		writeCounter(w, "debswarm_peers_left_total", m.PeersLeft.Value())

		for label, value := range m.DownloadsTotal.Values() {
			writeCounterWithLabel(w, "debswarm_downloads_total", "source", label, value)
		}
		for label, value := range m.BytesDownloaded.Values() {
			writeCounterWithLabel(w, "debswarm_bytes_downloaded_total", "source", label, value)
		}
		for label, value := range m.BytesUploaded.Values() {
			writeCounterWithLabel(w, "debswarm_bytes_uploaded_total", "peer", label, value)
		}
		// Error breakdown
		for label, value := range m.Errors.Values() {
			writeCounterWithLabel(w, "debswarm_errors_total", "type", label, value)
		}

		// Gauges
		writeGauge(w, "debswarm_connected_peers", m.ConnectedPeers.Value())
		writeGauge(w, "debswarm_routing_table_size", m.RoutingTableSize.Value())
		writeGauge(w, "debswarm_cache_size_bytes", m.CacheSize.Value())
		writeGauge(w, "debswarm_cache_count", m.CacheCount.Value())
		writeGauge(w, "debswarm_active_downloads", m.ActiveDownloads.Value())
		writeGauge(w, "debswarm_active_uploads", m.ActiveUploads.Value())

		// Bandwidth rates
		writeGauge(w, "debswarm_upload_bytes_per_second", m.UploadRate.Value())
		writeGauge(w, "debswarm_download_bytes_per_second", m.DownloadRate.Value())

		// Histograms
		writeHistogram(w, "debswarm_chunk_download_seconds", m.ChunkDownloadTime)
		writeHistogram(w, "debswarm_dht_lookup_seconds", m.DHTLookupDuration)
	})
}

func writeCounter(w http.ResponseWriter, name string, value int64) {
	_, _ = w.Write([]byte("# TYPE " + name + " counter\n"))
	_, _ = w.Write([]byte(name + " " + itoa(value) + "\n"))
}

func writeCounterWithLabel(w http.ResponseWriter, name, labelName, labelValue string, value int64) {
	_, _ = w.Write([]byte(name + "{" + labelName + "=\"" + labelValue + "\"} " + itoa(value) + "\n"))
}

func writeGauge(w http.ResponseWriter, name string, value float64) {
	_, _ = w.Write([]byte("# TYPE " + name + " gauge\n"))
	_, _ = w.Write([]byte(name + " " + ftoa(value) + "\n"))
}

func writeHistogram(w http.ResponseWriter, name string, h *Histogram) {
	count, sum, buckets := h.Stats()
	_, _ = w.Write([]byte("# TYPE " + name + " histogram\n"))

	cumulative := int64(0)
	for i, b := range h.buckets {
		cumulative += buckets[i]
		_, _ = w.Write([]byte(name + "_bucket{le=\"" + ftoa(b) + "\"} " + itoa(cumulative) + "\n"))
	}
	cumulative += buckets[len(buckets)-1]
	_, _ = w.Write([]byte(name + "_bucket{le=\"+Inf\"} " + itoa(cumulative) + "\n"))
	_, _ = w.Write([]byte(name + "_sum " + ftoa(sum) + "\n"))
	_, _ = w.Write([]byte(name + "_count " + itoa(count) + "\n"))
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func ftoa(f float64) string {
	if f == float64(int64(f)) {
		return itoa(int64(f))
	}
	// Simple float formatting
	intPart := int64(f)
	fracPart := int64((f - float64(intPart)) * 1000000)
	if fracPart < 0 {
		fracPart = -fracPart
	}
	return itoa(intPart) + "." + itoa(fracPart)
}

// Timer is a helper for timing operations
type Timer struct {
	start time.Time
	h     *Histogram
}

// NewTimer creates a new timer that will observe to the given histogram
func NewTimer(h *Histogram) *Timer {
	return &Timer{
		start: time.Now(),
		h:     h,
	}
}

// ObserveDuration records the elapsed time
func (t *Timer) ObserveDuration() time.Duration {
	d := time.Since(t.start)
	if t.h != nil {
		t.h.Observe(d.Seconds())
	}
	return d
}
