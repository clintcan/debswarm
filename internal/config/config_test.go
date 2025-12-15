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
