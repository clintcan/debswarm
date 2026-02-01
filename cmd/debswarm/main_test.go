package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/debswarm/debswarm/internal/config"
)

// newRootCmd creates a fresh root command for testing
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "debswarm",
		Short: "Peer-to-peer package distribution for APT",
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().StringVarP(&logLevel, "log-level", "l", "info", "log level")
	rootCmd.PersistentFlags().StringVar(&logFile, "log-file", "", "log file path")
	rootCmd.PersistentFlags().StringVarP(&dataDir, "data-dir", "d", "", "data directory")

	rootCmd.AddCommand(daemonCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(cacheCmd())
	rootCmd.AddCommand(peersCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(seedCmd())
	rootCmd.AddCommand(pskCmd())
	rootCmd.AddCommand(identityCmd())
	rootCmd.AddCommand(benchmarkCmd())
	rootCmd.AddCommand(versionCmd())

	return rootCmd
}

func TestRootCommand_Help(t *testing.T) {
	rootCmd := newRootCmd()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("root --help failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "debswarm") {
		t.Error("help output should contain 'debswarm'")
	}
	if !strings.Contains(output, "daemon") {
		t.Error("help output should list 'daemon' command")
	}
	if !strings.Contains(output, "version") {
		t.Error("help output should list 'version' command")
	}
}

func TestVersionCommand(t *testing.T) {
	rootCmd := newRootCmd()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"version"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	// Note: version command uses fmt.Printf which goes to stdout, not cmd.Out()
	// This test mainly verifies the command executes without error
}

func TestConfigInitCommand(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "test-config.toml")

	rootCmd := newRootCmd()
	rootCmd.SetArgs([]string{"--config", cfgPath, "config", "init"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("config init failed: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Error("config file was not created")
	}
}

func TestConfigShowCommand(t *testing.T) {
	// Create a config file first
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "test-config.toml")

	// Create config
	rootCmd := newRootCmd()
	rootCmd.SetArgs([]string{"--config", cfgPath, "config", "init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("config init failed: %v", err)
	}

	// Show config
	rootCmd = newRootCmd()
	rootCmd.SetArgs([]string{"--config", cfgPath, "config", "show"})
	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("config show failed: %v", err)
	}
}

func TestCacheCommand_Help(t *testing.T) {
	rootCmd := newRootCmd()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"cache", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("cache --help failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "list") {
		t.Error("cache help should list 'list' subcommand")
	}
	if !strings.Contains(output, "stats") {
		t.Error("cache help should list 'stats' subcommand")
	}
	if !strings.Contains(output, "clear") {
		t.Error("cache help should list 'clear' subcommand")
	}
}

func TestSeedCommand_Help(t *testing.T) {
	rootCmd := newRootCmd()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"seed", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("seed --help failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "import") {
		t.Error("seed help should list 'import' subcommand")
	}
	if !strings.Contains(output, "list") {
		t.Error("seed help should list 'list' subcommand")
	}
}

func TestPskCommand_Help(t *testing.T) {
	rootCmd := newRootCmd()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"psk", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("psk --help failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "generate") {
		t.Error("psk help should list 'generate' subcommand")
	}
	if !strings.Contains(output, "show") {
		t.Error("psk help should list 'show' subcommand")
	}
}

func TestIdentityCommand_Help(t *testing.T) {
	rootCmd := newRootCmd()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"identity", "--help"})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("identity --help failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "show") {
		t.Error("identity help should list 'show' subcommand")
	}
	if !strings.Contains(output, "regenerate") {
		t.Error("identity help should list 'regenerate' subcommand")
	}
}

