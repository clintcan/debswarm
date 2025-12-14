// Package dashboard provides a web UI for monitoring debswarm
package dashboard

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"time"
)

// Stats contains all dashboard statistics
type Stats struct {
	// Overview
	Uptime          string  `json:"uptime"`
	StartTime       string  `json:"start_time"`
	PeerID          string  `json:"peer_id"`
	Version         string  `json:"version"`
	P2PRatioPercent float64 `json:"p2p_ratio_percent"`

	// Request stats
	RequestsTotal  int64 `json:"requests_total"`
	RequestsP2P    int64 `json:"requests_p2p"`
	RequestsMirror int64 `json:"requests_mirror"`
	CacheHits      int64 `json:"cache_hits"`

	// Bytes stats
	BytesFromP2P    int64  `json:"bytes_from_p2p"`
	BytesFromMirror int64  `json:"bytes_from_mirror"`
	BytesFromP2PStr string `json:"bytes_from_p2p_str"`
	BytesFromMirStr string `json:"bytes_from_mirror_str"`

	// Cache stats
	CacheSizeBytes    int64   `json:"cache_size_bytes"`
	CacheSizeStr      string  `json:"cache_size_str"`
	CacheCount        int     `json:"cache_count"`
	CacheMaxSize      string  `json:"cache_max_size"`
	CacheUsagePercent float64 `json:"cache_usage_percent"`

	// Network stats
	ConnectedPeers   int `json:"connected_peers"`
	RoutingTableSize int `json:"routing_table_size"`
	ActiveDownloads  int `json:"active_downloads"`
	ActiveUploads    int `json:"active_uploads"`

	// Rate limits
	MaxUploadRate   string `json:"max_upload_rate"`
	MaxDownloadRate string `json:"max_download_rate"`

	// Recent activity
	RecentDownloads []RecentDownload `json:"recent_downloads"`
}

// RecentDownload represents a recent download entry
type RecentDownload struct {
	Time     string `json:"time"`
	Filename string `json:"filename"`
	Size     string `json:"size"`
	Source   string `json:"source"`
	Duration string `json:"duration"`
}

// PeerInfo contains information about a connected peer
type PeerInfo struct {
	ID          string  `json:"id"`
	ShortID     string  `json:"short_id"`
	Score       float64 `json:"score"`
	Category    string  `json:"category"`
	Latency     string  `json:"latency"`
	Throughput  string  `json:"throughput"`
	Downloaded  string  `json:"downloaded"`
	Uploaded    string  `json:"uploaded"`
	LastSeen    string  `json:"last_seen"`
	Blacklisted bool    `json:"blacklisted"`
}

// StatsProvider is a function that returns current stats
type StatsProvider func() *Stats

// PeersProvider is a function that returns peer information
type PeersProvider func() []PeerInfo

// Dashboard handles the web dashboard
type Dashboard struct {
	template      *template.Template
	getStats      StatsProvider
	getPeers      PeersProvider
	startTime     time.Time
	version       string
	peerID        string
	maxUploadRate string
	maxDownRate   string

	// Recent downloads tracking
	recentMu  sync.RWMutex
	recentDLs []RecentDownload
	maxRecent int
}

// Config holds dashboard configuration
type Config struct {
	Version         string
	PeerID          string
	MaxUploadRate   string
	MaxDownloadRate string
}

// New creates a new Dashboard
func New(cfg *Config, statsProvider StatsProvider, peersProvider PeersProvider) *Dashboard {
	d := &Dashboard{
		getStats:      statsProvider,
		getPeers:      peersProvider,
		startTime:     time.Now(),
		version:       cfg.Version,
		peerID:        cfg.PeerID,
		maxUploadRate: cfg.MaxUploadRate,
		maxDownRate:   cfg.MaxDownloadRate,
		recentDLs:     make([]RecentDownload, 0, 50),
		maxRecent:     50,
	}

	// Parse embedded template
	d.template = template.Must(template.New("dashboard").Parse(dashboardHTML))

	return d
}

