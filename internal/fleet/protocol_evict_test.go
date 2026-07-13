package fleet

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// failingStream is a network.Stream whose Write always errors, simulating a dead
// stream. Only Write, Reset, and SetWriteDeadline are exercised.
type failingStream struct {
	network.Stream
	resetCalled atomic.Bool
}

func (f *failingStream) Write(_ []byte) (int, error)        { return 0, errors.New("stream reset by peer") }
func (f *failingStream) Reset() error                       { f.resetCalled.Store(true); return nil }
func (f *failingStream) SetWriteDeadline(_ time.Time) error { return nil }

// When a send fails, the dead stream must be evicted from the cache and reset, so
// the next send dials a fresh stream instead of reusing the broken one forever.
// Regression for the bug where a single transient error made a peer permanently
// unreachable for fleet coordination.
func TestSendMessage_EvictsDeadStream(t *testing.T) {
	fs := &failingStream{}
	pid := peer.ID("peer-dead")
	p := &Protocol{
		logger:  zap.NewNop(),
		streams: map[peer.ID]*peerStream{pid: {s: fs}},
	}

	err := p.SendMessage(context.Background(), pid, &Message{Type: MsgWantPackage, Hash: strings.Repeat("a", 64)})
	if err == nil {
		t.Fatal("expected SendMessage to return the send error")
	}

	p.streamsMu.RLock()
	_, stillCached := p.streams[pid]
	p.streamsMu.RUnlock()
	if stillCached {
		t.Error("dead stream was not evicted from the cache")
	}
	if !fs.resetCalled.Load() {
		t.Error("dead stream was not reset")
	}
}

// evictStream must not remove a newer stream that a concurrent getOrCreateStream
// installed for the same peer.
func TestEvictStream_KeepsNewerStream(t *testing.T) {
	pid := peer.ID("peer-live")
	stale := &peerStream{s: &failingStream{}}
	current := &peerStream{s: &failingStream{}}
	p := &Protocol{logger: zap.NewNop(), streams: map[peer.ID]*peerStream{pid: current}}

	p.evictStream(pid, stale) // stale is not the cached stream; must be a no-op for the map

	p.streamsMu.RLock()
	got := p.streams[pid]
	p.streamsMu.RUnlock()
	if got != current {
		t.Error("evictStream removed the newer stream when evicting a stale one")
	}
}
