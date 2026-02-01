package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}

	// Check network defaults
	if cfg.Network.ListenPort != 4001 {
		t.Errorf("ListenPort = %d, want 4001", cfg.Network.ListenPort)
	}
	if cfg.Network.ProxyPort != 9977 {
		t.Errorf("ProxyPort = %d, want 9977", cfg.Network.ProxyPort)
	}
	if cfg.Network.MaxConnections != 100 {
		t.Errorf("MaxConnections = %d, want 100", cfg.Network.MaxConnections)
	}
	if len(cfg.Network.BootstrapPeers) != 4 {
		t.Errorf("BootstrapPeers count = %d, want 4", len(cfg.Network.BootstrapPeers))
	}

	// Check cache defaults
	if cfg.Cache.MaxSize != "10GB" {
		t.Errorf("Cache.MaxSize = %s, want 10GB", cfg.Cache.MaxSize)
	}

	// Check transfer defaults
	if cfg.Transfer.MaxConcurrentUploads != 20 {
		t.Errorf("MaxConcurrentUploads = %d, want 20", cfg.Transfer.MaxConcurrentUploads)
	}

	// Check DHT defaults
	if cfg.DHT.ProviderTTLDuration() != 24*time.Hour {
		t.Errorf("ProviderTTL = %v, want 24h", cfg.DHT.ProviderTTLDuration())
	}

	// Check privacy defaults
	if !cfg.Privacy.EnableMDNS {
		t.Error("EnableMDNS should be true by default")
	}
	if !cfg.Privacy.AnnouncePackages {
		t.Error("AnnouncePackages should be true by default")
	}

	// Check logging defaults
	if cfg.Logging.Level != "info" {
		t.Errorf("Logging.Level = %s, want info", cfg.Logging.Level)
	}
}

func TestLoad_NonexistentFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/config.toml")
	if err != nil {
		t.Fatalf("Load should not error for nonexistent file: %v", err)
	}

	// Should return defaults
	if cfg.Network.ListenPort != 4001 {
		t.Error("Should return default config for nonexistent file")
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	configContent := `
[network]
listen_port = 5001
proxy_port = 8080
max_connections = 50

[cache]
max_size = "5GB"

[logging]
level = "debug"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Network.ListenPort != 5001 {
		t.Errorf("ListenPort = %d, want 5001", cfg.Network.ListenPort)
	}
	if cfg.Network.ProxyPort != 8080 {
		t.Errorf("ProxyPort = %d, want 8080", cfg.Network.ProxyPort)
	}
	if cfg.Cache.MaxSize != "5GB" {
		t.Errorf("MaxSize = %s, want 5GB", cfg.Cache.MaxSize)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Level = %s, want debug", cfg.Logging.Level)
	}
}

func TestLoad_InvalidTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	if err := os.WriteFile(configPath, []byte("invalid toml [[["), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Error("Load should fail with invalid TOML")
	}
}

func TestConfig_Save(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "subdir", "config.toml")

	cfg := DefaultConfig()
	cfg.Network.ListenPort = 6001
	cfg.Logging.Level = "warn"

	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("Save did not create file")
	}

	// Load it back
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Network.ListenPort != 6001 {
		t.Errorf("ListenPort = %d, want 6001", loaded.Network.ListenPort)
	}
	if loaded.Logging.Level != "warn" {
		t.Errorf("Level = %s, want warn", loaded.Logging.Level)
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"0", 0},
		{"100", 100},
		{"1KB", 1024},
		{"1K", 1024},
		{"10KB", 10 * 1024},
		{"1MB", 1024 * 1024},
		{"1M", 1024 * 1024},
		{"100MB", 100 * 1024 * 1024},
		{"1GB", 1024 * 1024 * 1024},
		{"1G", 1024 * 1024 * 1024},
		{"10GB", 10 * 1024 * 1024 * 1024},
		{"1TB", 1024 * 1024 * 1024 * 1024},
		{"1T", 1024 * 1024 * 1024 * 1024},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result, err := ParseSize(tc.input)
			if err != nil {
				t.Fatalf("ParseSize(%q) error: %v", tc.input, err)
			}
			if result != tc.expected {
				t.Errorf("ParseSize(%q) = %d, want %d", tc.input, result, tc.expected)
			}
		})
	}
}

func TestParseRate(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"", 0},          // unlimited
		{"0", 0},         // unlimited
		{"unlimited", 0}, // unlimited
		{"1MB/s", 1024 * 1024},
		{"10MB/s", 10 * 1024 * 1024},
		{"100KB/s", 100 * 1024},
		{"1GB/s", 1024 * 1024 * 1024},
		{"50MB", 50 * 1024 * 1024}, // without /s
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result, err := ParseRate(tc.input)
			if err != nil {
				t.Fatalf("ParseRate(%q) error: %v", tc.input, err)
			}
			if result != tc.expected {
				t.Errorf("ParseRate(%q) = %d, want %d", tc.input, result, tc.expected)
			}
		})
	}
}

func TestLoadWithWarnings_NoWarnings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Config without PSK - no warnings expected
	configContent := `
[network]
listen_port = 4001
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, warnings, err := LoadWithWarnings(configPath)
	if err != nil {
		t.Fatalf("LoadWithWarnings failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("Config should not be nil")
	}

	if len(warnings) != 0 {
		t.Errorf("Expected no warnings, got %d", len(warnings))
	}
}

func TestLoadWithWarnings_WorldReadablePSK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Permission checks not applicable on Windows")
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Config with inline PSK
	configContent := `
[privacy]
psk = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
`
	// Write with world-readable permissions
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	_, warnings, err := LoadWithWarnings(configPath)
	if err != nil {
		t.Fatalf("LoadWithWarnings failed: %v", err)
	}

	if len(warnings) == 0 {
		t.Error("Expected warning for world-readable config with PSK")
	}
}

