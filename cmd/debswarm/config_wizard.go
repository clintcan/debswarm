package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/debswarm/debswarm/internal/config"
	"github.com/debswarm/debswarm/internal/p2p"
)

// wizard drives the interactive configuration flow.
type wizard struct {
	scanner *bufio.Scanner
	cfg     *config.Config
	out     *os.File // output writer (os.Stdout in production)
}

// profile presets applied after the user picks a deployment mode.
type profile struct {
	name            string
	cacheSize       string
	uploadRate      string
	downloadRate    string
	enableMDNS      bool
	announce        bool
	pskPath         string
	fleetEnabled    bool
	metricsBind     string
	connectivityMod string
}

var profiles = []profile{
	{
		name:         "Home user",
		cacheSize:    "10GB",
		uploadRate:   "0",
		downloadRate: "0",
		enableMDNS:   true,
		announce:     true,
		fleetEnabled: false,
		metricsBind:  "127.0.0.1",
	},
	{
		name:         "Seeding server",
		cacheSize:    "50GB",
		uploadRate:   "50MB/s",
		downloadRate: "0",
		enableMDNS:   true,
		announce:     true,
		fleetEnabled: true,
		metricsBind:  "0.0.0.0",
	},
	{
		name:            "Private swarm",
		cacheSize:       "10GB",
		uploadRate:      "0",
		downloadRate:    "0",
		enableMDNS:      true,
		announce:        true,
		pskPath:         "", // resolved at runtime
		fleetEnabled:    false,
		metricsBind:     "127.0.0.1",
		connectivityMod: "lan_only",
	},
}

