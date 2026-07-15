package p2p

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// bytesContentGetter serves a fixed payload for a single hash, and reports
// "not found" (io.EOF) for anything else — enough to back a transfer in tests.
func bytesContentGetter(wantHash string, payload []byte) ContentGetter {
	return func(hash string) (io.ReadCloser, int64, error) {
		if hash == wantHash {
			return io.NopCloser(bytes.NewReader(payload)), int64(len(payload)), nil
		}
		return nil, 0, io.EOF
	}
}

// TestRelayedTransferSkipped covers the pre-stream gate: a relay-only peer is
// skipped exactly when relayed transfers are disabled (cap <= 0), and a direct
// path is never skipped regardless of the cap. This is the policy that keeps the
// default (cap 0) behaving like the pre-feature build — a relay only coordinates
// hole punches, never carries bytes.
func TestRelayedTransferSkipped(t *testing.T) {
	tests := []struct {
		name     string
		relayed  bool
		maxBytes int64
		want     bool
	}{
		{"direct, disabled", false, 0, false},
		{"direct, enabled", false, 1 << 18, false},
		{"relayed, disabled (default)", true, 0, true},
		{"relayed, negative cap", true, -1, true},
		{"relayed, enabled", true, 1 << 18, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relayedTransferSkipped(tt.relayed, tt.maxBytes); got != tt.want {
				t.Errorf("relayedTransferSkipped(%v, %d) = %v, want %v", tt.relayed, tt.maxBytes, got, tt.want)
			}
		})
	}
}

// TestRelayedSizeExceeded covers the post-size gate: a relayed transfer is refused
// only when its size exceeds the cap, the boundary is inclusive, and a direct
// transfer is never bounded by the cap.
func TestRelayedSizeExceeded(t *testing.T) {
	const cap = 256 * 1024
	tests := []struct {
		name     string
		relayed  bool
		size     int64
		maxBytes int64
		want     bool
	}{
		{"direct, huge size", false, 1 << 30, cap, false},
		{"relayed, under cap", true, cap - 1, cap, false},
		{"relayed, at cap (inclusive)", true, cap, cap, false},
		{"relayed, one over cap", true, cap + 1, cap, true},
		{"relayed, far over cap", true, 10 << 20, cap, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relayedSizeExceeded(tt.relayed, tt.size, tt.maxBytes); got != tt.want {
				t.Errorf("relayedSizeExceeded(%v, %d, %d) = %v, want %v", tt.relayed, tt.size, tt.maxBytes, got, tt.want)
			}
		})
	}
}

// TestOnlyRelayedConn_DirectConnection verifies that onlyRelayedConn reports false
// for a peer reached over a normal direct connection (the localhost case), and
// false when there is no connection at all. The true case — an all-relayed path —
// requires a live circuit relay and is covered end-to-end by the NAT test rig
// (test/nat), which asserts a completed relayed transfer.
func TestOnlyRelayedConn_DirectConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := newTestLogger()

	node1, err := New(ctx, newTestConfig(t), logger)
	if err != nil {
		t.Fatalf("New node1 failed: %v", err)
	}
	defer node1.Close()

	node2, err := New(ctx, newTestConfig(t), logger)
	if err != nil {
		t.Fatalf("New node2 failed: %v", err)
	}
	defer node2.Close()

	// No connection yet: not "relay-only", it's "no path".
	if node2.onlyRelayedConn(node1.PeerID()) {
		t.Error("onlyRelayedConn = true with no connection, want false")
	}

	node1Info := peer.AddrInfo{ID: node1.PeerID(), Addrs: node1.Addrs()}
	if err := node2.host.Connect(ctx, node1Info); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// A direct localhost connection must never be classified as relay-only.
	if node2.onlyRelayedConn(node1.PeerID()) {
		t.Error("onlyRelayedConn = true for a direct connection, want false")
	}
}

// TestDownloadRange_DirectUnaffectedByRelayCap verifies that enabling the relayed
// transfer cap does not change a normal direct transfer: it still succeeds and is
// accounted as a peer download, not a relay download.
func TestDownloadRange_DirectUnaffectedByRelayCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := newTestLogger()

	serverCfg := newTestConfig(t)
	server, err := New(ctx, serverCfg, logger)
	if err != nil {
		t.Fatalf("New server failed: %v", err)
	}
	defer server.Close()

	testHash := "aa11bb22cc33dd44ee55ff66778899001122334455667788990011223344abcd"
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i)
	}
	server.SetContentGetter(bytesContentGetter(testHash, payload))

	// Client with the relayed-transfer cap enabled. The connection below is direct,
	// so the cap must not apply.
	clientCfg := newTestConfig(t)
	clientCfg.RelayedTransferMax = 1 // deliberately tiny; must be ignored for direct
	client, err := New(ctx, clientCfg, logger)
	if err != nil {
		t.Fatalf("New client failed: %v", err)
	}
	defer client.Close()

	serverInfo := peer.AddrInfo{ID: server.PeerID(), Addrs: server.Addrs()}
	if err := client.host.Connect(ctx, serverInfo); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	data, err := client.Download(ctx, serverInfo, testHash)
	if err != nil {
		t.Fatalf("Download over direct connection failed with cap set: %v", err)
	}
	if len(data) != len(payload) {
		t.Fatalf("Download returned %d bytes, want %d", len(data), len(payload))
	}
	if got := client.metrics.BytesFromRelay.Value(); got != 0 {
		t.Errorf("BytesFromRelay = %d after a direct transfer, want 0", got)
	}
}