func TestLoadWithWarnings_SecurePSK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Permission checks not applicable on Windows")
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Config with inline PSK
	configContent := `
[privacy]
psk = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
`
	// Write with secure permissions
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	_, warnings, err := LoadWithWarnings(configPath)
	if err != nil {
		t.Fatalf("LoadWithWarnings failed: %v", err)
	}

	if len(warnings) != 0 {
		t.Errorf("Expected no warnings for secure config, got: %v", warnings)
	}
}

func TestLoad_PartialConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Only override some values
	configContent := `
[network]
listen_port = 7001
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Overridden value
	if cfg.Network.ListenPort != 7001 {
		t.Errorf("ListenPort = %d, want 7001", cfg.Network.ListenPort)
	}

	// Default values should still be present
	if cfg.Network.ProxyPort != 9977 {
		t.Errorf("ProxyPort = %d, want 9977 (default)", cfg.Network.ProxyPort)
	}
	if cfg.Cache.MaxSize != "10GB" {
		t.Errorf("Cache.MaxSize = %s, want 10GB (default)", cfg.Cache.MaxSize)
	}
}

func TestParseSize_EdgeCases(t *testing.T) {
	// Test with just numbers (no unit)
	size, err := ParseSize("12345")
	if err != nil {
		t.Fatalf("ParseSize(\"12345\") error: %v", err)
	}
	if size != 12345 {
		t.Errorf("ParseSize(\"12345\") = %d, want 12345", size)
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("DefaultConfig().Validate() should not error, got: %v", err)
	}
}

func TestValidate_InvalidBootstrapPeers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Network.BootstrapPeers = []string{
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN", // valid
		"not-a-valid-multiaddr", // invalid
		"/ip4/invalid",          // invalid
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected validation error for invalid bootstrap peers")
	}

	// Should contain errors for the invalid entries
	errStr := err.Error()
	if !contains(errStr, "bootstrap_peers[1]") {
		t.Errorf("Error should mention bootstrap_peers[1], got: %s", errStr)
	}
}

func TestValidate_InvalidPort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Network.ListenPort = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected validation error for invalid port")
	}

	if !contains(err.Error(), "listen_port") {
		t.Errorf("Error should mention listen_port, got: %s", err.Error())
	}
}

func TestValidate_MutuallyExclusivePSK(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Privacy.PSK = "some-hex-value"
	cfg.Privacy.PSKPath = "/path/to/psk"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected validation error for mutually exclusive PSK settings")
	}

	if !contains(err.Error(), "mutually exclusive") {
		t.Errorf("Error should mention mutually exclusive, got: %s", err.Error())
	}
}

func TestValidate_InvalidLogLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Logging.Level = "invalid-level"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected validation error for invalid log level")
	}

	if !contains(err.Error(), "logging.level") {
		t.Errorf("Error should mention logging.level, got: %s", err.Error())
	}
}

func TestValidationErrors_MultipleErrors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Network.ListenPort = -1
	cfg.Network.ProxyPort = 99999
	cfg.Logging.Level = "bad"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected multiple validation errors")
	}

	errs, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("Expected ValidationErrors type, got %T", err)
	}

	if len(errs) < 3 {
		t.Errorf("Expected at least 3 errors, got %d: %v", len(errs), errs)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestCacheConfig_MaxSizeBytes(t *testing.T) {
	tests := []struct {
		name     string
		maxSize  string
		expected int64
	}{
		{"10GB", "10GB", 10 * 1024 * 1024 * 1024},
		{"1GB", "1GB", 1024 * 1024 * 1024},
		{"500MB", "500MB", 500 * 1024 * 1024},
		{"invalid falls back to 10GB", "invalid", 10 * 1024 * 1024 * 1024},
		{"empty falls back to 10GB", "", 10 * 1024 * 1024 * 1024},
		{"zero falls back to 10GB", "0", 10 * 1024 * 1024 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &CacheConfig{MaxSize: tt.maxSize}
			got := cfg.MaxSizeBytes()
			if got != tt.expected {
				t.Errorf("MaxSizeBytes() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestCacheConfig_MinFreeSpaceBytes(t *testing.T) {
	tests := []struct {
		name         string
		minFreeSpace string
		expected     int64
	}{
		{"1GB", "1GB", 1024 * 1024 * 1024},
		{"500MB", "500MB", 500 * 1024 * 1024},
		{"zero means no minimum", "0", 0},
		{"invalid parses as 0 (no min)", "invalid", 0},
		{"empty parses as 0 (no min)", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &CacheConfig{MinFreeSpace: tt.minFreeSpace}
			got := cfg.MinFreeSpaceBytes()
			if got != tt.expected {
				t.Errorf("MinFreeSpaceBytes() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestTransferConfig_MaxUploadRateBytes(t *testing.T) {
	tests := []struct {
		name     string
		rate     string
		expected int64
	}{
		{"10MB/s", "10MB/s", 10 * 1024 * 1024},
		{"1MB/s", "1MB/s", 1024 * 1024},
		{"0 (unlimited)", "0", 0},
		{"invalid falls back to 0", "invalid", 0},
		{"empty falls back to 0", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TransferConfig{MaxUploadRate: tt.rate}
			got := cfg.MaxUploadRateBytes()
			if got != tt.expected {
				t.Errorf("MaxUploadRateBytes() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestTransferConfig_MaxDownloadRateBytes(t *testing.T) {
	tests := []struct {
		name     string
		rate     string
		expected int64
	}{
		{"10MB/s", "10MB/s", 10 * 1024 * 1024},
		{"1MB/s", "1MB/s", 1024 * 1024},
		{"0 (unlimited)", "0", 0},
		{"invalid falls back to 0", "invalid", 0},
		{"empty falls back to 0", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TransferConfig{MaxDownloadRate: tt.rate}
			got := cfg.MaxDownloadRateBytes()
			if got != tt.expected {
				t.Errorf("MaxDownloadRateBytes() = %d, want %d", got, tt.expected)
			}
		})
	}
}

// NetworkConfig getter tests

func TestNetworkConfig_GetConnectivityMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		expected string
	}{
		{"empty defaults to auto", "", "auto"},
		{"auto", "auto", "auto"},
		{"lan_only", "lan_only", "lan_only"},
		{"online_only", "online_only", "online_only"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &NetworkConfig{ConnectivityMode: tt.mode}
			got := cfg.GetConnectivityMode()
			if got != tt.expected {
				t.Errorf("GetConnectivityMode() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestNetworkConfig_GetConnectivityCheckInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		expected time.Duration
	}{
		{"empty defaults to 30s", "", 30 * time.Second},
		{"1m", "1m", 1 * time.Minute},
		{"5s", "5s", 5 * time.Second},
		{"invalid defaults to 30s", "invalid", 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &NetworkConfig{ConnectivityCheckInterval: tt.interval}
			got := cfg.GetConnectivityCheckInterval()
			if got != tt.expected {
				t.Errorf("GetConnectivityCheckInterval() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNetworkConfig_GetConnectivityCheckURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"empty defaults to debian", "", "https://deb.debian.org"},
		{"custom url", "https://example.com", "https://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &NetworkConfig{ConnectivityCheckURL: tt.url}
			got := cfg.GetConnectivityCheckURL()
			if got != tt.expected {
				t.Errorf("GetConnectivityCheckURL() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestNetworkConfig_IsRelayEnabled(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		enabled  *bool
		expected bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", &trueVal, true},
		{"explicit false", &falseVal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &NetworkConfig{EnableRelay: tt.enabled}
			got := cfg.IsRelayEnabled()
			if got != tt.expected {
				t.Errorf("IsRelayEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNetworkConfig_IsHolePunchingEnabled(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		enabled  *bool
		expected bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", &trueVal, true},
		{"explicit false", &falseVal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &NetworkConfig{EnableHolePunching: tt.enabled}
			got := cfg.IsHolePunchingEnabled()
			if got != tt.expected {
				t.Errorf("IsHolePunchingEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// IndexConfig getter tests

func TestIndexConfig_GetWatchAPTLists(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		watch    *bool
		expected bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", &trueVal, true},
		{"explicit false", &falseVal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &IndexConfig{WatchAPTLists: tt.watch}
			got := cfg.GetWatchAPTLists()
			if got != tt.expected {
				t.Errorf("GetWatchAPTLists() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIndexConfig_GetImportAPTArchives(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		importV  *bool
		expected bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", &trueVal, true},
		{"explicit false", &falseVal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &IndexConfig{ImportAPTArchives: tt.importV}
			got := cfg.GetImportAPTArchives()
			if got != tt.expected {
				t.Errorf("GetImportAPTArchives() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// AuditConfig getter tests

func TestAuditConfig_GetMaxSizeMB(t *testing.T) {
	tests := []struct {
		name     string
		size     int
		expected int
	}{
		{"zero defaults to 100", 0, 100},
		{"negative defaults to 100", -1, 100},
		{"positive value", 50, 50},
		{"large value", 500, 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &AuditConfig{MaxSizeMB: tt.size}
			got := cfg.GetMaxSizeMB()
			if got != tt.expected {
				t.Errorf("GetMaxSizeMB() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestAuditConfig_GetMaxBackups(t *testing.T) {
	tests := []struct {
		name     string
		backups  int
		expected int
	}{
		{"zero defaults to 5", 0, 5},
		{"negative defaults to 5", -1, 5},
		{"positive value", 10, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &AuditConfig{MaxBackups: tt.backups}
			got := cfg.GetMaxBackups()
			if got != tt.expected {
				t.Errorf("GetMaxBackups() = %d, want %d", got, tt.expected)
			}
		})
	}
}

// TransferConfig getter tests

func TestTransferConfig_RetryIntervalDuration(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		expected time.Duration
	}{
		{"empty defaults to 5m", "", 5 * time.Minute},
		{"1m", "1m", 1 * time.Minute},
		{"10s", "10s", 10 * time.Second},
		{"invalid defaults to 5m", "invalid", 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TransferConfig{RetryInterval: tt.interval}
			got := cfg.RetryIntervalDuration()
			if got != tt.expected {
				t.Errorf("RetryIntervalDuration() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestTransferConfig_RetryMaxAgeDuration(t *testing.T) {
	tests := []struct {
		name     string
		maxAge   string
		expected time.Duration
	}{
		{"empty defaults to 1h", "", 1 * time.Hour},
		{"30m", "30m", 30 * time.Minute},
		{"2h", "2h", 2 * time.Hour},
		{"invalid defaults to 1h", "invalid", 1 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TransferConfig{RetryMaxAge: tt.maxAge}
			got := cfg.RetryMaxAgeDuration()
			if got != tt.expected {
				t.Errorf("RetryMaxAgeDuration() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestTransferConfig_PerPeerRateBytes(t *testing.T) {
	tests := []struct {
		name     string
		rate     string
		expected int64
	}{
		{"empty is auto (0)", "", 0},
		{"auto is 0", "auto", 0},
		{"5MB/s", "5MB/s", 5 * 1024 * 1024},
		{"invalid is 0", "invalid", 0},
	}

	for _, tt := range tests {
		t.Run("upload_"+tt.name, func(t *testing.T) {
			cfg := &TransferConfig{PerPeerUploadRate: tt.rate}
			got := cfg.PerPeerUploadRateBytes()
			if got != tt.expected {
				t.Errorf("PerPeerUploadRateBytes() = %d, want %d", got, tt.expected)
			}
		})
		t.Run("download_"+tt.name, func(t *testing.T) {
			cfg := &TransferConfig{PerPeerDownloadRate: tt.rate}
			got := cfg.PerPeerDownloadRateBytes()
			if got != tt.expected {
				t.Errorf("PerPeerDownloadRateBytes() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestTransferConfig_IsPerPeerEnabled(t *testing.T) {
	tests := []struct {
		name       string
		uploadRate string
		downRate   string
		expected   bool
	}{
		{"default is enabled", "", "", true},
		{"auto is enabled", "auto", "auto", true},
		{"both 0 is disabled", "0", "0", false},
		{"only upload 0 is enabled", "0", "auto", true},
		{"only download 0 is enabled", "auto", "0", true},
		{"explicit rate is enabled", "5MB/s", "5MB/s", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TransferConfig{
				PerPeerUploadRate:   tt.uploadRate,
				PerPeerDownloadRate: tt.downRate,
			}
			got := cfg.IsPerPeerEnabled()
			if got != tt.expected {
				t.Errorf("IsPerPeerEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestTransferConfig_IsAdaptiveEnabled(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		adaptive *bool
		perPeer  bool
		expected bool
	}{
		{"nil with per-peer enabled", nil, true, true},
		{"nil with per-peer disabled", nil, false, false},
		{"explicit true", &trueVal, false, true},
		{"explicit false", &falseVal, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TransferConfig{
				AdaptiveRateLimiting: tt.adaptive,
			}
			if !tt.perPeer {
				cfg.PerPeerUploadRate = "0"
				cfg.PerPeerDownloadRate = "0"
			}
			got := cfg.IsAdaptiveEnabled()
			if got != tt.expected {
				t.Errorf("IsAdaptiveEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestTransferConfig_AdaptiveMinRateBytes(t *testing.T) {
	tests := []struct {
		name     string
		rate     string
		expected int64
	}{
		{"empty defaults to 100KB", "", 100 * 1024},
		{"50KB/s", "50KB/s", 50 * 1024},
		{"200KB/s", "200KB/s", 200 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TransferConfig{AdaptiveMinRate: tt.rate}
			got := cfg.AdaptiveMinRateBytes()
			if got != tt.expected {
				t.Errorf("AdaptiveMinRateBytes() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestTransferConfig_AdaptiveMaxBoostFactor(t *testing.T) {
	tests := []struct {
		name     string
		boost    float64
		expected float64
	}{
		{"zero defaults to 1.5", 0, 1.5},
		{"negative defaults to 1.5", -1, 1.5},
		{"normal value", 2.0, 2.0},
		{"capped at 10", 15, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TransferConfig{AdaptiveMaxBoost: tt.boost}
			got := cfg.AdaptiveMaxBoostFactor()
			if got != tt.expected {
				t.Errorf("AdaptiveMaxBoostFactor() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestTransferConfig_GetExpectedPeers(t *testing.T) {
	tests := []struct {
		name     string
		peers    int
		expected int
	}{
		{"zero defaults to 10", 0, 10},
		{"negative defaults to 10", -1, 10},
		{"positive value", 20, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TransferConfig{ExpectedPeers: tt.peers}
			got := cfg.GetExpectedPeers()
			if got != tt.expected {
				t.Errorf("GetExpectedPeers() = %d, want %d", got, tt.expected)
			}
		})
	}
}

// DHTConfig getter tests

func TestDHTConfig_AnnounceIntervalDuration(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		expected time.Duration
	}{
		{"empty defaults to 12h", "", 12 * time.Hour},
		{"6h", "6h", 6 * time.Hour},
		{"invalid defaults to 12h", "invalid", 12 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &DHTConfig{AnnounceInterval: tt.interval}
			got := cfg.AnnounceIntervalDuration()
			if got != tt.expected {
				t.Errorf("AnnounceIntervalDuration() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// SchedulerConfig getter tests

func TestSchedulerConfig_OutsideWindowRateBytes(t *testing.T) {
	tests := []struct {
		name     string
		rate     string
		expected int64
	}{
		{"empty defaults to 100KB", "", 100 * 1024},
		{"50KB/s", "50KB/s", 50 * 1024},
		{"200KB/s", "200KB/s", 200 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{OutsideWindowRate: tt.rate}
			got := cfg.OutsideWindowRateBytes()
			if got != tt.expected {
				t.Errorf("OutsideWindowRateBytes() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestSchedulerConfig_InsideWindowRateBytes(t *testing.T) {
	tests := []struct {
		name     string
		rate     string
		expected int64
	}{
		{"empty is unlimited (0)", "", 0},
		{"unlimited is 0", "unlimited", 0},
		{"10MB/s", "10MB/s", 10 * 1024 * 1024},
		{"invalid is unlimited (0)", "invalid", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{InsideWindowRate: tt.rate}
			got := cfg.InsideWindowRateBytes()
			if got != tt.expected {
				t.Errorf("InsideWindowRateBytes() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestSchedulerConfig_IsUrgentFullSpeed(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		urgent   *bool
		expected bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", &trueVal, true},
		{"explicit false", &falseVal, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{UrgentFullSpeed: tt.urgent}
			got := cfg.IsUrgentFullSpeed()
			if got != tt.expected {
				t.Errorf("IsUrgentFullSpeed() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// FleetConfig getter tests

func TestFleetConfig_ClaimTimeoutDuration(t *testing.T) {
	tests := []struct {
		name     string
		timeout  string
		expected time.Duration
	}{
		{"empty defaults to 5s", "", 5 * time.Second},
		{"10s", "10s", 10 * time.Second},
		{"invalid defaults to 5s", "invalid", 5 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &FleetConfig{ClaimTimeout: tt.timeout}
			got := cfg.ClaimTimeoutDuration()
			if got != tt.expected {
				t.Errorf("ClaimTimeoutDuration() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFleetConfig_MaxWaitTimeDuration(t *testing.T) {
	tests := []struct {
		name     string
		waitTime string
		expected time.Duration
	}{
		{"empty defaults to 5m", "", 5 * time.Minute},
		{"10m", "10m", 10 * time.Minute},
		{"invalid defaults to 5m", "invalid", 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &FleetConfig{MaxWaitTime: tt.waitTime}
			got := cfg.MaxWaitTimeDuration()
			if got != tt.expected {
				t.Errorf("MaxWaitTimeDuration() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFleetConfig_RefreshIntervalDuration(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		expected time.Duration
	}{
		{"empty defaults to 1s", "", 1 * time.Second},
		{"500ms", "500ms", 500 * time.Millisecond},
		{"invalid defaults to 1s", "invalid", 1 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &FleetConfig{RefreshInterval: tt.interval}
			got := cfg.RefreshIntervalDuration()
			if got != tt.expected {
				t.Errorf("RefreshIntervalDuration() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFleetConfig_GetAllowConcurrent(t *testing.T) {
	tests := []struct {
		name       string
		concurrent int
		expected   int
	}{
		{"zero defaults to 1", 0, 1},
		{"negative defaults to 1", -1, 1},
		{"positive value", 5, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &FleetConfig{AllowConcurrent: tt.concurrent}
			got := cfg.GetAllowConcurrent()
			if got != tt.expected {
				t.Errorf("GetAllowConcurrent() = %d, want %d", got, tt.expected)
			}
		})
	}
}

// ValidationError tests

func TestValidationError_Error(t *testing.T) {
	err := &ValidationError{
		Field:   "network.port",
		Message: "must be positive",
	}
	got := err.Error()
	expected := "config validation failed: network.port: must be positive"
	if got != expected {
		t.Errorf("Error() = %q, want %q", got, expected)
	}
}

func TestValidationErrors_Error(t *testing.T) {
	// Empty errors
	var empty ValidationErrors
	if empty.Error() != "" {
		t.Errorf("Empty ValidationErrors.Error() should be empty, got %q", empty.Error())
	}

	// Single error
	single := ValidationErrors{{Field: "foo", Message: "bar"}}
	if !contains(single.Error(), "foo") || !contains(single.Error(), "bar") {
		t.Errorf("Single error should contain field and message, got %q", single.Error())
	}

	// Multiple errors
	multi := ValidationErrors{
		{Field: "field1", Message: "msg1"},
		{Field: "field2", Message: "msg2"},
	}
	errStr := multi.Error()
	if !contains(errStr, "2 errors") {
		t.Errorf("Multi error should mention count, got %q", errStr)
	}
	if !contains(errStr, "field1") || !contains(errStr, "field2") {
		t.Errorf("Multi error should contain all fields, got %q", errStr)
	}
}
