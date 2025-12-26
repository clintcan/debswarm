package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
