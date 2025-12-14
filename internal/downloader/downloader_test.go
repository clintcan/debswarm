package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/debswarm/debswarm/internal/peers"
	"github.com/libp2p/go-libp2p/core/peer"
)

// mockSource is a test source implementation
type mockSource struct {
	id          string
	sourceType  string
	data        []byte
	err         error
	delay       time.Duration
	callCount   int32
	rangeSupport bool
}

func (m *mockSource) ID() string   { return m.id }
func (m *mockSource) Type() string { return m.sourceType }

func (m *mockSource) Download(ctx context.Context, hash string, start, end int64) ([]byte, error) {
	atomic.AddInt32(&m.callCount, 1)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	if !m.rangeSupport {
		return m.data, nil
	}
	if end > int64(len(m.data)) {
		end = int64(len(m.data))
	}
	if start >= int64(len(m.data)) {
		return nil, errors.New("start beyond data length")
	}
	return m.data[start:end], nil
}

func (m *mockSource) DownloadFull(ctx context.Context, hash string) ([]byte, error) {
	atomic.AddInt32(&m.callCount, 1)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.data, nil
}

func testData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	return data
}

func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestNew(t *testing.T) {
	d := New(&Config{
		ChunkSize:     1024,
		MaxConcurrent: 4,
	})

	if d.chunkSize != 1024 {
		t.Errorf("Expected chunk size 1024, got %d", d.chunkSize)
	}
	if d.maxConc != 4 {
		t.Errorf("Expected max concurrent 4, got %d", d.maxConc)
	}
}

func TestNewDefaults(t *testing.T) {
	d := New(&Config{})

	if d.chunkSize != DefaultChunkSize {
		t.Errorf("Expected default chunk size %d, got %d", DefaultChunkSize, d.chunkSize)
	}
	if d.maxConc != MaxConcurrentChunks {
		t.Errorf("Expected default max concurrent %d, got %d", MaxConcurrentChunks, d.maxConc)
	}
}

func TestDownloadRacingPeerWins(t *testing.T) {
	data := testData(1000)
	hash := hashBytes(data)

	peerSource := &mockSource{
		id:         "peer1",
		sourceType: SourceTypePeer,
		data:       data,
		delay:      10 * time.Millisecond,
	}

	mirrorSource := &mockSource{
		id:         "mirror1",
		sourceType: SourceTypeMirror,
		data:       data,
		delay:      500 * time.Millisecond, // Much slower
	}

	d := New(&Config{})
	result, err := d.Download(context.Background(), hash, int64(len(data)), []Source{peerSource}, mirrorSource)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if result.Source != SourceTypePeer {
		t.Errorf("Expected peer source, got %s", result.Source)
	}

	if result.Hash != hash {
		t.Errorf("Hash mismatch")
	}
}

func TestDownloadRacingMirrorWins(t *testing.T) {
	data := testData(1000)
	hash := hashBytes(data)

	peerSource := &mockSource{
		id:         "peer1",
		sourceType: SourceTypePeer,
		data:       data,
		delay:      500 * time.Millisecond, // Slow peer
	}

	mirrorSource := &mockSource{
		id:         "mirror1",
		sourceType: SourceTypeMirror,
		data:       data,
		delay:      10 * time.Millisecond, // Fast mirror
	}

	d := New(&Config{})
	result, err := d.Download(context.Background(), hash, int64(len(data)), []Source{peerSource}, mirrorSource)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if result.Source != SourceTypeMirror {
		t.Errorf("Expected mirror source, got %s", result.Source)
	}
}

func TestDownloadRacingHashMismatch(t *testing.T) {
	data := testData(1000)
	wrongData := testData(1001) // Different data
	hash := hashBytes(data)

	scorer := peers.NewScorer()

	peerSource := &mockSource{
		id:         "peer1",
		sourceType: SourceTypePeer,
		data:       wrongData, // Wrong data
	}

	mirrorSource := &mockSource{
		id:         "mirror1",
		sourceType: SourceTypeMirror,
		data:       data, // Correct data
		delay:      300 * time.Millisecond,
	}

	d := New(&Config{Scorer: scorer})
	result, err := d.Download(context.Background(), hash, int64(len(data)), []Source{peerSource}, mirrorSource)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	// Mirror should win since peer has wrong hash
	if result.Source != SourceTypeMirror {
		t.Errorf("Expected mirror source after peer hash mismatch, got %s", result.Source)
	}
}

