package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/debswarm/debswarm/internal/config"
)

// newTestWizard creates a wizard backed by simulated stdin lines.
func newTestWizard(lines ...string) (*wizard, *os.File) {
	input := strings.Join(lines, "\n") + "\n"
	r := strings.NewReader(input)
	// Discard output to avoid test noise
	devNull, _ := os.Open(os.DevNull)
	return &wizard{
		scanner: bufio.NewScanner(r),
		cfg:     config.DefaultConfig(),
		out:     devNull,
	}, devNull
}

func TestWizard_HomeProfile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.toml")

	// Inputs: profile=1(home), cache=enter, upload=enter, download=enter,
	// proxy port=enter, p2p port=enter, metrics port=enter,
	// mdns=enter(Y), fleet=enter(N), log level=enter(1=info), confirm=y
	w, f := newTestWizard(
		"1", // profile: home
		"",  // cache size: accept default 10GB
		"",  // upload rate: accept default
		"",  // download rate: accept default
		"",  // proxy port: accept default
		"",  // p2p port: accept default
		"",  // metrics port: accept default
		"",  // mdns: accept default (Y)
		"",  // fleet: accept default (N)
		"",  // log level: accept default (info)
		"y", // confirm save
	)
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	// Load saved config and verify
	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if cfg.Cache.MaxSize != "10GB" {
		t.Errorf("cache.max_size = %q, want %q", cfg.Cache.MaxSize, "10GB")
	}
	if cfg.Transfer.MaxUploadRate != "0" {
		t.Errorf("transfer.max_upload_rate = %q, want %q", cfg.Transfer.MaxUploadRate, "0")
	}
	if cfg.Fleet.Enabled {
		t.Errorf("fleet.enabled = true, want false for home profile")
	}
	if cfg.Metrics.Bind != "127.0.0.1" {
		t.Errorf("metrics.bind = %q, want %q", cfg.Metrics.Bind, "127.0.0.1")
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("logging.level = %q, want %q", cfg.Logging.Level, "info")
	}
}

func TestWizard_ServerProfile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.toml")

	w, f := newTestWizard(
		"2",     // profile: server
		"100GB", // custom cache size
		"",      // upload rate: accept default 50MB/s
		"",      // download rate: accept default
		"",      // proxy port: accept default
		"",      // p2p port: accept default
		"",      // metrics port: accept default
		"",      // mdns: accept default
		"",      // fleet: accept default (Y for server)
		"",      // log level: accept default
		"y",     // confirm save
	)
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if cfg.Cache.MaxSize != "100GB" {
		t.Errorf("cache.max_size = %q, want %q", cfg.Cache.MaxSize, "100GB")
	}
	if cfg.Transfer.MaxUploadRate != "50MB/s" {
		t.Errorf("transfer.max_upload_rate = %q, want %q", cfg.Transfer.MaxUploadRate, "50MB/s")
	}
	if !cfg.Fleet.Enabled {
		t.Errorf("fleet.enabled = false, want true for server profile")
	}
	if cfg.Metrics.Bind != "0.0.0.0" {
		t.Errorf("metrics.bind = %q, want %q", cfg.Metrics.Bind, "0.0.0.0")
	}
}

func TestWizard_PrivateSwarmProfile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.toml")

	w, f := newTestWizard(
		"3", // profile: private swarm
		"",  // cache size: accept default
		"",  // upload rate: accept default
		"",  // download rate: accept default
		"",  // proxy port: accept default
		"",  // p2p port: accept default
		"",  // metrics port: accept default
		"",  // mdns: accept default (Y)
		"n", // PSK generation: no (skip actual file write in test)
		"",  // fleet: accept default (N)
		"",  // log level: accept default
		"y", // confirm save
	)
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if cfg.Network.ConnectivityMode != "lan_only" {
		t.Errorf("network.connectivity_mode = %q, want %q", cfg.Network.ConnectivityMode, "lan_only")
	}
	if cfg.Metrics.Bind != "127.0.0.1" {
		t.Errorf("metrics.bind = %q, want %q", cfg.Metrics.Bind, "127.0.0.1")
	}
}

func TestWizard_CustomPorts(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.toml")

	w, f := newTestWizard(
		"1",    // profile: home
		"",     // cache size: accept default
		"",     // upload rate: accept default
		"",     // download rate: accept default
		"8080", // custom proxy port
		"5001", // custom p2p port
		"0",    // disable metrics
		"",     // mdns: accept default
		"",     // fleet: accept default
		"",     // log level: accept default
		"y",    // confirm save
	)
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if cfg.Network.ProxyPort != 8080 {
		t.Errorf("network.proxy_port = %d, want %d", cfg.Network.ProxyPort, 8080)
	}
	if cfg.Network.ListenPort != 5001 {
		t.Errorf("network.listen_port = %d, want %d", cfg.Network.ListenPort, 5001)
	}
	if cfg.Metrics.Port != 0 {
		t.Errorf("metrics.port = %d, want %d", cfg.Metrics.Port, 0)
	}
}

func TestWizard_InvalidCacheSizeThenValid(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.toml")

	// First cache size input is invalid, second is valid
	w, f := newTestWizard(
		"1",        // profile: home
		"notasize", // invalid cache size → re-prompt
		"20GB",     // valid cache size
		"",         // upload rate
		"",         // download rate
		"",         // proxy port
		"",         // p2p port
		"",         // metrics port
		"",         // mdns
		"",         // fleet
		"",         // log level
		"y",        // confirm
	)
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if cfg.Cache.MaxSize != "20GB" {
		t.Errorf("cache.max_size = %q, want %q", cfg.Cache.MaxSize, "20GB")
	}
}

func TestWizard_AbortSave(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.toml")

	w, f := newTestWizard(
		"1", // profile
		"",  // cache size
		"",  // upload rate
		"",  // download rate
		"",  // proxy port
		"",  // p2p port
		"",  // metrics port
		"",  // mdns
		"",  // fleet
		"",  // log level
		"n", // decline save
	)
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() should not error on abort: %v", err)
	}

	// Config file should NOT exist
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("config file should not exist after abort, but it does")
	}
}

func TestWizard_OutputFile(t *testing.T) {
	dir := t.TempDir()
	customPath := filepath.Join(dir, "subdir", "custom.toml")

	w, f := newTestWizard(
		"1", // profile
		"",  // cache size
		"",  // upload rate
		"",  // download rate
		"",  // proxy port
		"",  // p2p port
		"",  // metrics port
		"",  // mdns
		"",  // fleet
		"",  // log level
		"y", // confirm
	)
	defer f.Close()

	if err := w.run(customPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	if _, err := os.Stat(customPath); os.IsNotExist(err) {
		t.Errorf("config file should exist at custom path %s", customPath)
	}
}

func TestWizard_DebugLogLevel(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "config.toml")

	w, f := newTestWizard(
		"1", // profile
		"",  // cache size
		"",  // upload rate
		"",  // download rate
		"",  // proxy port
		"",  // p2p port
		"",  // metrics port
		"",  // mdns
		"",  // fleet
		"2", // log level: debug
		"y", // confirm
	)
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if cfg.Logging.Level != "debug" {
		t.Errorf("logging.level = %q, want %q", cfg.Logging.Level, "debug")
	}
}
