// Package config handles configuration loading and defaults for apt-p2p
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/multiformats/go-multiaddr"
	"github.com/pelletier/go-toml/v2"
)

// Config holds all configuration for apt-p2p
type Config struct {
	Network   NetworkConfig   `toml:"network"`
	Proxy     ProxyConfig     `toml:"proxy"`
	Cache     CacheConfig     `toml:"cache"`
	Transfer  TransferConfig  `toml:"transfer"`
	DHT       DHTConfig       `toml:"dht"`
	Privacy   PrivacyConfig   `toml:"privacy"`
	Metrics   MetricsConfig   `toml:"metrics"`
	Logging   LoggingConfig   `toml:"logging"`
	Scheduler SchedulerConfig `toml:"scheduler"`
	Fleet     FleetConfig     `toml:"fleet"`
	Index     IndexConfig     `toml:"index"`
	Security  SecurityConfig  `toml:"security"`
}

// ProxyConfig holds proxy-related settings
type ProxyConfig struct {
	// AllowedHosts is a list of additional repository hostnames to allow through the proxy.
	// These hosts must still use Debian-style URL patterns (/dists/, /pool/).
	// Built-in allowed hosts (Debian, Ubuntu, and Linux Mint domains) are always permitted.
	AllowedHosts []string `toml:"allowed_hosts"`

	// TrustKnownRepos controls whether the curated DefaultTrustedRepos set (common
	// third-party repositories such as Docker, Launchpad PPAs, and PostgreSQL) is
	// merged into the effective allowed-hosts list. Defaults to true when unset;
	// set to false for a strict Debian/Ubuntu/Mint-only posture.
	TrustKnownRepos *bool `toml:"trust_known_repos"`

	// HTTPSUpstreamHosts lists repository hostnames for which the proxy should
	// upgrade an incoming plain-HTTP request to an HTTPS connection when fetching
	// from the upstream mirror. This lets APT talk plain HTTP to the local proxy
	// while debswarm caches and P2P-shares packages from HTTPS-only repositories.
	// When TrustKnownRepos is enabled, the curated DefaultHTTPSUpstreamHosts set
	// (known HTTPS-only repos such as pkgs.k8s.io) is merged in automatically.
	HTTPSUpstreamHosts []string `toml:"https_upstream_hosts"`
}

// DefaultTrustedRepos is a curated set of well-known public APT repositories that
// debswarm trusts by default (in addition to the built-in Debian/Ubuntu/Mint
// mirrors) so common third-party sources work without manual configuration.
//
// This only broadens which *public* hosts the proxy will fetch from: private and
// internal addresses remain blocked (SSRF protection), and every package is still
// checked against the SHA256 in the repository index. All entries use the
// standard /dists/ and /pool/ layout required by the proxy's path check.
var DefaultTrustedRepos = []string{
	"ppa.launchpad.net",          // Launchpad PPAs
	"ppa.launchpadcontent.net",   // Launchpad PPAs (current content host)
	"launchpadlibrarian.net",     // Launchpad file downloads
	"download.docker.com",        // Docker
	"apt.postgresql.org",         // PostgreSQL
	"deb.nodesource.com",         // NodeSource (Node.js)
	"packages.microsoft.com",     // Microsoft (VS Code, .NET, etc.)
	"apt.releases.hashicorp.com", // HashiCorp
	"mirrors.kernel.org",         // kernel.org Debian/Ubuntu mirror
	"pkgs.k8s.io",                // Kubernetes (flat-layout repository)
}

// DefaultHTTPSUpstreamHosts is a curated set of known HTTPS-only public APT
// repositories. For these hosts the proxy fetches upstream over HTTPS even when
// APT requests them via plain HTTP, so their packages can be cached, verified,
// and shared over P2P (which an opaque HTTPS CONNECT tunnel cannot do).
//
// Every host here must also be reachable through the allowlist (e.g. present in
// DefaultTrustedRepos); this list only controls the http->https scheme upgrade,
// not whether the host is permitted.
var DefaultHTTPSUpstreamHosts = []string{
	"pkgs.k8s.io", // Kubernetes (HTTPS-only, flat-layout repository)
}

// TrustsKnownRepos reports whether the curated DefaultTrustedRepos set is trusted.
// It defaults to true when the option is unset.
func (p *ProxyConfig) TrustsKnownRepos() bool {
	return p.TrustKnownRepos == nil || *p.TrustKnownRepos
}

// EffectiveAllowedHosts returns the full set of additional hosts the proxy should
// permit: the user-configured AllowedHosts, plus DefaultTrustedRepos when
// TrustKnownRepos is enabled. The result is de-duplicated (case-insensitively),
// preserving user entries first.
func (p *ProxyConfig) EffectiveAllowedHosts() []string {
	if !p.TrustsKnownRepos() {
		return p.AllowedHosts
	}

	seen := make(map[string]struct{}, len(p.AllowedHosts)+len(DefaultTrustedRepos))
	result := make([]string, 0, len(p.AllowedHosts)+len(DefaultTrustedRepos))
	add := func(hosts []string) {
		for _, h := range hosts {
			key := strings.ToLower(strings.TrimSpace(h))
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, h)
		}
	}
	add(p.AllowedHosts)
	add(DefaultTrustedRepos)
	return result
}

// EffectiveHTTPSUpstreamHosts returns the full set of hosts for which the proxy
// should upgrade upstream fetches to HTTPS: the user-configured
// HTTPSUpstreamHosts, plus DefaultHTTPSUpstreamHosts when TrustKnownRepos is
// enabled. The result is de-duplicated (case-insensitively), preserving user
// entries first.
func (p *ProxyConfig) EffectiveHTTPSUpstreamHosts() []string {
	seen := make(map[string]struct{}, len(p.HTTPSUpstreamHosts)+len(DefaultHTTPSUpstreamHosts))
	result := make([]string, 0, len(p.HTTPSUpstreamHosts)+len(DefaultHTTPSUpstreamHosts))
	add := func(hosts []string) {
		for _, h := range hosts {
			key := strings.ToLower(strings.TrimSpace(h))
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, h)
		}
	}
	add(p.HTTPSUpstreamHosts)
	if p.TrustsKnownRepos() {
		add(DefaultHTTPSUpstreamHosts)
	}
	return result
}