func configWizardCmd() *cobra.Command {
	var outputPath string
	cmd := &cobra.Command{
		Use:   "wizard",
		Short: "Interactive configuration wizard for new installations",
		Long: `Guides you through the most important configuration options,
applies a deployment profile, validates inputs, and saves a ready-to-use config.toml.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := &wizard{
				scanner: bufio.NewScanner(os.Stdin),
				cfg:     config.DefaultConfig(),
				out:     os.Stdout,
			}
			return w.run(outputPath)
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "output config file path")
	return cmd
}

// run executes the full wizard flow.
func (w *wizard) run(outputPath string) error {
	w.printf("\n")
	w.printf("╔══════════════════════════════════════════╗\n")
	w.printf("║   debswarm configuration wizard          ║\n")
	w.printf("║   Press Enter to accept [defaults]       ║\n")
	w.printf("╚══════════════════════════════════════════╝\n")
	w.printf("\n")

	// Step 1: Deployment profile
	profileIdx := w.promptChoice(
		"Step 1: Deployment profile",
		[]string{"Home user — small cache, public DHT",
			"Seeding server — large cache, rate-limited, fleet enabled",
			"Private swarm — PSK, mDNS only, no public DHT"},
		0,
	)
	w.applyProfile(profileIdx)

	// Step 2: Cache size
	w.promptCacheSize()

	// Step 3: Bandwidth limits
	w.promptBandwidthLimits()

	// Step 4: Ports
	w.promptPorts()

	// Step 5: Privacy (mDNS + PSK)
	w.promptPrivacy(profileIdx)

	// Step 6: Fleet coordination
	w.promptFleet()

	// Step 7: Log level
	w.promptLogLevel()

	// Step 8: Summary + confirm
	w.printSummary()

	if !w.promptYesNo("Save this configuration?", true) {
		w.printf("Aborted. No configuration saved.\n")
		return nil
	}

	// Validate
	if err := w.cfg.Validate(); err != nil {
		w.printf("\nValidation errors:\n%s\n", err)
		return fmt.Errorf("configuration validation failed: %w", err)
	}

	// Resolve output path
	savePath := outputPath
	if savePath == "" {
		if cfgFile != "" {
			savePath = cfgFile
		} else {
			homeDir, _ := os.UserHomeDir()
			savePath = filepath.Join(homeDir, ".config", "debswarm", "config.toml")
		}
	}

	if err := w.cfg.Save(savePath); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	w.printf("\nConfiguration saved to: %s\n", savePath)
	w.printf("\nNext steps:\n")
	w.printf("  debswarm daemon                    # start the daemon\n")
	w.printf("  debswarm config show               # review configuration\n")
	w.printf("  sudo systemctl enable --now debswarm  # enable on boot (Linux)\n")
	return nil
}

// applyProfile sets config values from the chosen profile.
func (w *wizard) applyProfile(idx int) {
	p := profiles[idx]
	w.cfg.Cache.MaxSize = p.cacheSize
	w.cfg.Transfer.MaxUploadRate = p.uploadRate
	w.cfg.Transfer.MaxDownloadRate = p.downloadRate
	w.cfg.Privacy.EnableMDNS = p.enableMDNS
	w.cfg.Privacy.AnnouncePackages = p.announce
	w.cfg.Fleet.Enabled = p.fleetEnabled
	w.cfg.Metrics.Bind = p.metricsBind
	if p.connectivityMod != "" {
		w.cfg.Network.ConnectivityMode = p.connectivityMod
	}
	if p.pskPath != "" {
		w.cfg.Privacy.PSKPath = p.pskPath
	}
}

func (w *wizard) promptCacheSize() {
	w.printf("\n")
	for {
		val := w.promptString(
			fmt.Sprintf("Step 2: Maximum cache size? [%s]", w.cfg.Cache.MaxSize),
			w.cfg.Cache.MaxSize,
		)
		if _, err := config.ParseSize(val); err != nil {
			w.printf("  Invalid size %q: %v. Try e.g. 10GB, 500MB\n", val, err)
			continue
		}
		w.cfg.Cache.MaxSize = val
		return
	}
}

func (w *wizard) promptBandwidthLimits() {
	w.printf("\n")
	for {
		val := w.promptString(
			fmt.Sprintf("Step 3a: Max upload rate? (e.g. 10MB/s, 0=unlimited) [%s]", displayRate(w.cfg.Transfer.MaxUploadRate)),
			w.cfg.Transfer.MaxUploadRate,
		)
		if _, err := config.ParseRate(val); err != nil {
			w.printf("  Invalid rate %q: %v. Try e.g. 10MB/s, 50MB/s, 0\n", val, err)
			continue
		}
		w.cfg.Transfer.MaxUploadRate = val
		break
	}
	for {
		val := w.promptString(
			fmt.Sprintf("Step 3b: Max download rate? (e.g. 10MB/s, 0=unlimited) [%s]", displayRate(w.cfg.Transfer.MaxDownloadRate)),
			w.cfg.Transfer.MaxDownloadRate,
		)
		if _, err := config.ParseRate(val); err != nil {
			w.printf("  Invalid rate %q: %v. Try e.g. 10MB/s, 50MB/s, 0\n", val, err)
			continue
		}
		w.cfg.Transfer.MaxDownloadRate = val
		break
	}
}

func (w *wizard) promptPorts() {
	w.printf("\n")
	w.cfg.Network.ProxyPort = w.promptPort(
		fmt.Sprintf("Step 4a: APT proxy port? [%d]", w.cfg.Network.ProxyPort),
		w.cfg.Network.ProxyPort,
	)
	w.cfg.Network.ListenPort = w.promptPort(
		fmt.Sprintf("Step 4b: P2P listen port? [%d]", w.cfg.Network.ListenPort),
		w.cfg.Network.ListenPort,
	)
	w.printf("  (Metrics/dashboard port, 0 to disable)\n")
	w.cfg.Metrics.Port = w.promptPort(
		fmt.Sprintf("Step 4c: Metrics/dashboard port? [%d]", w.cfg.Metrics.Port),
		w.cfg.Metrics.Port,
	)
}

func (w *wizard) promptPrivacy(profileIdx int) {
	w.printf("\n")
	w.cfg.Privacy.EnableMDNS = w.promptYesNo(
		fmt.Sprintf("Step 5a: Enable LAN discovery (mDNS)? [%s]", boolDefault(w.cfg.Privacy.EnableMDNS)),
		w.cfg.Privacy.EnableMDNS,
	)

	// PSK generation for private swarm profile
	if profileIdx == 2 {
		if w.promptYesNo("Step 5b: Generate a new PSK now?", true) {
			psk, err := p2p.GeneratePSK()
			if err != nil {
				w.printf("  Failed to generate PSK: %v\n", err)
				return
			}
			homeDir, _ := os.UserHomeDir()
			pskPath := filepath.Join(homeDir, ".config", "debswarm", "swarm.key")
			pskDir := filepath.Dir(pskPath)
			if err := os.MkdirAll(pskDir, 0750); err != nil {
				w.printf("  Failed to create directory %s: %v\n", pskDir, err)
				return
			}
			if err := p2p.SavePSK(psk, pskPath); err != nil {
				w.printf("  Failed to save PSK: %v\n", err)
				return
			}
			w.cfg.Privacy.PSKPath = pskPath
			w.printf("  PSK saved to: %s\n", pskPath)
			w.printf("  Fingerprint: %s\n", p2p.PSKFingerprint(psk))
		}
	}
}

func (w *wizard) promptFleet() {
	w.printf("\n")
	w.cfg.Fleet.Enabled = w.promptYesNo(
		fmt.Sprintf("Step 6: Enable fleet coordination (LAN download dedup)? [%s]", boolDefault(w.cfg.Fleet.Enabled)),
		w.cfg.Fleet.Enabled,
	)
}

func (w *wizard) promptLogLevel() {
	w.printf("\n")
	levels := []string{"info (recommended)", "debug", "warn"}
	currentDefault := 0
	switch w.cfg.Logging.Level {
	case "debug":
		currentDefault = 1
	case "warn":
		currentDefault = 2
	}
	idx := w.promptChoice("Step 7: Log level", levels, currentDefault)
	switch idx {
	case 0:
		w.cfg.Logging.Level = "info"
	case 1:
		w.cfg.Logging.Level = "debug"
	case 2:
		w.cfg.Logging.Level = "warn"
	}
}

func (w *wizard) printSummary() {
	w.printf("\n")
	w.printf("╔══════════════════════════════════════════╗\n")
	w.printf("║   Configuration Summary                  ║\n")
	w.printf("╚══════════════════════════════════════════╝\n")
	w.printf("\n")
	w.printf("  %-28s %s\n", "Cache size:", w.cfg.Cache.MaxSize)
	w.printf("  %-28s %s\n", "Max upload rate:", displayRate(w.cfg.Transfer.MaxUploadRate))
	w.printf("  %-28s %s\n", "Max download rate:", displayRate(w.cfg.Transfer.MaxDownloadRate))
	w.printf("  %-28s %d\n", "APT proxy port:", w.cfg.Network.ProxyPort)
	w.printf("  %-28s %d\n", "P2P listen port:", w.cfg.Network.ListenPort)
	w.printf("  %-28s %d\n", "Metrics port:", w.cfg.Metrics.Port)
	w.printf("  %-28s %s\n", "Metrics bind:", w.cfg.Metrics.Bind)
	w.printf("  %-28s %v\n", "mDNS:", w.cfg.Privacy.EnableMDNS)
	w.printf("  %-28s %v\n", "Fleet coordination:", w.cfg.Fleet.Enabled)
	w.printf("  %-28s %s\n", "Log level:", w.cfg.Logging.Level)
	if w.cfg.Privacy.PSKPath != "" {
		w.printf("  %-28s %s\n", "PSK path:", w.cfg.Privacy.PSKPath)
	}
	if w.cfg.Network.ConnectivityMode != "" {
		w.printf("  %-28s %s\n", "Connectivity mode:", w.cfg.Network.ConnectivityMode)
	}
	w.printf("\n")
}

// --- prompt helpers ---

// promptChoice prints numbered options and returns the selected index (0-based).
// Enter accepts the default.
func (w *wizard) promptChoice(question string, options []string, defaultIdx int) int {
	w.printf("%s\n", question)
	for i, opt := range options {
		marker := "  "
		if i == defaultIdx {
			marker = "> "
		}
		w.printf("  %s[%d] %s\n", marker, i+1, opt)
	}
	for {
		w.printf("  Choice [%d]: ", defaultIdx+1)
		line := w.readLine()
		if line == "" {
			return defaultIdx
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(options) {
			w.printf("  Please enter a number between 1 and %d\n", len(options))
			continue
		}
		return n - 1
	}
}

// promptString prints a question and returns the user's answer or the default.
func (w *wizard) promptString(question string, defaultVal string) string {
	w.printf("  %s: ", question)
	line := w.readLine()
	if line == "" {
		return defaultVal
	}
	return line
}

// promptYesNo prints a yes/no question and returns the boolean result.
func (w *wizard) promptYesNo(question string, defaultYes bool) bool {
	hint := "Y/n"
	if !defaultYes {
		hint = "y/N"
	}
	w.printf("  %s [%s]: ", question, hint)
	line := strings.ToLower(strings.TrimSpace(w.readLine()))
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes"
}

// promptPort prompts for a port number with validation.
func (w *wizard) promptPort(question string, defaultVal int) int {
	for {
		val := w.promptString(question, strconv.Itoa(defaultVal))
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > 65535 {
			w.printf("  Invalid port. Must be 0-65535.\n")
			continue
		}
		return n
	}
}

// readLine reads one line from the scanner. Returns "" on EOF.
func (w *wizard) readLine() string {
	if w.scanner.Scan() {
		return strings.TrimSpace(w.scanner.Text())
	}
	return ""
}

// printf writes to the wizard's output.
func (w *wizard) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(w.out, format, args...)
}

// boolDefault returns "Y/n" or "y/N" depending on the boolean.
func boolDefault(b bool) string {
	if b {
		return "Y/n"
	}
	return "y/N"
}
