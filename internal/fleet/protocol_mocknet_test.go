package fleet

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"go.uber.org/zap"
)

// End-to-end send over an in-memory mocknet link, exercising getOrCreateStream
// (create then cached), SendMessage, peerStream.send, and Close without a real
// network or a full coordinator.
func TestProtocol_SendMessage_EndToEnd(t *testing.T) {
	mn := mocknet.New()
	defer func() { _ = mn.Close() }()

	hostA, err := mn.GenPeer()
	if err != nil {
		t.Fatalf("GenPeer A: %v", err)
	}
	hostB, err := mn.GenPeer()
	if err != nil {
		t.Fatalf("GenPeer B: %v", err)
	}
	if err := mn.LinkAll(); err != nil {
		t.Fatalf("LinkAll: %v", err)
	}
	if err := mn.ConnectAllButSelf(); err != nil {
		t.Fatalf("ConnectAllButSelf: %v", err)
	}

	// Receiver decodes incoming fleet messages onto a channel.
	received := make(chan Message, 8)
	hostB.SetStreamHandler(ProtocolID, func(s network.Stream) {
		r := bufio.NewReader(s)
		for {
			var m Message
			if err := m.Decode(r); err != nil {
				return
			}
			received <- m
		}
	})

	// Construct the Protocol directly (no coordinator needed for the send path).
	p := &Protocol{host: hostA, logger: zap.NewNop(), streams: make(map[peer.ID]*peerStream)}
	defer func() { _ = p.Close() }()

	msg := &Message{Type: MsgWantPackage, Nonce: 9, Hash: strings.Repeat("b", 64), Size: 4242}

	// Two sends: the first creates and caches the stream, the second reuses it.
	for i := range 2 {
		if err := p.SendMessage(context.Background(), hostB.ID(), msg); err != nil {
			t.Fatalf("SendMessage #%d: %v", i, err)
		}
	}

	for i := range 2 {
		select {
		case got := <-received:
			if got.Type != msg.Type || got.Hash != msg.Hash || got.Nonce != msg.Nonce {
				t.Errorf("received wrong message: %+v", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for message #%d", i)
		}
	}

	// Exactly one stream should be cached for the peer (the second send reused it).
	p.streamsMu.RLock()
	n := len(p.streams)
	p.streamsMu.RUnlock()
	if n != 1 {
		t.Errorf("expected 1 cached stream, got %d", n)
	}
}