// NetworkConfig holds network-related settings
type NetworkConfig struct {
	ListenPort int `toml:"listen_port"`
	ProxyPort  int `toml:"proxy_port"`

	// ProxyBind is the HTTP proxy listen address (default "127.0.0.1", loopback
	// only). Setting a non-loopback address (a LAN interface IP or "0.0.0.0")
	// enables LAN server mode and REQUIRES ProxyAllowedCIDRs (fail-closed).
	ProxyBind string `toml:"proxy_bind"`
	// ProxyAllowedCIDRs lists the client networks (CIDR notation) permitted to
	// use this cache when ProxyBind is non-loopback. Loopback is always allowed.
	ProxyAllowedCIDRs []string `toml:"proxy_allowed_cidrs"`

	MaxConnections int      `toml:"max_connections"`
	BootstrapPeers []string `toml:"bootstrap_peers"`

	// Connectivity detection settings
	ConnectivityMode          string `toml:"connectivity_mode"`           // "auto", "lan_only", "online_only"
	ConnectivityCheckInterval string `toml:"connectivity_check_interval"` // How often to check connectivity
	ConnectivityCheckURL      string `toml:"connectivity_check_url"`      // URL to check for internet access

	// NAT traversal settings
	EnableRelay        *bool `toml:"enable_relay"`         // Use circuit relays to reach NAT'd peers (default: true)
	EnableHolePunching *bool `toml:"enable_hole_punching"` // Enable NAT hole punching (default: true)
}

// GetConnectivityMode returns the connectivity mode with a default of "auto"
func (c *NetworkConfig) GetConnectivityMode() string {
	if c.ConnectivityMode == "" {
		return "auto"
	}
	return c.ConnectivityMode
}

// ParsedAllowedCIDRs parses ProxyAllowedCIDRs into *net.IPNet values, skipping
// empty entries. Validate reports every malformed entry; this returns an error on
// the first one for callers that parse after validation has passed. The result is
// used to gate inbound proxy clients in LAN server mode.
func (c *NetworkConfig) ParsedAllowedCIDRs() ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(c.ProxyAllowedCIDRs))
	for _, cidr := range c.ProxyAllowedCIDRs {
		if cidr == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipnet)
	}
	return nets, nil
}

// isLoopbackBindAddr reports whether a bind address restricts the listener to the
// local host. Empty and "localhost" count as loopback; "0.0.0.0"/"::" (all
// interfaces) and any specific interface IP do not.
func isLoopbackBindAddr(bind string) bool {
	if bind == "" || bind == "localhost" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}

// countNonEmptyStrings returns the number of non-empty entries in s.
func countNonEmptyStrings(s []string) int {
	n := 0
	for _, v := range s {
		if v != "" {
			n++
		}
	}
	return n
}

