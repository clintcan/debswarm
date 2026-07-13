package mirror

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
)

func stallTestFetcher(window time.Duration) *Fetcher {
	return NewFetcher(&Config{
		Timeout:    window,
		MaxRetries: 1,
		UserAgent:  "debswarm-test",
	}, zap.NewNop())
}

// TestFetch_SlowButSteadyTransferSurvives verifies the timeout semantics
// change: a transfer that keeps making progress must complete even when its
// total duration far exceeds the configured timeout. The old whole-request
// client timeout killed exactly this case (a large package on a slow link)
// mid-body and re-downloaded from byte zero on every retry.
func TestFetch_SlowButSteadyTransferSurvives(t *testing.T) {
	const window = 300 * time.Millisecond
	payload := []byte("0123456789abcdef")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		// Trickle one byte at a time; total duration ~1.6s >> 300ms window.
		for i := range payload {
			_, _ = w.Write(payload[i : i+1])
			fl.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()

	f := stallTestFetcher(window)
	data, err := f.Fetch(context.Background(), srv.URL+"/slow")
	if err != nil {
		t.Fatalf("Fetch of a slow-but-steady transfer failed: %v (whole-request timeout regression?)", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("payload mismatch: got %q", data)
	}
}

// TestStream_StalledTransferAborts verifies the other half of the semantics:
// a transfer that stops making progress is aborted after the stall window
// instead of hanging indefinitely (there is no whole-request timeout anymore,
// so the stall guard is the only protection).
func TestStream_StalledTransferAborts(t *testing.T) {
	const window = 300 * time.Millisecond
	hang := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		fl.Flush()
		// Never send the rest; return when the test ends or the client aborts
		// (the stall guard cancels the request), so srv.Close can finish.
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(hang) // declared after srv.Close so it runs first (LIFO)

	f := stallTestFetcher(window)
	body, _, err := f.Stream(context.Background(), srv.URL+"/stall")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer body.Close()

	start := time.Now()
	_, err = io.ReadAll(body)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("reading a stalled body succeeded, want abort")
	}
	// Must abort near the stall window — generous upper bound for CI jitter.
	if elapsed > 5*time.Second {
		t.Errorf("stalled read took %v to abort, want ~%v", elapsed, window)
	}
}

// TestStreamConditional_NotModified verifies 304 handling and validator relay.
func TestStreamConditional_NotModified(t *testing.T) {
	const lastMod = "Mon, 01 Jan 2026 00:00:00 GMT"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Modified-Since") == lastMod {
			w.Header().Set("Last-Modified", lastMod)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Last-Modified", lastMod)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fresh content"))
	}))
	defer srv.Close()

	f := stallTestFetcher(time.Second)

	// Unconditional: full body plus validators.
	res, err := f.StreamConditional(context.Background(), srv.URL+"/f", "", "")
	if err != nil {
		t.Fatalf("StreamConditional: %v", err)
	}
	if res.NotModified || res.Body == nil {
		t.Fatal("unconditional fetch must return a body")
	}
	data, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(data) != "fresh content" {
		t.Fatalf("body = %q", data)
	}
	if res.LastModified != lastMod {
		t.Errorf("LastModified = %q, want %q", res.LastModified, lastMod)
	}

	// Conditional: 304, no body.
	res, err = f.StreamConditional(context.Background(), srv.URL+"/f", lastMod, "")
	if err != nil {
		t.Fatalf("StreamConditional (revalidate): %v", err)
	}
	if !res.NotModified {
		t.Fatal("expected NotModified for matching If-Modified-Since")
	}
	if res.Body != nil {
		t.Fatal("304 result must not carry a body")
	}
	if res.LastModified != lastMod {
		t.Errorf("304 LastModified = %q, want %q", res.LastModified, lastMod)
	}
}