func TestDownloadRacingNoSources(t *testing.T) {
	d := New(&Config{})
	_, err := d.Download(context.Background(), "abc123", 1000, nil, nil)

	if err != ErrNoSources {
		t.Errorf("Expected ErrNoSources, got %v", err)
	}
}

func TestDownloadRacingAllFail(t *testing.T) {
	peerSource := &mockSource{
		id:         "peer1",
		sourceType: SourceTypePeer,
		err:        errors.New("peer error"),
	}

	mirrorSource := &mockSource{
		id:         "mirror1",
		sourceType: SourceTypeMirror,
		err:        errors.New("mirror error"),
	}

	d := New(&Config{})
	_, err := d.Download(context.Background(), "abc123", 1000, []Source{peerSource}, mirrorSource)

	if err == nil {
		t.Fatal("Expected error when all sources fail")
	}

	if !errors.Is(err, ErrAllSourcesFailed) {
		t.Errorf("Expected ErrAllSourcesFailed, got %v", err)
	}
}

func TestDownloadChunked(t *testing.T) {
	// Create data larger than MinChunkedSize
	data := testData(15 * 1024 * 1024) // 15 MB
	hash := hashBytes(data)

	peerSource := &mockSource{
		id:           "peer1",
		sourceType:   SourceTypePeer,
		data:         data,
		rangeSupport: true,
	}

	d := New(&Config{
		ChunkSize:     4 * 1024 * 1024, // 4 MB chunks
		MaxConcurrent: 4,
	})

	result, err := d.Download(context.Background(), hash, int64(len(data)), []Source{peerSource}, nil)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if result.Hash != hash {
		t.Error("Hash mismatch")
	}

	if result.ChunksTotal != 4 { // 15MB / 4MB = 4 chunks (rounded up)
		t.Errorf("Expected 4 chunks, got %d", result.ChunksTotal)
	}

	if result.Source != SourceTypePeer {
		t.Errorf("Expected peer source, got %s", result.Source)
	}
}

func TestDownloadChunkedMixedSources(t *testing.T) {
	data := testData(12 * 1024 * 1024) // 12 MB
	hash := hashBytes(data)

	peer1 := &mockSource{
		id:           "peer1",
		sourceType:   SourceTypePeer,
		data:         data,
		rangeSupport: true,
		delay:        5 * time.Millisecond,
	}

	peer2 := &mockSource{
		id:           "peer2",
		sourceType:   SourceTypePeer,
		data:         data,
		rangeSupport: true,
		delay:        5 * time.Millisecond,
	}

	mirror := &mockSource{
		id:           "mirror1",
		sourceType:   SourceTypeMirror,
		data:         data,
		rangeSupport: true,
		delay:        5 * time.Millisecond,
	}

	d := New(&Config{
		ChunkSize:     4 * 1024 * 1024,
		MaxConcurrent: 3,
	})

	result, err := d.Download(context.Background(), hash, int64(len(data)), []Source{peer1, peer2}, mirror)

	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if result.Hash != hash {
		t.Error("Hash mismatch")
	}

	// All sources should have been used
	if peer1.callCount == 0 && peer2.callCount == 0 && mirror.callCount == 0 {
		t.Error("Expected at least one source to be used")
	}
}

