package downloader

import (
	"context"
	"errors"
	"testing"
	"time"
)

// After a chunked download fails, the persisted state must carry the mirror URL:
// proxy.checkAndRetryFailedDownloads skips any failed download whose stored URL is
// empty, so an absent URL silently disables retries for that download.
func TestFailedDownloadPersistsMirrorURL(t *testing.T) {
	chunkSize := int64(1024)
	data := testData(int(chunkSize) * 4)
	hash := hashBytes(data)

	db := setupTestDB(t)
	defer db.Close()

	cache := &mockPartialCache{baseDir: t.TempDir()}
	stateManager := NewStateManager(db)

	// A peer source is required to enter the chunked path; make it fail.
	failingPeer := &mockSource{
		id:           "peer1",
		sourceType:   SourceTypePeer,
		err:          errors.New("peer unavailable"),
		rangeSupport: true,
	}

	const mirrorURL = "http://deb.debian.org/debian/pool/main/h/hello/hello_2.10-2_amd64.deb"
	failingMirror := &MirrorSource{
		URL: mirrorURL,
		Fetcher: func(ctx context.Context, url string, start, end int64) ([]byte, error) {
			return nil, errors.New("mirror unreachable")
		},
	}

	d := New(&Config{
		ChunkSize:      chunkSize,
		MaxConcurrent:  2,
		StateManager:   stateManager,
		Cache:          cache,
		MinChunkedSize: 1,
	})

	_, err := d.Download(context.Background(), hash, int64(len(data)), []Source{failingPeer}, failingMirror)
	if err == nil {
		t.Fatal("expected the download to fail")
	}

	// This is exactly what the retry worker queries.
	retryable, err := stateManager.GetRetryableDownloads(3, time.Hour)
	if err != nil {
		t.Fatalf("GetRetryableDownloads: %v", err)
	}
	if len(retryable) == 0 {
		t.Fatal("expected the failed download to be retryable")
	}

	state := retryable[0]
	t.Logf("retry worker sees: id=%s... url=%q", state.ID[:12], state.URL)

	if state.URL == "" {
		t.Fatalf("persisted URL is empty, so proxy.checkAndRetryFailedDownloads "+
			"will skip this download and it will never be retried (want %q)", mirrorURL)
	}
	if state.URL != mirrorURL {
		t.Errorf("persisted URL = %q, want %q", state.URL, mirrorURL)
	}
}