func TestPskGenerateCommand(t *testing.T) {
	tmpDir := t.TempDir()
	pskPath := filepath.Join(tmpDir, "test.key")

	rootCmd := newRootCmd()
	rootCmd.SetArgs([]string{"psk", "generate", "-o", pskPath})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("psk generate failed: %v", err)
	}

	// Verify file was created
	info, err := os.Stat(pskPath)
	if os.IsNotExist(err) {
		t.Error("PSK file was not created")
	}
	// Check permissions (should be 0600) - skip on Windows as it uses a different permission model
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Errorf("PSK file permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{100, "100 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
		{1024 * 1024 * 1024 * 1024 * 1024, "1.0 PB"},
		{1500 * 1024, "1.5 MB"},
		{1500 * 1024 * 1024, "1.5 GB"},
		{500 * 1024 * 1024, "500.0 MB"},
		{10 * 1024 * 1024 * 1024, "10.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := formatBytes(tt.input)
			if got != tt.expected {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestConfigPaths(t *testing.T) {
	// Save original value
	origCfgFile := cfgFile
	defer func() { cfgFile = origCfgFile }()

	// Test with explicit config file
	cfgFile = "/custom/config.toml"
	paths := configPaths()
	if len(paths) != 1 || paths[0] != "/custom/config.toml" {
		t.Errorf("With cfgFile set, configPaths() = %v, want [/custom/config.toml]", paths)
	}

	// Test with empty config file (should return default paths)
	cfgFile = ""
	paths = configPaths()
	if len(paths) != 2 {
		t.Errorf("Without cfgFile, configPaths() should return 2 paths, got %d", len(paths))
	}
	if paths[0] != "/etc/debswarm/config.toml" {
		t.Errorf("First path should be /etc/debswarm/config.toml, got %s", paths[0])
	}
	if !strings.Contains(paths[1], "config.toml") {
		t.Errorf("Second path should contain config.toml, got %s", paths[1])
	}
}

func TestResolveDataDir(t *testing.T) {
	// Save original values
	origDataDir := dataDir
	origStateDir := os.Getenv("STATE_DIRECTORY")
	defer func() {
		dataDir = origDataDir
		if origStateDir != "" {
			os.Setenv("STATE_DIRECTORY", origStateDir)
		} else {
			os.Unsetenv("STATE_DIRECTORY")
		}
	}()

	// Create a mock config
	cfg := &config.Config{
		Cache: config.CacheConfig{
			Path: "/tmp/cache",
		},
	}

	// Test 1: --data-dir flag takes precedence
	dataDir = "/custom/data"
	os.Unsetenv("STATE_DIRECTORY")
	result := resolveDataDir(cfg)
	if result != "/custom/data" {
		t.Errorf("With dataDir flag, resolveDataDir() = %q, want %q", result, "/custom/data")
	}

	// Test 2: STATE_DIRECTORY env takes precedence when no flag
	dataDir = ""
	os.Setenv("STATE_DIRECTORY", "/var/lib/systemd-state")
	result = resolveDataDir(cfg)
	if result != "/var/lib/systemd-state" {
		t.Errorf("With STATE_DIRECTORY, resolveDataDir() = %q, want %q", result, "/var/lib/systemd-state")
	}

	// Test 3: Falls back to user home directory
	dataDir = ""
	os.Unsetenv("STATE_DIRECTORY")
	result = resolveDataDir(cfg)
	// Should be either /var/lib/debswarm or ~/.local/share/debswarm
	if result == "" {
		t.Error("resolveDataDir() should return a non-empty path")
	}
}

func TestLoadConfig_NoFile(t *testing.T) {
	// Save original value
	origCfgFile := cfgFile
	defer func() { cfgFile = origCfgFile }()

	// Set to nonexistent path
	cfgFile = "/nonexistent/config.toml"
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() with nonexistent file should return defaults, got error: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadConfig() should return non-nil config")
	}
	// Should have defaults
	if cfg.Network.ProxyPort != 9977 {
		t.Errorf("Expected default proxy port 9977, got %d", cfg.Network.ProxyPort)
	}
}

func TestLoadConfigWithWarnings_NoFile(t *testing.T) {
	// Save original value
	origCfgFile := cfgFile
	defer func() { cfgFile = origCfgFile }()

	// Set to nonexistent path - should return defaults with no warnings
	cfgFile = "/nonexistent/config.toml"
	cfg, warnings, err := loadConfigWithWarnings()
	if err != nil {
		t.Fatalf("loadConfigWithWarnings() error: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadConfigWithWarnings() should return non-nil config")
	}
	if len(warnings) != 0 {
		t.Errorf("Expected no warnings for nonexistent config, got %d", len(warnings))
	}
}