func TestDownloadContextCancellation(t *testing.T) {
	data := testData(1000)
	hash := hashBytes(data)

	peerSource := &mockSource{
		id:         "peer1",
		sourceType: SourceTypePeer,
		data:       data,
		delay:      1 * time.Second, // Slow
	}

	d := New(&Config{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := d.Download(ctx, hash, int64(len(data)), []Source{peerSource}, nil)

	if err == nil {
		t.Fatal("Expected context cancellation error")
	}
}

func TestSourceTracker(t *testing.T) {
	st := &sourceTracker{
		stats: make(map[string]*sourceStats),
	}

	source1 := &mockSource{id: "source1", sourceType: SourceTypePeer}
	source2 := &mockSource{id: "source2", sourceType: SourceTypePeer}

	// Initially both unknown - should return one of them
	selected := st.selectBest([]Source{source1, source2})
	if selected == nil {
		t.Fatal("Expected a source to be selected")
	}

	// Record successes for source1
	st.recordSuccess("source1", 1024, 10*time.Millisecond)
	st.recordSuccess("source1", 1024, 10*time.Millisecond)

	// Record failure for source2
	st.recordFailure("source2")

	// Now source1 should be preferred
	selected = st.selectBest([]Source{source1, source2})
	if selected.ID() != "source1" {
		t.Errorf("Expected source1 to be selected, got %s", selected.ID())
	}
}

func TestSourceTrackerRecentFailurePenalty(t *testing.T) {
	st := &sourceTracker{
		stats: make(map[string]*sourceStats),
	}

	source1 := &mockSource{id: "source1", sourceType: SourceTypePeer}
	source2 := &mockSource{id: "source2", sourceType: SourceTypePeer}

	// Give both sources equal success
	st.recordSuccess("source1", 1024, 10*time.Millisecond)
	st.recordSuccess("source2", 1024, 10*time.Millisecond)

	// Recent failure for source1
	st.recordFailure("source1")

	// source2 should now be preferred due to recent failure penalty
	selected := st.selectBest([]Source{source1, source2})
	if selected.ID() != "source2" {
		t.Errorf("Expected source2 after source1 failure, got %s", selected.ID())
	}
}

func TestPeerSource(t *testing.T) {
	var downloadCalled bool
	ps := &PeerSource{
		Downloader: func(ctx context.Context, info peer.AddrInfo, hash string, start, end int64) ([]byte, error) {
			downloadCalled = true
			return []byte("test"), nil
		},
	}

	if ps.Type() != SourceTypePeer {
		t.Errorf("Expected %s, got %s", SourceTypePeer, ps.Type())
	}

	data, err := ps.DownloadFull(context.Background(), "hash")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !downloadCalled {
		t.Error("Downloader was not called")
	}
	if string(data) != "test" {
		t.Errorf("Unexpected data: %s", data)
	}
}

func TestMirrorSource(t *testing.T) {
	var fetchCalled bool
	ms := &MirrorSource{
		URL: "http://example.com/test.deb",
		Fetcher: func(ctx context.Context, url string, start, end int64) ([]byte, error) {
			fetchCalled = true
			return []byte("mirror data"), nil
		},
	}

	if ms.Type() != SourceTypeMirror {
		t.Errorf("Expected %s, got %s", SourceTypeMirror, ms.Type())
	}

	if ms.ID() != "http://example.com/test.deb" {
		t.Errorf("Unexpected ID: %s", ms.ID())
	}

	data, err := ms.DownloadFull(context.Background(), "hash")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !fetchCalled {
		t.Error("Fetcher was not called")
	}
	if string(data) != "mirror data" {
		t.Errorf("Unexpected data: %s", data)
	}
}

func TestBtoi(t *testing.T) {
	if btoi(true) != 1 {
		t.Error("btoi(true) should be 1")
	}
	if btoi(false) != 0 {
		t.Error("btoi(false) should be 0")
	}
}

func TestDownloadResultSourceType(t *testing.T) {
	// Test source type determination
	tests := []struct {
		peerBytes   int64
		mirrorBytes int64
		expected    string
	}{
		{1000, 0, SourceTypePeer},
		{0, 1000, SourceTypeMirror},
		{500, 500, SourceTypeMixed},
	}

	for _, tt := range tests {
		sourceType := SourceTypeMixed
		if tt.peerBytes == 0 {
			sourceType = SourceTypeMirror
		} else if tt.mirrorBytes == 0 {
			sourceType = SourceTypePeer
		}

		if sourceType != tt.expected {
			t.Errorf("For peer=%d, mirror=%d: expected %s, got %s",
				tt.peerBytes, tt.mirrorBytes, tt.expected, sourceType)
		}
	}
}
