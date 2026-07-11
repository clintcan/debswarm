package main

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
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
		"",  // trust known repos: accept default (Y)
		"",  // additional repo hosts: none
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
		"",      // trust known repos: accept default (Y)
		"",      // additional repo hosts: none
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
		"",  // trust known repos: accept default (Y)
		"",  // additional repo hosts: none
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
		"",     // trust known repos: accept default (Y)
		"",     // additional repo hosts: none
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
	// Guards against input misalignment: the home profile leaves fleet off, so a
	// shifted answer sequence would show up here as fleet being unexpectedly on.
	if cfg.Fleet.Enabled {
		t.Error("fleet.enabled = true, want false — wizard input sequence is misaligned")
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
		"",         // trust known repos: accept default (Y)
		"",         // additional repo hosts: none
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
		"",  // trust known repos: accept default (Y)
		"",  // additional repo hosts: none
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
		"",  // trust known repos: accept default (Y)
		"",  // additional repo hosts: none
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
		"",  // trust known repos: accept default (Y)
		"",  // additional repo hosts: none
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

// repoWizardInputs builds a default answer sequence with the repository step
// (trust, hosts) substituted in, so the repo tests stay readable.
func repoWizardInputs(trust, hosts string) []string {
	return []string{
		"1",   // profile: home
		"",    // cache size
		"",    // upload rate
		"",    // download rate
		"",    // proxy port
		"",    // p2p port
		"",    // metrics port
		trust, // Step 5a: trust known repos
		hosts, // Step 5b: additional repo hosts
		"",    // mdns
		"",    // fleet
		"",    // log level
		"y",   // confirm save
	}
}

func TestWizard_Repositories_DefaultTrusts(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "config.toml")

	w, f := newTestWizard(repoWizardInputs("", "")...) // accept defaults
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if !cfg.Proxy.TrustsKnownRepos() {
		t.Error("trust_known_repos should default to true")
	}
	// The curated defaults must survive the wizard round trip.
	if len(cfg.Proxy.EffectiveAllowedHosts()) != len(config.DefaultTrustedRepos) {
		t.Errorf("effective allowed hosts = %d, want %d curated defaults",
			len(cfg.Proxy.EffectiveAllowedHosts()), len(config.DefaultTrustedRepos))
	}
	if !slices.Contains(cfg.Proxy.EffectiveHTTPSUpstreamHosts(), "pkgs.k8s.io") {
		t.Errorf("HTTPS-upstream defaults lost: %v", cfg.Proxy.EffectiveHTTPSUpstreamHosts())
	}
}

func TestWizard_Repositories_DeclineTrust(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "config.toml")

	w, f := newTestWizard(repoWizardInputs("n", "")...)
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if cfg.Proxy.TrustsKnownRepos() {
		t.Error("trust_known_repos should be false after declining")
	}
	if len(cfg.Proxy.EffectiveAllowedHosts()) != 0 {
		t.Errorf("declining trust should leave no extra hosts, got %v", cfg.Proxy.EffectiveAllowedHosts())
	}
	// Declining also drops the curated HTTPS-upstream defaults.
	if len(cfg.Proxy.EffectiveHTTPSUpstreamHosts()) != 0 {
		t.Errorf("declining trust should drop HTTPS-upstream defaults, got %v",
			cfg.Proxy.EffectiveHTTPSUpstreamHosts())
	}
}

func TestWizard_Repositories_CustomHosts(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "config.toml")

	w, f := newTestWizard(repoWizardInputs("", " packages.gitlab.com , my-mirror.example.com ,")...)
	defer f.Close()

	if err := w.run(outPath); err != nil {
		t.Fatalf("wizard.run() failed: %v", err)
	}

	cfg, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	want := []string{"packages.gitlab.com", "my-mirror.example.com"}
	if !slices.Equal(cfg.Proxy.AllowedHosts, want) {
		t.Errorf("allowed_hosts = %v, want %v (trimmed, blanks dropped)", cfg.Proxy.AllowedHosts, want)
	}
	// User hosts come first, then the curated defaults.
	if !slices.Contains(cfg.Proxy.EffectiveAllowedHosts(), "download.docker.com") {
		t.Error("curated defaults should still be merged in alongside custom hosts")
	}
}

func TestParseHostList(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",,", nil},
		{"a.example.com", []string{"a.example.com"}},
		{" a.example.com , b.example.com ", []string{"a.example.com", "b.example.com"}},
		{"a.example.com,,b.example.com,", []string{"a.example.com", "b.example.com"}},
	}
	for _, tc := range tests {
		if got := parseHostList(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("parseHostList(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
