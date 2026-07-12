package fleet

import (
	"bufio"
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// concurrencyStream is a network.Stream whose Write detects overlapping calls and
// records everything written. Message.Encode only ever calls Write, so the other
// interface methods (promoted from the embedded nil interface) are never touched.
type concurrencyStream struct {
	network.Stream
	active   atomic.Int32
	overlaps atomic.Int32
	mu       sync.Mutex
	buf      bytes.Buffer
}

func (m *concurrencyStream) Write(p []byte) (int, error) {
	if m.active.Add(1) > 1 {
		m.overlaps.Add(1)
	}
	time.Sleep(50 * time.Microsecond) // widen the interleaving window
	m.active.Add(-1)

	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}

// Concurrent sends on one shared stream must be serialized: Message.Encode issues
// several Writes, and interleaving two of them corrupts the length-prefixed
// framing. Regression for the data race where SendMessage encoded onto a shared
// cached stream with no per-stream lock.
func TestPeerStream_SerializesConcurrentSends(t *testing.T) {
	ms := &concurrencyStream{}
	ps := &peerStream{s: ms}

	const senders = 50
	msg := &Message{Type: MsgWantPackage, Nonce: 7, Hash: strings.Repeat("a", 64), Size: 12345}

	var wg sync.WaitGroup
	for range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ps.send(msg); err != nil {
				t.Errorf("send: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := ms.overlaps.Load(); got != 0 {
		t.Errorf("detected %d overlapping writes; concurrent sends are not serialized", got)
	}

	// The buffer must hold exactly `senders` cleanly-framed, decodable messages.
	r := bufio.NewReader(&ms.buf)
	for i := range senders {
		var decoded Message
		if err := decoded.Decode(r); err != nil {
			t.Fatalf("message %d failed to decode (corrupted framing): %v", i, err)
		}
		if decoded.Type != msg.Type || decoded.Hash != msg.Hash || decoded.Nonce != msg.Nonce {
			t.Fatalf("message %d decoded wrong: %+v", i, decoded)
		}
	}
	if _, err := r.ReadByte(); err == nil {
		t.Error("unexpected trailing bytes after decoding all messages")
	}
}
