// Package config handles configuration loading and defaults for apt-p2p
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Config holds all configuration for apt-p2p
type Config struct {
	Network  NetworkConfig  `toml:"network"`
	Cache    CacheConfig    `toml:"cache"`
	Transfer TransferConfig `toml:"transfer"`
	DHT      DHTConfig      `toml:"dht"`
	Privacy  PrivacyConfig  `toml:"privacy"`
	Logging  LoggingConfig  `toml:"logging"`
}

// NetworkConfig holds network-related settings
type NetworkConfig struct {
	ListenPort     int      `toml:"listen_port"`
	ProxyPort      int      `toml:"proxy_port"`
	MaxConnections int      `toml:"max_connections"`
	BootstrapPeers []string `toml:"bootstrap_peers"`
}

// CacheConfig holds cache-related settings
type CacheConfig struct {
	MaxSize      string `toml:"max_size"`
	Path         string `toml:"path"`
	MinFreeSpace string `toml:"min_free_space"`
}

// TransferConfig holds transfer-related settings
type TransferConfig struct {
	MaxUploadRate              string `toml:"max_upload_rate"`
	MaxDownloadRate            string `toml:"max_download_rate"`
	MaxConcurrentUploads       int    `toml:"max_concurrent_uploads"`
	MaxConcurrentPeerDownloads int    `toml:"max_concurrent_peer_downloads"`
}

// DHTConfig holds DHT-related settings
type DHTConfig struct {
	ProviderTTL      time.Duration `toml:"provider_ttl"`
	AnnounceInterval time.Duration `toml:"announce_interval"`
}

// PrivacyConfig holds privacy-related settings
type PrivacyConfig struct {
	EnableMDNS       bool     `toml:"enable_mdns"`
	AnnouncePackages bool     `toml:"announce_packages"`
	PSKPath          string   `toml:"psk_path"`          // Path to PSK file for private swarm
	PSK              string   `toml:"psk"`               // Inline PSK (hex), mutually exclusive with path
	PeerAllowlist    []string `toml:"peer_allowlist"`    // List of allowed peer IDs
}

// LoggingConfig holds logging-related settings
type LoggingConfig struct {
	Level string `toml:"level"`
	File  string `toml:"file"`
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	return &Config{
		Network: NetworkConfig{
			ListenPort:     4001,
			ProxyPort:      9977,
			MaxConnections: 100,
			BootstrapPeers: []string{
				// libp2p public bootstrap nodes
				"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
				"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
				"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
				"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
			},
		},
		Cache: CacheConfig{
			MaxSize:      "10GB",
			Path:         filepath.Join(homeDir, ".cache", "debswarm"),
			MinFreeSpace: "1GB",
		},
		Transfer: TransferConfig{
			MaxUploadRate:              "0", // unlimited
			MaxDownloadRate:            "0", // unlimited
			MaxConcurrentUploads:       20,
			MaxConcurrentPeerDownloads: 10,
		},
		DHT: DHTConfig{
			ProviderTTL:      24 * time.Hour,
			AnnounceInterval: 12 * time.Hour,
		},
		Privacy: PrivacyConfig{
			EnableMDNS:       true,
			AnnouncePackages: true,
		},
		Logging: LoggingConfig{
			Level: "info",
			File:  "",
		},
	}
}

// Load reads configuration from a file, merging with defaults
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // Use defaults if no config file
		}
		return nil, err
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Save writes configuration to a file
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}

	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

// ParseSize parses a size string like "10GB" into bytes
func ParseSize(s string) (int64, error) {
	var size int64
	var unit string

	_, err := parseWithUnit(s, &size, &unit)
	if err != nil {
		return 0, err
	}

	multiplier := int64(1)
	switch unit {
	case "KB", "K":
		multiplier = 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	case "TB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	}

	return size * multiplier, nil
}

func parseWithUnit(s string, size *int64, unit *string) (int, error) {
	var n int
	for i, c := range s {
		if c >= '0' && c <= '9' {
			*size = *size*10 + int64(c-'0')
			n = i + 1
		} else {
			break
		}
	}
	*unit = s[n:]
	return n, nil
}

// ParseRate parses a rate string like "10MB/s" or "100KB" into bytes per second
// Returns 0 for unlimited (empty string, "0", or "unlimited")
func ParseRate(s string) (int64, error) {
	if s == "" || s == "0" || s == "unlimited" {
		return 0, nil
	}

	// Remove "/s" suffix if present
	rateStr := s
	if len(s) > 2 && s[len(s)-2:] == "/s" {
		rateStr = s[:len(s)-2]
	}

	return ParseSize(rateStr)
}

// SecurityWarning represents a security concern with the configuration
type SecurityWarning struct {
	Message string
	File    string
}

// LoadWithWarnings reads configuration and returns security warnings
// This should be used when security-sensitive options might be present
func LoadWithWarnings(path string) (*Config, []SecurityWarning, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, nil, err
	}

	var warnings []SecurityWarning

	// Check file permissions if inline PSK is configured
	if cfg.Privacy.PSK != "" {
		warn := checkFilePermissions(path)
		if warn != nil {
			warnings = append(warnings, *warn)
		}
	}

	return cfg, warnings, nil
}

// checkFilePermissions checks if a file has appropriately restrictive permissions
// Returns a warning if the file is world-readable or group-writable
func checkFilePermissions(path string) *SecurityWarning {
	// Skip permission check on Windows as it uses a different security model
	if runtime.GOOS == "windows" {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil
	}

	mode := info.Mode().Perm()

	// Check if file is world-readable (o+r) or world-writable (o+w)
	// Bits: -----rwx (world), --rwx--- (group), rwx------ (owner)
	if mode&0004 != 0 { // world readable
		return &SecurityWarning{
			Message: fmt.Sprintf("config file is world-readable (mode %04o); consider 'chmod 600 %s' for files with inline PSK", mode, path),
			File:    path,
		}
	}

	if mode&0002 != 0 { // world writable
		return &SecurityWarning{
			Message: fmt.Sprintf("config file is world-writable (mode %04o); this is a security risk", mode),
			File:    path,
		}
	}

	return nil
}
