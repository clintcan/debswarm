package connectivity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestModeString(t *testing.T) {
	tests := []struct {
		mode     Mode
		expected string
	}{
		{ModeOnline, "online"},
		{ModeLANOnly, "lan_only"},
		{ModeOffline, "offline"},
		{Mode(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.expected {
			t.Errorf("Mode(%d).String() = %q, want %q", tt.mode, got, tt.expected)
		}
	}
}

func TestNewMonitorDefaults(t *testing.T) {
	logger := zap.NewNop()
	m := NewMonitor(nil, logger)

	if m.checkInterval != 30*time.Second {
		t.Errorf("expected default checkInterval 30s, got %v", m.checkInterval)
	}
	if m.checkURL != "http://deb.debian.org/debian/" {
		t.Errorf("expected default checkURL, got %s", m.checkURL)
	}
	if m.checkTimeout != 5*time.Second {
		t.Errorf("expected default checkTimeout 5s, got %v", m.checkTimeout)
	}
	if m.GetMode() != ModeOnline {
		t.Errorf("expected default mode ModeOnline, got %v", m.GetMode())
	}
}

func TestNewMonitorStaticModes(t *testing.T) {
	logger := zap.NewNop()

	// Test lan_only mode
	m := NewMonitor(&Config{Mode: "lan_only"}, logger)
	if m.GetMode() != ModeLANOnly {
		t.Errorf("expected ModeLANOnly for lan_only config, got %v", m.GetMode())
	}

	// Test online_only mode
	m = NewMonitor(&Config{Mode: "online_only"}, logger)
	if m.GetMode() != ModeOnline {
		t.Errorf("expected ModeOnline for online_only config, got %v", m.GetMode())
	}
}

func TestCheckConnectivityOnline(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := zap.NewNop()
	m := NewMonitor(&Config{
		Mode:         "auto",
		CheckURL:     server.URL,
		CheckTimeout: 5 * time.Second,
	}, logger)

	ctx := context.Background()
	mode := m.checkConnectivity(ctx)
	if mode != ModeOnline {
		t.Errorf("expected ModeOnline when server is reachable, got %v", mode)
	}
}

func TestCheckConnectivityOffline(t *testing.T) {
	logger := zap.NewNop()
	m := NewMonitor(&Config{
		Mode:         "auto",
		CheckURL:     "http://localhost:1", // Invalid port, will fail
		CheckTimeout: 1 * time.Second,
	}, logger)

	ctx := context.Background()
	mode := m.checkConnectivity(ctx)
	if mode != ModeOffline {
		t.Errorf("expected ModeOffline when server is unreachable, got %v", mode)
	}
}

func TestCheckConnectivityLANOnly(t *testing.T) {
	logger := zap.NewNop()
	m := NewMonitor(&Config{
		Mode:         "auto",
		CheckURL:     "http://localhost:1", // Invalid port, will fail
		CheckTimeout: 1 * time.Second,
		GetMDNSPeerCount: func() int {
			return 3 // Simulate having mDNS peers
		},
	}, logger)

	ctx := context.Background()
	mode := m.checkConnectivity(ctx)
	if mode != ModeLANOnly {
		t.Errorf("expected ModeLANOnly when server unreachable but mDNS peers exist, got %v", mode)
	}
}

func TestForceMode(t *testing.T) {
	logger := zap.NewNop()
	var oldMode, newMode Mode
	modeChanged := false

	m := NewMonitor(&Config{
		Mode: "auto",
		OnModeChange: func(old, new Mode) {
			oldMode = old
			newMode = new
			modeChanged = true
		},
	}, logger)

	// Force to LAN only
	m.ForceMode(ModeLANOnly)

	if !modeChanged {
		t.Error("expected OnModeChange to be called")
	}
	if oldMode != ModeOnline {
		t.Errorf("expected old mode ModeOnline, got %v", oldMode)
	}
	if newMode != ModeLANOnly {
		t.Errorf("expected new mode ModeLANOnly, got %v", newMode)
	}
	if m.GetMode() != ModeLANOnly {
		t.Errorf("expected current mode ModeLANOnly, got %v", m.GetMode())
	}
}

func TestModeChangeCallback(t *testing.T) {
	// Create a server that we can shut down
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	logger := zap.NewNop()
	modeChanges := make([]Mode, 0)

	m := NewMonitor(&Config{
		Mode:         "auto",
		CheckURL:     server.URL,
		CheckTimeout: 1 * time.Second,
		OnModeChange: func(old, new Mode) {
			modeChanges = append(modeChanges, new)
		},
	}, logger)

	ctx := context.Background()

	// Initial check - should be online
	m.checkAndUpdate(ctx)
	if m.GetMode() != ModeOnline {
		t.Errorf("expected ModeOnline initially, got %v", m.GetMode())
	}

	// Close server to simulate going offline
	server.Close()

	// Check again - should change to offline
	m.checkAndUpdate(ctx)
	if m.GetMode() != ModeOffline {
		t.Errorf("expected ModeOffline after server close, got %v", m.GetMode())
	}

	// Verify callback was called
	if len(modeChanges) != 1 {
		t.Errorf("expected 1 mode change, got %d", len(modeChanges))
	}
}
