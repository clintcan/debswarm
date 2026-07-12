package p2p

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// stallingReader serves a small prefix of data and then blocks until the gate
// channel is closed, simulating a peer mid-way through a large upload.
type stallingReader struct {
	prefix []byte
	sent   bool
	gate   chan struct{}
}

func (r *stallingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, r.prefix)
		return n, nil
	}
	<-r.gate
	return 0, io.EOF
}

func (r *stallingReader) Close() error { return nil }

// TestNode_Download_CancelResetsStream verifies that cancelling a download's
// context unblocks the transfer immediately (via stream reset) instead of the
// read continuing until the size-based deadline. This is what stops racing
// losers from downloading the entire file after another source has won.
func TestNode_Download_CancelResetsStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := newTestLogger()

	cfg1 := newTestConfig(t)
	node1, err := New(ctx, cfg1, logger)
	if err != nil {
		t.Fatalf("New node1 failed: %v", err)
	}
	defer node1.Close()

	cfg2 := newTestConfig(t)
	node2, err := New(ctx, cfg2, logger)
	if err != nil {
		t.Fatalf("New node2 failed: %v", err)
	}
	defer node2.Close()

	// node1 advertises a large transfer (so the size-based stream deadline is
	// far away) but stalls after the first KB — the downloader will be blocked
	// mid-read when we cancel.
	testHash := "b1b2c3d4e5f67890123456789012345678901234567890123456789012abcdef"
	const advertisedSize = 64 * 1024 * 1024
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) }) // unblock the server-side copy at test end

	node1.SetContentGetter(func(hash string) (io.ReadCloser, int64, error) {
		if hash == testHash {
			return &stallingReader{prefix: make([]byte, 1024), gate: gate}, advertisedSize, nil
		}
		return nil, 0, io.EOF
	})

	node1Info := peer.AddrInfo{
		ID:    node1.PeerID(),
		Addrs: node1.Addrs(),
	}
	if err := node2.host.Connect(ctx, node1Info); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	dlCtx, dlCancel := context.WithCancel(ctx)
	defer dlCancel()

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, err := node2.Download(dlCtx, node1Info, testHash)
		done <- result{err: err}
	}()

	// Let the transfer get in flight (size header + first KB over localhost),
	// then cancel — as the racing path does when another source wins.
	time.Sleep(500 * time.Millisecond)
	cancelled := time.Now()
	dlCancel()

	select {
	case res := <-done:
		if elapsed := time.Since(cancelled); elapsed > 3*time.Second {
			t.Errorf("Download returned %v after cancel; want prompt return", elapsed)
		}
		if res.err == nil {
			t.Fatal("Download succeeded, want cancellation error")
		}
		if !errors.Is(res.err, context.Canceled) {
			t.Errorf("Download error = %v, want context.Canceled", res.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Download did not return within 10s of cancellation — cancelled loser kept transferring")
	}
}