// RecordDownload records a completed download for the recent activity list
func (d *Dashboard) RecordDownload(filename string, size int64, source string, duration time.Duration) {
	d.recentMu.Lock()
	defer d.recentMu.Unlock()

	dl := RecentDownload{
		Time:     time.Now().Format("15:04:05"),
		Filename: truncateFilename(filename, 40),
		Size:     formatBytes(size),
		Source:   sanitizeForCSS(source), // Sanitize for safe CSS class usage
		Duration: formatDuration(duration),
	}

	// Prepend to list (newest first)
	d.recentDLs = append([]RecentDownload{dl}, d.recentDLs...)

	// Trim to max
	if len(d.recentDLs) > d.maxRecent {
		d.recentDLs = d.recentDLs[:d.maxRecent]
	}
}

// Handler returns the HTTP handler for the dashboard
func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.handleDashboard)
	mux.HandleFunc("/api/stats", d.handleAPIStats)
	mux.HandleFunc("/api/peers", d.handleAPIPeers)
	return mux
}

func (d *Dashboard) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/dashboard" {
		http.NotFound(w, r)
		return
	}

	stats := d.getStats()
	if stats == nil {
		stats = &Stats{}
	}

	// Add dashboard-specific fields
	stats.Uptime = formatDuration(time.Since(d.startTime))
	stats.StartTime = d.startTime.Format("2006-01-02 15:04:05")
	stats.PeerID = d.peerID
	stats.Version = d.version
	stats.MaxUploadRate = d.maxUploadRate
	stats.MaxDownloadRate = d.maxDownRate

	// Add recent downloads
	d.recentMu.RLock()
	stats.RecentDownloads = make([]RecentDownload, len(d.recentDLs))
	copy(stats.RecentDownloads, d.recentDLs)
	d.recentMu.RUnlock()

	// Format byte values
	stats.BytesFromP2PStr = formatBytes(stats.BytesFromP2P)
	stats.BytesFromMirStr = formatBytes(stats.BytesFromMirror)
	stats.CacheSizeStr = formatBytes(stats.CacheSizeBytes)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := d.template.Execute(w, stats); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (d *Dashboard) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	stats := d.getStats()
	if stats == nil {
		stats = &Stats{}
	}

	stats.Uptime = formatDuration(time.Since(d.startTime))
	stats.PeerID = d.peerID
	stats.Version = d.version

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		http.Error(w, "Failed to encode stats", http.StatusInternalServerError)
		return
	}
}

func (d *Dashboard) handleAPIPeers(w http.ResponseWriter, r *http.Request) {
	peers := d.getPeers()
	if peers == nil {
		peers = []PeerInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(peers); err != nil {
		http.Error(w, "Failed to encode peers", http.StatusInternalServerError)
		return
	}
}

// Helper functions

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

func truncateFilename(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// sanitizeForCSS ensures a string is safe to use in a CSS class name
// Only allows alphanumeric characters and hyphens
func sanitizeForCSS(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			result = append(result, c)
		}
	}
	if len(result) == 0 {
		return "unknown"
	}
	return string(result)
}

