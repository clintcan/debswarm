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

	// existingPath is the config file the wizard loaded its starting values from,
	// or "" when starting from defaults. When set, the wizard is editing rather
	// than creating: prompts default to the current values, the profile step
	// offers to keep them, and the result is saved back to this path.
	existingPath string
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

			// Start from the existing config when there is one, so re-running the
			// wizard edits the current settings instead of silently resetting them.
			if path, ok := existingConfigPath(); ok {
				if loaded, err := config.Load(path); err == nil {
					w.cfg = loaded
					w.existingPath = path
				} else {
					w.printf("Warning: could not read %s (%v).\nStarting from defaults.\n", path, err)
				}
			}
			return w.run(outputPath)
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "output config file path")
	return cmd
}

// run executes the full wizard flow.
func (w *wizard) run(outputPath string) error {
	editing := w.existingPath != ""

	w.printf("\n")
	w.printf("╔══════════════════════════════════════════╗\n")
	w.printf("║   debswarm configuration wizard          ║\n")
	if editing {
		w.printf("║   Press Enter to keep [current] values    ║\n")
	} else {
		w.printf("║   Press Enter to accept [defaults]       ║\n")
	}
	w.printf("╚══════════════════════════════════════════╝\n")
	if editing {
		w.printf("\nEditing existing configuration: %s\n", w.existingPath)
	}
	w.printf("\n")

	// Step 1: Deployment profile.
	// profileIdx is -1 when keeping the current settings (no profile applied).
	// Applying a profile overwrites cache size, rates, mDNS, fleet, metrics bind,
	// and connectivity mode, so when editing we must not do it unless asked.
	profileIdx := w.promptProfile(editing)
	if profileIdx >= 0 {
		w.applyProfile(profileIdx)
	}

	// Step 2: Cache size
	w.promptCacheSize()

	// Step 3: Bandwidth limits
	w.promptBandwidthLimits()

	// Step 4: Ports
	w.promptPorts()

	// Step 5: Repositories
	w.promptRepositories()

	// Step 6: Privacy (mDNS + PSK)
	w.promptPrivacy(profileIdx)

	// Step 7: Fleet coordination
	w.promptFleet()

	// Step 8: Log level
	w.promptLogLevel()

	// Step 9: Summary + confirm
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

	// Resolve output path. When editing, write back to the file we read from so
	// re-running the wizard updates that config instead of creating a second one.
	savePath := outputPath
	if savePath == "" {
		switch {
		case cfgFile != "":
			savePath = cfgFile
		case w.existingPath != "":
			savePath = w.existingPath
		default:
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

// profileLabels are the deployment profiles offered in step 1, in the same order
// as the profiles slice.
var profileLabels = []string{
	"Home user — small cache, public DHT",
	"Seeding server — large cache, rate-limited, fleet enabled",
	"Private swarm — PSK, mDNS only, no public DHT",
}

// promptProfile asks for a deployment profile. When editing an existing config it
// offers "keep current settings" as the default, and returns -1 for that choice —
// applying a profile would overwrite values the user already chose.
func (w *wizard) promptProfile(editing bool) int {
	if !editing {
		return w.promptChoice("Step 1: Deployment profile", profileLabels, 0)
	}

	opts := append([]string{"Keep current settings — only change what you edit below"}, profileLabels...)
	choice := w.promptChoice("Step 1: Deployment profile", opts, 0)
	if choice == 0 {
		return -1
	}
	w.printf("  Applying the %s profile will overwrite your current cache size,\n", profiles[choice-1].name)
	w.printf("  rate limits, mDNS, fleet, and metrics settings.\n")
	if !w.promptYesNo("  Apply it?", false) {
		return -1
	}
	return choice - 1
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

// promptRepositories configures which repository hosts the proxy will fetch from.
//
// Both settings are written explicitly rather than left to their defaults: an
// absent key is invisible, and users with a private mirror or an HTTPS-only repo
// need to discover that these knobs exist.
func (w *wizard) promptRepositories() {
	w.printf("\n")
	w.printf("  Debian, Ubuntu, and Linux Mint mirrors are always allowed.\n")
	w.printf("  Common third-party repos (Docker, Launchpad PPAs, PostgreSQL, NodeSource,\n")
	w.printf("  Microsoft, HashiCorp, kernel.org, Kubernetes) can be trusted automatically.\n")

	trust := w.promptYesNo(
		"Step 5a: Trust common third-party repositories?",
		w.cfg.Proxy.TrustsKnownRepos(),
	)
	w.cfg.Proxy.TrustKnownRepos = &trust

	// Default to the hosts already configured, so pressing Enter keeps them rather
	// than silently clearing the list. "none" is the escape hatch for clearing it.
	current := strings.Join(w.cfg.Proxy.AllowedHosts, ", ")
	if current == "" {
		w.printf("  Any other repository hosts to allow? (comma-separated, blank for none)\n")
	} else {
		w.printf("  Any other repository hosts to allow? (comma-separated, blank keeps current,\n")
		w.printf("  \"none\" clears the list)\n")
	}
	hosts := w.promptString(
		fmt.Sprintf("Step 5b: Additional repository hosts? [%s]", displayHosts(current)),
		current,
	)
	w.cfg.Proxy.AllowedHosts = parseHostList(hosts)

	// HTTPS-only repos need an http:// source plus an entry in https_upstream_hosts.
	// The curated default already covers the common case (pkgs.k8s.io), so surface
	// this as a hint rather than another prompt.
	if len(w.cfg.Proxy.EffectiveHTTPSUpstreamHosts()) > 0 {
		w.printf("  Note: HTTPS-only repos are fetched over HTTPS upstream so they can still be\n")
		w.printf("  cached and P2P-shared. Enabled for: %s\n", strings.Join(w.cfg.Proxy.EffectiveHTTPSUpstreamHosts(), ", "))
		w.printf("  To add your own, set [proxy] https_upstream_hosts and use http:// in sources.list.\n")
	}
}

// parseHostList splits a comma-separated host list, trimming blanks.
// The literal "none" clears the list, so an existing list can be emptied from the
// prompt (where a blank answer means "keep current").
// Returns nil (not an empty slice) when no hosts are given.
func parseHostList(s string) []string {
	if strings.EqualFold(strings.TrimSpace(s), "none") {
		return nil
	}
	var hosts []string
	for h := range strings.SplitSeq(s, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// displayHosts renders a host list for a prompt default, showing "none" when empty.
func displayHosts(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func (w *wizard) promptPrivacy(profileIdx int) {
	w.printf("\n")
	w.cfg.Privacy.EnableMDNS = w.promptYesNo(
		"Step 6a: Enable LAN discovery (mDNS)?",
		w.cfg.Privacy.EnableMDNS,
	)

	// PSK generation for private swarm profile
	if profileIdx == 2 {
		if w.promptYesNo("Step 6b: Generate a new PSK now?", true) {
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
		"Step 7: Enable fleet coordination (LAN download dedup)?",
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
	idx := w.promptChoice("Step 8: Log level", levels, currentDefault)
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
	w.printf("  %-28s %v\n", "Trust known repos:", w.cfg.Proxy.TrustsKnownRepos())
	if len(w.cfg.Proxy.AllowedHosts) > 0 {
		w.printf("  %-28s %s\n", "Additional repo hosts:", strings.Join(w.cfg.Proxy.AllowedHosts, ", "))
	}
	if hosts := w.cfg.Proxy.EffectiveHTTPSUpstreamHosts(); len(hosts) > 0 {
		w.printf("  %-28s %s\n", "HTTPS upstream fetch:", strings.Join(hosts, ", "))
	}
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
