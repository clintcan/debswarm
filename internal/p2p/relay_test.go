package p2p

import (
	"testing"
	"time"

	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"
)

func TestParseRelayPeers(t *testing.T) {
	logger := zap.NewNop()

	const validRelay = "/ip4/203.0.113.10/udp/4001/quic-v1/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"

	t.Run("parses a full relay multiaddr", func(t *testing.T) {
		got := ParseRelayPeers([]string{validRelay}, logger)
		if len(got) != 1 {
			t.Fatalf("expected 1 relay, got %d", len(got))
		}
		if len(got[0].Addrs) == 0 {
			t.Error("expected the relay AddrInfo to carry an address")
		}
		if got[0].ID == "" {
			t.Error("expected the relay AddrInfo to carry a peer ID")
		}
	})

	t.Run("skips an address with no /p2p peer ID", func(t *testing.T) {
		// A relay address without a peer ID cannot be reserved on.
		got := ParseRelayPeers([]string{"/ip4/203.0.113.10/udp/4001/quic-v1"}, logger)
		if len(got) != 0 {
			t.Errorf("expected peer-ID-less address to be skipped, got %v", got)
		}
	})

	t.Run("skips garbage but keeps the good entries", func(t *testing.T) {
		// One bad line in a config must not take the node offline.
		got := ParseRelayPeers([]string{"not-a-multiaddr", validRelay, ""}, logger)
		if len(got) != 1 {
			t.Fatalf("expected the one valid relay to survive, got %d", len(got))
		}
	})

	t.Run("empty input yields no relays", func(t *testing.T) {
		if got := ParseRelayPeers(nil, logger); len(got) != 0 {
			t.Errorf("expected no relays, got %v", got)
		}
	})
}

func TestRelayServiceMode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", RelayServiceAuto},
		{"auto", RelayServiceAuto},
		{"on", RelayServiceOn},
		{"off", RelayServiceOff},
		{"ON", RelayServiceOn},
		{"  Off  ", RelayServiceOff},
		{"nonsense", RelayServiceAuto}, // degrade to the safe default, never silently "off"
	}
	for _, tc := range tests {
		if got := relayServiceMode(tc.in); got != tc.want {
			t.Errorf("relayServiceMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRelayResourcesFrom(t *testing.T) {
	t.Run("zero config keeps circuit-v2 defaults", func(t *testing.T) {
		res := relayResourcesFrom(&Config{})
		if res.MaxReservations <= 0 || res.MaxCircuits <= 0 {
			t.Errorf("expected libp2p defaults to survive, got %+v", res)
		}
		if res.Limit == nil {
			t.Fatal("expected a default relay limit")
		}
	})

	t.Run("applies configured bounds", func(t *testing.T) {
		res := relayResourcesFrom(&Config{
			RelayMaxReservations: 7,
			RelayMaxCircuits:     3,
			RelayBufferSize:      4096,
			RelayDuration:        90 * time.Second,
		})
		if res.MaxReservations != 7 {
			t.Errorf("MaxReservations = %d, want 7", res.MaxReservations)
		}
		if res.MaxCircuits != 3 {
			t.Errorf("MaxCircuits = %d, want 3", res.MaxCircuits)
		}
		if res.Limit.Data != 4096 {
			t.Errorf("Limit.Data = %d, want 4096", res.Limit.Data)
		}
		if res.Limit.Duration != 90*time.Second {
			t.Errorf("Limit.Duration = %v, want 90s", res.Limit.Duration)
		}
	})

	// relay.DefaultResources() hands back a struct holding a *RelayLimit. Writing
	// through that pointer would corrupt the default for every later caller in the
	// process, so relayResourcesFrom must copy it.
	t.Run("does not mutate the shared default limit", func(t *testing.T) {
		first := relayResourcesFrom(&Config{RelayBufferSize: 1234, RelayDuration: time.Second})
		second := relayResourcesFrom(&Config{}) // should still see pristine defaults

		if second.Limit.Data == 1234 {
			t.Error("configuring one node's relay limit leaked into the shared default")
		}
		if second.Limit.Duration == time.Second {
			t.Error("configuring one node's relay duration leaked into the shared default")
		}
		if first.Limit.Data != 1234 {
			t.Errorf("first config lost its own limit: %d", first.Limit.Data)
		}
	})
}

func TestIsCircuitAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{
			name: "circuit address",
			addr: "/ip4/203.0.113.10/udp/4001/quic-v1/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN/p2p-circuit",
			want: true,
		},
		{
			name: "plain quic address",
			addr: "/ip4/203.0.113.10/udp/4001/quic-v1",
			want: false,
		},
		{
			name: "plain tcp address",
			addr: "/ip4/192.168.1.5/tcp/4001",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ma, err := multiaddr.NewMultiaddr(tc.addr)
			if err != nil {
				t.Fatalf("bad test multiaddr %q: %v", tc.addr, err)
			}
			if got := isCircuitAddr(ma); got != tc.want {
				t.Errorf("isCircuitAddr(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}
