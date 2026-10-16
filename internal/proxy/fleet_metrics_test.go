package proxy

import (
	"context"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/fleet"
)

type staticPeers []peer.AddrInfo

func (p staticPeers) GetMDNSPeers() []peer.AddrInfo { return p }

type noCache struct{}

func (noCache) Has(string) bool { return false }

// The fleet gauges are set only by UpdateMetrics; the daemon used to refresh a
// subset of gauges itself and never called it, leaving these at zero.
func TestUpdateMetrics_FleetGauges(t *testing.T) {
	s := newTestServer(t)
	coord := fleet.New(nil, staticPeers{{ID: "peer-a"}, {ID: "peer-b"}}, noCache{}, zap.NewNop())
	t.Cleanup(func() { _ = coord.Close() })
	s.fleet = coord
	coord.NotifyFetching("inflighthash", 1000)

	s.UpdateMetrics()

	if got := s.metrics.FleetPeers.Value(); got != 2 {
		t.Errorf("FleetPeers = %v, want 2", got)
	}
	if got := s.metrics.FleetInFlight.Value(); got != 1 {
		t.Errorf("FleetInFlight = %v, want 1", got)
	}
}

// A package fetched from a LAN peer replaces a WAN download, so it must count
// toward the avoided-download metrics (which nothing used to increment).
func TestRecordFleetLANDownload_CountsAvoided(t *testing.T) {
	s := newTestServer(t)

	s.recordFleetLANDownload(4096)
	s.recordFleetLANDownload(1024)

	if got := s.metrics.FleetWANAvoided.Value(); got != 2 {
		t.Errorf("FleetWANAvoided = %d, want 2", got)
	}
	if got := s.metrics.FleetBytesAvoided.Value(); got != 5120 {
		t.Errorf("FleetBytesAvoided = %d, want 5120", got)
	}
	if got := s.GetStats().BytesFromP2P; got != 5120 {
		t.Errorf("BytesFromP2P = %d, want 5120", got)
	}
}

// Every fleet decision is counted under its action label.
func TestDownloadPackage_CountsFleetDecision(t *testing.T) {
	s := newTestServer(t)
	coord := fleet.New(nil, staticPeers{}, noCache{}, zap.NewNop()) // no fleet peers: fetch_wan
	t.Cleanup(func() { _ = coord.Close() })
	s.fleet = coord

	// A canceled context stops the download right after the fleet decision, so
	// the test never reaches the network.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hash := strings.Repeat("ab", 32)
	_, _ = s.downloadPackage(ctx, "http://deb.debian.org/debian/pool/main/h/hello/hello_1_all.deb", hash, 10, "hello_1_all.deb")

	if got := s.metrics.FleetCoordination.Values()["fetch_wan"]; got != 1 {
		t.Errorf(`FleetCoordination["fetch_wan"] = %d, want 1 (all: %v)`, got, s.metrics.FleetCoordination.Values())
	}
}