// GetConnectivityCheckInterval returns the check interval duration.
// Returns 30 seconds default if not configured.
func (c *NetworkConfig) GetConnectivityCheckInterval() time.Duration {
	if c.ConnectivityCheckInterval == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(c.ConnectivityCheckInterval)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

// GetConnectivityCheckURL returns the URL for connectivity checks.
// Returns the default Debian mirror if not configured.
//
// The default probes over plain HTTP (the transport debswarm actually uses to
// fetch packages), so the check reflects mirror reachability rather than TLS
// trust. An HTTPS probe fails on hosts without a CA bundle even when the mirror
// is perfectly reachable, which would mis-report an online node as offline. The
// path must not redirect to HTTPS (deb.debian.org/debian/ does not) or the TLS
// dependency would return through the redirect.
func (c *NetworkConfig) GetConnectivityCheckURL() string {
	if c.ConnectivityCheckURL == "" {
		return "http://deb.debian.org/debian/"
	}
	return c.ConnectivityCheckURL
}

// IsRelayEnabled returns whether circuit relay is enabled.
// Defaults to true if not configured.
func (c *NetworkConfig) IsRelayEnabled() bool {
	if c.EnableRelay == nil {
		return true
	}
	return *c.EnableRelay
}

// IsHolePunchingEnabled returns whether NAT hole punching is enabled.
// Defaults to true if not configured.
func (c *NetworkConfig) IsHolePunchingEnabled() bool {
	if c.EnableHolePunching == nil {
		return true
	}
	return *c.EnableHolePunching
}

// CacheConfig holds cache-related settings
type CacheConfig struct {
	MaxSize      string `toml:"max_size"`
	Path         string `toml:"path"`
	MinFreeSpace string `toml:"min_free_space"`
	// CacheMetadata enables caching of repository metadata (Release/InRelease,
	// Packages/Sources, Translation, Contents, DEP-11) in addition to .deb
	// packages, so a cold client (e.g. a fresh CI container) fetches it from the
	// local cache after a cheap revalidation instead of re-downloading from the
	// WAN. Default: true.
	CacheMetadata *bool `toml:"cache_metadata"`
	// MetadataMaxSize is the disk budget for the metadata cache, kept separate
	// from MaxSize so metadata and packages never evict each other. Default: 1GB.
	MetadataMaxSize string `toml:"metadata_max_size"`
	// ServeStaleMetadata lets the proxy serve cached metadata when the mirror is
	// unreachable (or connectivity is offline) instead of failing the request,
	// so apt-get update keeps working offline. APT still verifies the signature
	// and Valid-Until of whatever is served. Default: true.
	ServeStaleMetadata *bool `toml:"serve_stale_metadata"`
}

// IndexConfig holds package index settings
type IndexConfig struct {
	// APTListsPath is the path to APT's package lists directory (default: /var/lib/apt/lists)
	APTListsPath string `toml:"apt_lists_path"`
	// WatchAPTLists enables watching APT lists for changes (default: true)
	WatchAPTLists *bool `toml:"watch_apt_lists"`
	// APTArchivesPath is the path to APT's package cache (default: /var/cache/apt/archives)
	APTArchivesPath string `toml:"apt_archives_path"`
	// ImportAPTArchives enables importing packages from APT's cache on startup (default: true)
	ImportAPTArchives *bool `toml:"import_apt_archives"`
}

// GetWatchAPTLists returns whether APT lists watching is enabled (default: true)
func (c *IndexConfig) GetWatchAPTLists() bool {
	if c.WatchAPTLists == nil {
		return true
	}
	return *c.WatchAPTLists
}

// GetImportAPTArchives returns whether APT archives import is enabled (default: true)
func (c *IndexConfig) GetImportAPTArchives() bool {
	if c.ImportAPTArchives == nil {
		return true
	}
	return *c.ImportAPTArchives
}

// Upstream signature-verification modes for SecurityConfig.VerifyUpstreamSignatures.
const (
	// VerifyOff disables daemon-side signature verification (pre-1.34 behavior).
	VerifyOff = "off"
	// VerifyWarn verifies but still serves on failure, emitting a metric, a log,
	// and an X-Debswarm-Unverified response header. Behaviorally identical to
	// "off" from APT's perspective (nothing is refused).
	VerifyWarn = "warn"
	// VerifyAuto refuses an index only when verification was possible and it
	// failed — a signature-verified Release exists for the repository but the
	// index does not match it (tampering) — and otherwise behaves like warn
	// (serve + report) when verification could not be attempted at all (no
	// trusted key for the repo, a flat/no-dists repo, or no cached signed
	// Release). This gives real protection for every repository whose signing key
	// is discoverable without breaking repositories that cannot be verified, which
	// is why it is the default.
	VerifyAuto = "auto"
	// VerifyEnforce refuses to parse/serve an index whose Release fails signature
	// or hash verification, including when verification could not be attempted
	// (fail-closed).
	VerifyEnforce = "enforce"
)

// SecurityConfig holds daemon-side upstream signature-verification settings: the
// daemon verifies a repository's GPG-signed Release against a trusted keyring and
// checks each Packages/Sources index against that signed Release, anchoring the
// SHA256 it trusts. See docs/design/upstream-gpg-verification.md.
type SecurityConfig struct {
	// VerifyUpstreamSignatures is one of "off", "warn", "auto", or "enforce".
	// Defaults to "auto" when unset. See the Verify* constants for semantics.
	VerifyUpstreamSignatures string `toml:"verify_upstream_signatures"`

	// KeyringPath is an optional file or directory of additional trusted public
	// keys (binary .gpg or armored .asc), appended to the auto-discovered APT
	// keyrings (/etc/apt/trusted.gpg{,.d}, /usr/share/keyrings, /etc/apt/keyrings).
	// Required in enforce mode on a host whose APT keyrings are empty, e.g. a
	// dedicated cache-server that never runs apt-get update locally.
	KeyringPath string `toml:"keyring_path"`

	// VerifyExemptHosts lists repository hostnames served even when unverifiable.
	// Effective only in the refusing modes (auto, enforce) — an escape hatch for a
	// repo whose signing key cannot be provisioned. Ignored in off/warn (which
	// serve regardless).
	VerifyExemptHosts []string `toml:"verify_exempt_hosts"`
}

// GetVerifyMode returns the normalized verification mode, defaulting to "auto"
// when unset (and for any unrecognized value, which Validate rejects separately).
func (c *SecurityConfig) GetVerifyMode() string {
	switch strings.ToLower(strings.TrimSpace(c.VerifyUpstreamSignatures)) {
	case VerifyOff:
		return VerifyOff
	case VerifyWarn:
		return VerifyWarn
	case VerifyEnforce:
		return VerifyEnforce
	case VerifyAuto:
		return VerifyAuto
	default:
		return VerifyAuto
	}
}

// VerificationEnabled reports whether any verification work runs (mode != off).
func (c *SecurityConfig) VerificationEnabled() bool {
	return c.GetVerifyMode() != VerifyOff
}

// TransferConfig holds transfer-related settings
type TransferConfig struct {
	MaxUploadRate              string `toml:"max_upload_rate"`
	MaxDownloadRate            string `toml:"max_download_rate"`
	MaxConcurrentUploads       int    `toml:"max_concurrent_uploads"`
	MaxConcurrentPeerDownloads int    `toml:"max_concurrent_peer_downloads"`
	// Retry settings for failed downloads
	RetryMaxAttempts int    `toml:"retry_max_attempts"` // Max retry attempts per download (0 = disabled)
	RetryInterval    string `toml:"retry_interval"`     // How often to check for failed downloads
	RetryMaxAge      string `toml:"retry_max_age"`      // Don't retry downloads older than this

	// Per-peer rate limiting
	PerPeerUploadRate   string `toml:"per_peer_upload_rate"`   // "auto", "5MB/s", or "0" (disabled)
	PerPeerDownloadRate string `toml:"per_peer_download_rate"` // "auto", "5MB/s", or "0" (disabled)
	ExpectedPeers       int    `toml:"expected_peers"`         // For auto-calculation (default: 10)

	// Adaptive rate limiting (enabled by default when per-peer is active)
	AdaptiveRateLimiting *bool   `toml:"adaptive_rate_limiting"` // nil = auto (enabled if per-peer active)
	AdaptiveMinRate      string  `toml:"adaptive_min_rate"`      // Minimum rate floor: "100KB/s"
	AdaptiveMaxBoost     float64 `toml:"adaptive_max_boost"`     // Max multiplier: 1.5
}

// DHTConfig holds DHT-related settings
type DHTConfig struct {
	ProviderTTL      string `toml:"provider_ttl"`
	AnnounceInterval string `toml:"announce_interval"`
}

// ProviderTTLDuration returns the parsed provider TTL duration.
// Returns 24h default if parsing fails or value is empty.
func (c *DHTConfig) ProviderTTLDuration() time.Duration {
	if c.ProviderTTL == "" {
		return 24 * time.Hour
	}
	d, err := time.ParseDuration(c.ProviderTTL)
	if err != nil {
		return 24 * time.Hour
	}
	return d
}

// AnnounceIntervalDuration returns the parsed announce interval duration.
// Returns 12h default if parsing fails or value is empty.
func (c *DHTConfig) AnnounceIntervalDuration() time.Duration {
	if c.AnnounceInterval == "" {
		return 12 * time.Hour
	}
	d, err := time.ParseDuration(c.AnnounceInterval)
	if err != nil {
		return 12 * time.Hour
	}
	return d
}

// PrivacyConfig holds privacy-related settings
type PrivacyConfig struct {
	EnableMDNS       bool     `toml:"enable_mdns"`
	AnnouncePackages bool     `toml:"announce_packages"`
	PSKPath          string   `toml:"psk_path"`       // Path to PSK file for private swarm
	PSK              string   `toml:"psk"`            // Inline PSK (hex), mutually exclusive with path
	PeerAllowlist    []string `toml:"peer_allowlist"` // List of allowed peer IDs
	PeerBlocklist    []string `toml:"peer_blocklist"` // List of blocked peer IDs
}

// MetricsConfig holds metrics/monitoring settings
type MetricsConfig struct {
	Port int    `toml:"port"` // Metrics endpoint port (0 to disable)
	Bind string `toml:"bind"` // Metrics endpoint bind address
}

// LoggingConfig holds logging-related settings
type LoggingConfig struct {
	Level string      `toml:"level"`
	File  string      `toml:"file"`
	Audit AuditConfig `toml:"audit"`
}

// AuditConfig holds audit logging settings
type AuditConfig struct {
	Enabled    bool   `toml:"enabled"`     // Enable audit logging (default: false)
	Path       string `toml:"path"`        // Path for JSON audit log file
	MaxSizeMB  int    `toml:"max_size_mb"` // Max file size before rotation (default: 100)
	MaxBackups int    `toml:"max_backups"` // Number of backup files to keep (default: 5)
}

// GetMaxSizeMB returns the max size with a default of 100MB
func (c *AuditConfig) GetMaxSizeMB() int {
	if c.MaxSizeMB <= 0 {
		return 100
	}
	return c.MaxSizeMB
}

// GetMaxBackups returns the max backups with a default of 5
func (c *AuditConfig) GetMaxBackups() int {
	if c.MaxBackups <= 0 {
		return 5
	}
	return c.MaxBackups
}

// SchedulerConfig holds scheduled sync window settings
type SchedulerConfig struct {
	Enabled           bool             `toml:"enabled"`                  // Enable scheduler (default: false)
	Windows           []ScheduleWindow `toml:"windows"`                  // List of sync windows
	Timezone          string           `toml:"timezone"`                 // IANA timezone (e.g., "America/New_York")
	OutsideWindowRate string           `toml:"outside_window_rate"`      // Rate limit outside windows (e.g., "100KB/s")
	InsideWindowRate  string           `toml:"inside_window_rate"`       // Rate limit inside windows (e.g., "unlimited")
	UrgentFullSpeed   *bool            `toml:"urgent_always_full_speed"` // Security updates always get full speed
}

// ScheduleWindow represents a time window for sync operations
type ScheduleWindow struct {
	Days      []string `toml:"days"`       // "monday", "tuesday", etc. or "weekday", "weekend"
	StartTime string   `toml:"start_time"` // "09:00" (24h format)
	EndTime   string   `toml:"end_time"`   // "17:00"
}

// OutsideWindowRateBytes returns the rate limit in bytes/sec for outside windows.
// Returns 100KB/s default if not configured.
func (c *SchedulerConfig) OutsideWindowRateBytes() int64 {
	if c.OutsideWindowRate == "" {
		return 100 * 1024 // 100KB/s default
	}
	rate, err := ParseRate(c.OutsideWindowRate)
	if err != nil {
		return 100 * 1024
	}
	return rate
}

// InsideWindowRateBytes returns the rate limit in bytes/sec for inside windows.
// Returns 0 (unlimited) if not configured or set to "unlimited".
func (c *SchedulerConfig) InsideWindowRateBytes() int64 {
	if c.InsideWindowRate == "" || c.InsideWindowRate == "unlimited" {
		return 0 // unlimited
	}
	rate, err := ParseRate(c.InsideWindowRate)
	if err != nil {
		return 0
	}
	return rate
}

// IsUrgentFullSpeed returns whether security updates should always get full speed.
// Returns true by default.
func (c *SchedulerConfig) IsUrgentFullSpeed() bool {
	if c.UrgentFullSpeed == nil {
		return true // default
	}
	return *c.UrgentFullSpeed
}

// FleetConfig holds fleet coordination settings
type FleetConfig struct {
	Enabled         bool   `toml:"enabled"`          // Enable fleet coordination (default: false)
	ClaimTimeout    string `toml:"claim_timeout"`    // How long to wait for peer to claim WAN download
	MaxWaitTime     string `toml:"max_wait_time"`    // Max wait for peer to finish WAN download
	AllowConcurrent int    `toml:"allow_concurrent"` // Number of concurrent WAN fetchers allowed
	RefreshInterval string `toml:"refresh_interval"` // Progress broadcast interval
}

// ClaimTimeoutDuration returns the claim timeout duration.
// Returns 5 seconds default if not configured.
func (c *FleetConfig) ClaimTimeoutDuration() time.Duration {
	if c.ClaimTimeout == "" {
		return 5 * time.Second
	}
	d, err := time.ParseDuration(c.ClaimTimeout)
	if err != nil {
		return 5 * time.Second
	}
	return d
}

// MaxWaitTimeDuration returns the max wait time duration.
// Returns 5 minutes default if not configured.
func (c *FleetConfig) MaxWaitTimeDuration() time.Duration {
	if c.MaxWaitTime == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(c.MaxWaitTime)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

// RefreshIntervalDuration returns the refresh interval duration.
// Returns 1 second default if not configured.
func (c *FleetConfig) RefreshIntervalDuration() time.Duration {
	if c.RefreshInterval == "" {
		return 1 * time.Second
	}
	d, err := time.ParseDuration(c.RefreshInterval)
	if err != nil {
		return 1 * time.Second
	}
	return d
}

// GetAllowConcurrent returns the number of concurrent WAN fetchers allowed.
// Returns 1 default if not configured.
func (c *FleetConfig) GetAllowConcurrent() int {
	if c.AllowConcurrent <= 0 {
		return 1
	}
	return c.AllowConcurrent
}

// MaxSizeBytes returns the parsed max size in bytes.
// Returns 10GB default if parsing fails or value is 0.
func (c *CacheConfig) MaxSizeBytes() int64 {
	size, err := ParseSize(c.MaxSize)
	if err != nil || size == 0 {
		return 10 * 1024 * 1024 * 1024 // 10GB default
	}
	return size
}

// MinFreeSpaceBytes returns the parsed min free space in bytes.
// Returns 0 if parsing fails (no minimum requirement).
func (c *CacheConfig) MinFreeSpaceBytes() int64 {
	size, err := ParseSize(c.MinFreeSpace)
	if err != nil {
		return 0 // no minimum requirement
	}
	return size
}

// MetadataCachingEnabled reports whether repository-metadata caching is on.
// Default: true.
func (c *CacheConfig) MetadataCachingEnabled() bool {
	if c.CacheMetadata == nil {
		return true
	}
	return *c.CacheMetadata
}

// ServesStaleMetadata reports whether serving stale cached metadata when the
// mirror is unreachable is on. Default: true.
func (c *CacheConfig) ServesStaleMetadata() bool {
	if c.ServeStaleMetadata == nil {
		return true
	}
	return *c.ServeStaleMetadata
}

// MetadataMaxSizeBytes returns the metadata cache disk budget in bytes, or 0
// when metadata caching is disabled. Defaults to 1GB.
func (c *CacheConfig) MetadataMaxSizeBytes() int64 {
	if !c.MetadataCachingEnabled() {
		return 0
	}
	size, err := ParseSize(c.MetadataMaxSize)
	if err != nil || size == 0 {
		return 1 * 1024 * 1024 * 1024 // 1GB default
	}
	return size
}

// MaxUploadRateBytes returns the parsed max upload rate in bytes/sec.
// Returns 0 (unlimited) if parsing fails (should not happen after Validate).
func (c *TransferConfig) MaxUploadRateBytes() int64 {
	rate, err := ParseRate(c.MaxUploadRate)
	if err != nil {
		return 0 // unlimited
	}
	return rate
}

// MaxDownloadRateBytes returns the parsed max download rate in bytes/sec.
// Returns 0 (unlimited) if parsing fails (should not happen after Validate).
func (c *TransferConfig) MaxDownloadRateBytes() int64 {
	rate, err := ParseRate(c.MaxDownloadRate)
	if err != nil {
		return 0 // unlimited
	}
	return rate
}

// RetryIntervalDuration returns the parsed retry interval duration.
// Returns 5 minutes default if parsing fails or value is empty.
func (c *TransferConfig) RetryIntervalDuration() time.Duration {
	if c.RetryInterval == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(c.RetryInterval)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

// RetryMaxAgeDuration returns the parsed retry max age duration.
// Returns 1 hour default if parsing fails or value is empty.
func (c *TransferConfig) RetryMaxAgeDuration() time.Duration {
	if c.RetryMaxAge == "" {
		return 1 * time.Hour
	}
	d, err := time.ParseDuration(c.RetryMaxAge)
	if err != nil {
		return 1 * time.Hour
	}
	return d
}

// PerPeerUploadRateBytes returns the per-peer upload rate in bytes/sec.
// Returns 0 for "auto" (calculate from global/expected_peers) or disabled.
func (c *TransferConfig) PerPeerUploadRateBytes() int64 {
	if c.PerPeerUploadRate == "" || c.PerPeerUploadRate == "auto" {
		return 0 // auto-calculate
	}
	rate, err := ParseRate(c.PerPeerUploadRate)
	if err != nil {
		return 0
	}
	return rate
}

// PerPeerDownloadRateBytes returns the per-peer download rate in bytes/sec.
// Returns 0 for "auto" (calculate from global/expected_peers) or disabled.
func (c *TransferConfig) PerPeerDownloadRateBytes() int64 {
	if c.PerPeerDownloadRate == "" || c.PerPeerDownloadRate == "auto" {
		return 0 // auto-calculate
	}
	rate, err := ParseRate(c.PerPeerDownloadRate)
	if err != nil {
		return 0
	}
	return rate
}

// IsPerPeerEnabled returns whether per-peer rate limiting is enabled.
// It's enabled by default ("auto") unless explicitly set to "0".
func (c *TransferConfig) IsPerPeerEnabled() bool {
	// Disabled if explicitly set to "0"
	if c.PerPeerUploadRate == "0" && c.PerPeerDownloadRate == "0" {
		return false
	}
	// Enabled by default (auto) or if any rate is configured
	return true
}

// IsAdaptiveEnabled returns whether adaptive rate limiting is enabled.
// Enabled by default when per-peer limiting is active, unless explicitly disabled.
func (c *TransferConfig) IsAdaptiveEnabled() bool {
	if c.AdaptiveRateLimiting != nil {
		return *c.AdaptiveRateLimiting
	}
	// Auto: enabled if per-peer is enabled
	return c.IsPerPeerEnabled()
}

// AdaptiveMinRateBytes returns the minimum adaptive rate in bytes/sec.
// Returns 100KB/s default if not configured.
func (c *TransferConfig) AdaptiveMinRateBytes() int64 {
	if c.AdaptiveMinRate == "" {
		return 100 * 1024 // 100KB/s default
	}
	rate, err := ParseRate(c.AdaptiveMinRate)
	if err != nil {
		return 100 * 1024
	}
	return rate
}

// AdaptiveMaxBoostFactor returns the max boost multiplier.
// Returns 1.5 default if not configured or invalid.
func (c *TransferConfig) AdaptiveMaxBoostFactor() float64 {
	if c.AdaptiveMaxBoost <= 0 {
		return 1.5 // default
	}
	if c.AdaptiveMaxBoost > 10 {
		return 10 // cap at 10x
	}
	return c.AdaptiveMaxBoost
}

// GetExpectedPeers returns the expected number of concurrent peers.
// Returns 10 default if not configured.
func (c *TransferConfig) GetExpectedPeers() int {
	if c.ExpectedPeers <= 0 {
		return 10
	}
	return c.ExpectedPeers
}

// DefaultConfig returns a configuration with sensible defaults.
// When running under systemd with CacheDirectory=, the CACHE_DIRECTORY
// environment variable is used automatically.
func DefaultConfig() *Config {
	// Check for systemd environment variable first (set when using CacheDirectory=)
	cachePath := os.Getenv("CACHE_DIRECTORY")
	if cachePath == "" {
		// Fall back to user's home directory
		homeDir, err := os.UserHomeDir()
		if err != nil {
			homeDir = "/tmp" // Fallback for systems without a home directory
		}
		cachePath = filepath.Join(homeDir, ".cache", "debswarm")
	}

	return &Config{
		Network: NetworkConfig{
			ListenPort:     4001,
			ProxyPort:      9977,
			ProxyBind:      "127.0.0.1", // loopback only; non-loopback needs ProxyAllowedCIDRs
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
			Path:         cachePath,
			MinFreeSpace: "1GB",
		},
		Transfer: TransferConfig{
			MaxUploadRate:              "0", // unlimited
			MaxDownloadRate:            "0", // unlimited
			MaxConcurrentUploads:       20,
			MaxConcurrentPeerDownloads: 10,
			RetryInterval:              "5m", // Check for failed downloads every 5 minutes
			RetryMaxAttempts:           3,    // Retry failed downloads up to 3 times
			RetryMaxAge:                "1h", // Don't retry downloads older than 1 hour
			// Per-peer rate limiting (enabled by default with auto-calculation)
			PerPeerUploadRate:   "auto", // global_limit / expected_peers
			PerPeerDownloadRate: "auto", // global_limit / expected_peers
			ExpectedPeers:       10,     // For auto-calculation
			// Adaptive rate limiting (enabled by default when per-peer is active)
			AdaptiveRateLimiting: nil,       // Auto: enabled if per-peer active
			AdaptiveMinRate:      "100KB/s", // Minimum rate floor
			AdaptiveMaxBoost:     1.5,       // Max 1.5x base rate
		},
		DHT: DHTConfig{
			ProviderTTL:      "24h",
			AnnounceInterval: "12h",
		},
		Privacy: PrivacyConfig{
			EnableMDNS:       true,
			AnnouncePackages: true,
		},
		Fleet: FleetConfig{
			// LAN fleet coordination (peer-to-peer download dedup) is on by default:
			// mDNS discovery is already enabled, so nearby nodes find each other, and
			// this lets them actually share packages instead of each fetching from the
			// WAN. Set [fleet] enabled = false for an isolated/no-sharing posture.
			// The remaining fields default via their *Duration() accessors when empty.
			Enabled: true,
		},
		Metrics: MetricsConfig{
			Port: 9978,
			Bind: "127.0.0.1",
		},
		Logging: LoggingConfig{
			Level: "info",
			File:  "",
		},
		Security: SecurityConfig{
			// Default "auto": refuse an index only when a signature-verified Release
			// proves it was tampered with, and otherwise serve-and-report (like warn)
			// when verification cannot be attempted — real protection without breaking
			// unverifiable repos. Operators set "enforce" for strict fail-closed
			// protection, "warn" to only observe, or "off" to disable entirely.
			VerifyUpstreamSignatures: VerifyAuto,
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

	// Systemd environment variables always take precedence over config file
	// This ensures CacheDirectory=/StateDirectory= work correctly
	if cacheDir := os.Getenv("CACHE_DIRECTORY"); cacheDir != "" {
		cfg.Cache.Path = cacheDir
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

// ParseSize parses a size string like "10GB" into bytes.
// Returns an error for empty strings, non-numeric input, or unrecognized units.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}

	var size int64
	var unit string

	n := parseWithUnit(s, &size, &unit)
	if n == 0 {
		return 0, fmt.Errorf("invalid size: no numeric value in %q", s)
	}

	unit = strings.ToUpper(strings.TrimSpace(unit))
	var multiplier int64
	switch unit {
	case "", "B":
		multiplier = 1
	case "KB", "K":
		multiplier = 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	case "TB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("invalid size unit %q in %q", unit, s)
	}

	result := size * multiplier
	// Check for overflow: if size is positive but result is negative or
	// dividing back doesn't yield the original value, overflow occurred
	if size > 0 && (result < 0 || result/multiplier != size) {
		return 0, fmt.Errorf("size value overflows int64 in %q", s)
	}
	return result, nil
}

func parseWithUnit(s string, size *int64, unit *string) int {
	// Find the end of the numeric prefix
	var n int
	for i, c := range s {
		if c >= '0' && c <= '9' {
			n = i + 1
		} else {
			break
		}
	}
	if n > 0 {
		// Use strconv.ParseInt for overflow-safe parsing
		v, err := strconv.ParseInt(s[:n], 10, 64)
		if err != nil {
			// Overflow or other parse error — return 0 digits consumed
			return 0
		}
		*size = v
	}
	*unit = s[n:]
	return n
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

	// Check world-writable FIRST (more severe than world-readable).
	// Previously, world-readable was checked first and returned early,
	// which meant a file that was both world-readable AND world-writable
	// would only report the less severe warning.
	// Bits: -----rwx (world), --rwx--- (group), rwx------ (owner)
	if mode&0002 != 0 { // world writable
		return &SecurityWarning{
			Message: fmt.Sprintf("config file is world-writable (mode %04o); this is a security risk", mode),
			File:    path,
		}
	}

	if mode&0004 != 0 { // world readable
		return &SecurityWarning{
			Message: fmt.Sprintf("config file is world-readable (mode %04o); consider 'chmod 600 %s' for files with inline PSK", mode, path),
			File:    path,
		}
	}

	return nil
}

// ValidationError represents a configuration validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("config validation failed: %s: %s", e.Field, e.Message)
}

// ValidationErrors collects multiple validation errors
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return ""
	}
	if len(e) == 1 {
		return e[0].Error()
	}
	msgs := make([]string, 0, len(e))
	for _, err := range e {
		msgs = append(msgs, fmt.Sprintf("  - %s: %s", err.Field, err.Message))
	}
	return fmt.Sprintf("config validation failed with %d errors:\n%s", len(e), strings.Join(msgs, "\n"))
}

// Validate checks configuration for errors and returns all validation failures.
// This should be called at startup to fail fast on invalid configuration.
func (c *Config) Validate() error {
	var errs ValidationErrors

	// Validate bootstrap peers
	for i, addr := range c.Network.BootstrapPeers {
		if addr == "" {
			continue
		}
		_, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("network.bootstrap_peers[%d]", i),
				Message: fmt.Sprintf("invalid multiaddr %q: %v", addr, err),
			})
		}
	}

	// Validate port numbers
	if c.Network.ListenPort < 1 || c.Network.ListenPort > 65535 {
		errs = append(errs, ValidationError{
			Field:   "network.listen_port",
			Message: fmt.Sprintf("must be between 1 and 65535, got %d", c.Network.ListenPort),
		})
	}
	if c.Network.ProxyPort < 1 || c.Network.ProxyPort > 65535 {
		errs = append(errs, ValidationError{
			Field:   "network.proxy_port",
			Message: fmt.Sprintf("must be between 1 and 65535, got %d", c.Network.ProxyPort),
		})
	}

	// Validate proxy bind address + client allowlist (LAN server mode).
	if c.Network.ProxyBind != "" && c.Network.ProxyBind != "localhost" && net.ParseIP(c.Network.ProxyBind) == nil {
		errs = append(errs, ValidationError{
			Field:   "network.proxy_bind",
			Message: fmt.Sprintf("invalid bind address %q; must be an IP address (e.g. 127.0.0.1, 0.0.0.0, or a LAN interface IP)", c.Network.ProxyBind),
		})
	}
	for i, cidr := range c.Network.ProxyAllowedCIDRs {
		if cidr == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("network.proxy_allowed_cidrs[%d]", i),
				Message: fmt.Sprintf("invalid CIDR %q: %v", cidr, err),
			})
		}
	}
	// Fail-closed: a non-loopback proxy bind exposes the cache to the network, so
	// it must be paired with an explicit client allowlist. Refuse to start
	// otherwise rather than silently serving every reachable host.
	if !isLoopbackBindAddr(c.Network.ProxyBind) && countNonEmptyStrings(c.Network.ProxyAllowedCIDRs) == 0 {
		errs = append(errs, ValidationError{
			Field:   "network.proxy_allowed_cidrs",
			Message: fmt.Sprintf("proxy_bind %q is not loopback; set proxy_allowed_cidrs to the client network(s) allowed to use this cache (e.g. [\"192.168.1.0/24\"])", c.Network.ProxyBind),
		})
	}

	// Validate cache settings
	if c.Cache.MaxSize != "" {
		if _, err := ParseSize(c.Cache.MaxSize); err != nil {
			errs = append(errs, ValidationError{
				Field:   "cache.max_size",
				Message: fmt.Sprintf("invalid size %q: %v", c.Cache.MaxSize, err),
			})
		}
	}
	if c.Cache.MinFreeSpace != "" {
		if _, err := ParseSize(c.Cache.MinFreeSpace); err != nil {
			errs = append(errs, ValidationError{
				Field:   "cache.min_free_space",
				Message: fmt.Sprintf("invalid size %q: %v", c.Cache.MinFreeSpace, err),
			})
		}
	}

	// Validate rate limits
	if c.Transfer.MaxUploadRate != "" {
		if _, err := ParseRate(c.Transfer.MaxUploadRate); err != nil {
			errs = append(errs, ValidationError{
				Field:   "transfer.max_upload_rate",
				Message: fmt.Sprintf("invalid rate %q: %v", c.Transfer.MaxUploadRate, err),
			})
		}
	}
	if c.Transfer.MaxDownloadRate != "" {
		if _, err := ParseRate(c.Transfer.MaxDownloadRate); err != nil {
			errs = append(errs, ValidationError{
				Field:   "transfer.max_download_rate",
				Message: fmt.Sprintf("invalid rate %q: %v", c.Transfer.MaxDownloadRate, err),
			})
		}
	}

	// Validate per-peer rate limits
	if c.Transfer.PerPeerUploadRate != "" && c.Transfer.PerPeerUploadRate != "auto" && c.Transfer.PerPeerUploadRate != "0" {
		if _, err := ParseRate(c.Transfer.PerPeerUploadRate); err != nil {
			errs = append(errs, ValidationError{
				Field:   "transfer.per_peer_upload_rate",
				Message: fmt.Sprintf("invalid rate %q: must be 'auto', '0', or a rate like '5MB/s'", c.Transfer.PerPeerUploadRate),
			})
		}
	}
	if c.Transfer.PerPeerDownloadRate != "" && c.Transfer.PerPeerDownloadRate != "auto" && c.Transfer.PerPeerDownloadRate != "0" {
		if _, err := ParseRate(c.Transfer.PerPeerDownloadRate); err != nil {
			errs = append(errs, ValidationError{
				Field:   "transfer.per_peer_download_rate",
				Message: fmt.Sprintf("invalid rate %q: must be 'auto', '0', or a rate like '5MB/s'", c.Transfer.PerPeerDownloadRate),
			})
		}
	}

	// Validate adaptive min rate
	if c.Transfer.AdaptiveMinRate != "" {
		if _, err := ParseRate(c.Transfer.AdaptiveMinRate); err != nil {
			errs = append(errs, ValidationError{
				Field:   "transfer.adaptive_min_rate",
				Message: fmt.Sprintf("invalid rate %q: %v", c.Transfer.AdaptiveMinRate, err),
			})
		}
	}

	// Validate adaptive max boost
	if c.Transfer.AdaptiveMaxBoost < 0 {
		errs = append(errs, ValidationError{
			Field:   "transfer.adaptive_max_boost",
			Message: fmt.Sprintf("must be non-negative, got %v", c.Transfer.AdaptiveMaxBoost),
		})
	}

	// Validate PSK configuration (mutually exclusive)
	if c.Privacy.PSKPath != "" && c.Privacy.PSK != "" {
		errs = append(errs, ValidationError{
			Field:   "privacy.psk/psk_path",
			Message: "psk and psk_path are mutually exclusive; use only one",
		})
	}

	// Validate metrics port
	if c.Metrics.Port < 0 || c.Metrics.Port > 65535 {
		errs = append(errs, ValidationError{
			Field:   "metrics.port",
			Message: fmt.Sprintf("must be between 0 and 65535, got %d", c.Metrics.Port),
		})
	}

	// Validate log level
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true, "": true}
	if !validLevels[strings.ToLower(c.Logging.Level)] {
		errs = append(errs, ValidationError{
			Field:   "logging.level",
			Message: fmt.Sprintf("invalid level %q; must be debug, info, warn, or error", c.Logging.Level),
		})
	}

	// Validate connectivity mode
	validModes := map[string]bool{"auto": true, "lan_only": true, "online_only": true, "": true}
	if !validModes[c.Network.ConnectivityMode] {
		errs = append(errs, ValidationError{
			Field:   "network.connectivity_mode",
			Message: fmt.Sprintf("invalid mode %q; must be auto, lan_only, or online_only", c.Network.ConnectivityMode),
		})
	}

	// Validate connectivity check interval
	if c.Network.ConnectivityCheckInterval != "" {
		if _, err := time.ParseDuration(c.Network.ConnectivityCheckInterval); err != nil {
			errs = append(errs, ValidationError{
				Field:   "network.connectivity_check_interval",
				Message: fmt.Sprintf("invalid duration %q: %v", c.Network.ConnectivityCheckInterval, err),
			})
		}
	}

	// Validate audit config
	if c.Logging.Audit.Enabled && c.Logging.Audit.Path == "" {
		errs = append(errs, ValidationError{
			Field:   "logging.audit.path",
			Message: "audit log path is required when audit logging is enabled",
		})
	}
	if c.Logging.Audit.MaxSizeMB < 0 {
		errs = append(errs, ValidationError{
			Field:   "logging.audit.max_size_mb",
			Message: fmt.Sprintf("must be non-negative, got %d", c.Logging.Audit.MaxSizeMB),
		})
	}
	if c.Logging.Audit.MaxBackups < 0 {
		errs = append(errs, ValidationError{
			Field:   "logging.audit.max_backups",
			Message: fmt.Sprintf("must be non-negative, got %d", c.Logging.Audit.MaxBackups),
		})
	}

	// Validate scheduler config
	if c.Scheduler.Enabled {
		if c.Scheduler.Timezone != "" {
			if _, err := time.LoadLocation(c.Scheduler.Timezone); err != nil {
				errs = append(errs, ValidationError{
					Field:   "scheduler.timezone",
					Message: fmt.Sprintf("invalid timezone %q: %v", c.Scheduler.Timezone, err),
				})
			}
		}
		if c.Scheduler.OutsideWindowRate != "" && c.Scheduler.OutsideWindowRate != "unlimited" {
			if _, err := ParseRate(c.Scheduler.OutsideWindowRate); err != nil {
				errs = append(errs, ValidationError{
					Field:   "scheduler.outside_window_rate",
					Message: fmt.Sprintf("invalid rate %q: %v", c.Scheduler.OutsideWindowRate, err),
				})
			}
		}
		if c.Scheduler.InsideWindowRate != "" && c.Scheduler.InsideWindowRate != "unlimited" {
			if _, err := ParseRate(c.Scheduler.InsideWindowRate); err != nil {
				errs = append(errs, ValidationError{
					Field:   "scheduler.inside_window_rate",
					Message: fmt.Sprintf("invalid rate %q: %v", c.Scheduler.InsideWindowRate, err),
				})
			}
		}
	}

	// Validate fleet config
	if c.Fleet.Enabled {
		if c.Fleet.ClaimTimeout != "" {
			if _, err := time.ParseDuration(c.Fleet.ClaimTimeout); err != nil {
				errs = append(errs, ValidationError{
					Field:   "fleet.claim_timeout",
					Message: fmt.Sprintf("invalid duration %q: %v", c.Fleet.ClaimTimeout, err),
				})
			}
		}
		if c.Fleet.MaxWaitTime != "" {
			if _, err := time.ParseDuration(c.Fleet.MaxWaitTime); err != nil {
				errs = append(errs, ValidationError{
					Field:   "fleet.max_wait_time",
					Message: fmt.Sprintf("invalid duration %q: %v", c.Fleet.MaxWaitTime, err),
				})
			}
		}
		if c.Fleet.RefreshInterval != "" {
			if _, err := time.ParseDuration(c.Fleet.RefreshInterval); err != nil {
				errs = append(errs, ValidationError{
					Field:   "fleet.refresh_interval",
					Message: fmt.Sprintf("invalid duration %q: %v", c.Fleet.RefreshInterval, err),
				})
			}
		}
	}

	// Validate upstream signature-verification settings.
	if v := strings.ToLower(strings.TrimSpace(c.Security.VerifyUpstreamSignatures)); v != "" &&
		v != VerifyOff && v != VerifyWarn && v != VerifyAuto && v != VerifyEnforce {
		errs = append(errs, ValidationError{
			Field:   "security.verify_upstream_signatures",
			Message: fmt.Sprintf("must be one of \"off\", \"warn\", \"auto\", or \"enforce\", got %q", c.Security.VerifyUpstreamSignatures),
		})
	}
	// An explicit keyring_path that does not exist is an operator mistake — fail
	// rather than silently verifying against fewer keys than intended.
	if c.Security.KeyringPath != "" {
		if _, err := os.Stat(c.Security.KeyringPath); err != nil {
			errs = append(errs, ValidationError{
				Field:   "security.keyring_path",
				Message: fmt.Sprintf("keyring path %q is not accessible: %v", c.Security.KeyringPath, err),
			})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}