// Embedded HTML template
const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta http-equiv="refresh" content="5">
    <title>Debswarm Dashboard</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
            background: #0d1117;
            color: #c9d1d9;
            line-height: 1.5;
            padding: 20px;
        }
        .container { max-width: 1400px; margin: 0 auto; }
        header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 24px;
            padding-bottom: 16px;
            border-bottom: 1px solid #30363d;
        }
        h1 { font-size: 24px; font-weight: 600; color: #f0f6fc; }
        .version { color: #8b949e; font-size: 14px; }
        .peer-id { font-family: monospace; font-size: 12px; color: #8b949e; }
        .grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
            gap: 16px;
            margin-bottom: 24px;
        }
        .card {
            background: #161b22;
            border: 1px solid #30363d;
            border-radius: 6px;
            padding: 16px;
        }
        .card h2 {
            font-size: 14px;
            font-weight: 600;
            color: #8b949e;
            text-transform: uppercase;
            letter-spacing: 0.5px;
            margin-bottom: 12px;
        }
        .stat-row {
            display: flex;
            justify-content: space-between;
            padding: 8px 0;
            border-bottom: 1px solid #21262d;
        }
        .stat-row:last-child { border-bottom: none; }
        .stat-label { color: #8b949e; }
        .stat-value { font-weight: 500; color: #f0f6fc; font-family: monospace; }
        .stat-value.highlight { color: #58a6ff; }
        .stat-value.success { color: #3fb950; }
        .stat-value.warning { color: #d29922; }
        .progress-bar {
            height: 8px;
            background: #21262d;
            border-radius: 4px;
            overflow: hidden;
            margin-top: 8px;
        }
        .progress-fill {
            height: 100%;
            background: linear-gradient(90deg, #238636, #3fb950);
            transition: width 0.3s ease;
        }
        table {
            width: 100%;
            border-collapse: collapse;
            font-size: 13px;
        }
        th, td {
            text-align: left;
            padding: 8px 12px;
            border-bottom: 1px solid #21262d;
        }
        th {
            color: #8b949e;
            font-weight: 500;
            text-transform: uppercase;
            font-size: 11px;
            letter-spacing: 0.5px;
        }
        td { color: #c9d1d9; }
        tr:hover { background: #21262d; }
        .source-peer { color: #3fb950; }
        .source-mirror { color: #d29922; }
        .source-cache { color: #58a6ff; }
        .empty-state {
            text-align: center;
            padding: 24px;
            color: #8b949e;
        }
        @media (max-width: 768px) {
            .grid { grid-template-columns: 1fr; }
        }
    </style>
</head>
<body>
    <div class="container">
        <header>
            <div>
                <h1>debswarm</h1>
                <div class="peer-id">{{.PeerID}}</div>
            </div>
            <div class="version">v{{.Version}} | Uptime: {{.Uptime}}</div>
        </header>

        <div class="grid">
            <div class="card">
                <h2>Overview</h2>
                <div class="stat-row">
                    <span class="stat-label">P2P Ratio</span>
                    <span class="stat-value highlight">{{printf "%.1f" .P2PRatioPercent}}%</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Total Requests</span>
                    <span class="stat-value">{{.RequestsTotal}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">From P2P</span>
                    <span class="stat-value success">{{.RequestsP2P}} ({{.BytesFromP2PStr}})</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">From Mirror</span>
                    <span class="stat-value warning">{{.RequestsMirror}} ({{.BytesFromMirStr}})</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Cache Hits</span>
                    <span class="stat-value">{{.CacheHits}}</span>
                </div>
            </div>

            <div class="card">
                <h2>Cache</h2>
                <div class="stat-row">
                    <span class="stat-label">Size</span>
                    <span class="stat-value">{{.CacheSizeStr}} / {{.CacheMaxSize}}</span>
                </div>
                <div class="progress-bar">
                    <div class="progress-fill" style="width: {{printf "%.1f" .CacheUsagePercent}}%"></div>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Packages</span>
                    <span class="stat-value">{{.CacheCount}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Usage</span>
                    <span class="stat-value">{{printf "%.1f" .CacheUsagePercent}}%</span>
                </div>
            </div>

            <div class="card">
                <h2>Network</h2>
                <div class="stat-row">
                    <span class="stat-label">Connected Peers</span>
                    <span class="stat-value highlight">{{.ConnectedPeers}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">DHT Routing Table</span>
                    <span class="stat-value">{{.RoutingTableSize}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Active Downloads</span>
                    <span class="stat-value">{{.ActiveDownloads}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Active Uploads</span>
                    <span class="stat-value">{{.ActiveUploads}}</span>
                </div>
            </div>

            <div class="card">
                <h2>Rate Limits</h2>
                <div class="stat-row">
                    <span class="stat-label">Max Upload</span>
                    <span class="stat-value">{{if .MaxUploadRate}}{{.MaxUploadRate}}{{else}}Unlimited{{end}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Max Download</span>
                    <span class="stat-value">{{if .MaxDownloadRate}}{{.MaxDownloadRate}}{{else}}Unlimited{{end}}</span>
                </div>
            </div>
        </div>

        <div class="card">
            <h2>Recent Downloads</h2>
            {{if .RecentDownloads}}
            <table>
                <thead>
                    <tr>
                        <th>Time</th>
                        <th>Package</th>
                        <th>Size</th>
                        <th>Source</th>
                        <th>Duration</th>
                    </tr>
                </thead>
                <tbody>
                    {{range .RecentDownloads}}
                    <tr>
                        <td>{{.Time}}</td>
                        <td>{{.Filename}}</td>
                        <td>{{.Size}}</td>
                        <td class="source-{{.Source}}">{{.Source}}</td>
                        <td>{{.Duration}}</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
            {{else}}
            <div class="empty-state">No recent downloads</div>
            {{end}}
        </div>
    </div>
</body>
</html>`
