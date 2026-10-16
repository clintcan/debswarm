// Package dashboard provides a web UI for monitoring debswarm
package dashboard

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
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

	// Peers
	Peers []PeerInfo `json:"peers"`

	// Errors
	VerificationFailures int64 `json:"verification_failures"`
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
		maxUploadRate: displayRate(cfg.MaxUploadRate),
		maxDownRate:   displayRate(cfg.MaxDownloadRate),
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

// templateData wraps Stats with extra template fields.
// Embedding *Stats preserves all existing {{.Field}} references.
type templateData struct {
	*Stats
	Nonce string
}

// generateNonce creates a cryptographically random base64-encoded nonce for CSP.
// Uses RawURLEncoding to avoid '+' and '/' which Go's html/template escapes.
func generateNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Handler returns the HTTP handler for the dashboard
func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.handleDashboard)
	mux.HandleFunc("/api/stats", d.handleAPIStats)
	mux.HandleFunc("/api/peers", d.handleAPIPeers)
	return securityHeadersMiddleware(mux)
}

// securityHeadersMiddleware adds security headers to all responses
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")
		// Disable caching for sensitive data
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		// XSS protection (legacy but still useful)
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		// Referrer policy
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// Content Security Policy - restrict resource loading
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'none'; img-src 'self' data:; frame-ancestors 'none'")

		next.ServeHTTP(w, r)
	})
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

	d.populate(stats)
	stats.StartTime = d.startTime.Format("2006-01-02 15:04:05")

	// Format byte values
	stats.BytesFromP2PStr = formatBytes(stats.BytesFromP2P)
	stats.BytesFromMirStr = formatBytes(stats.BytesFromMirror)
	stats.CacheSizeStr = formatBytes(stats.CacheSizeBytes)

	// Generate nonce for inline script CSP
	nonce := generateNonce()

	// Override middleware CSP to allow our nonced inline script
	w.Header().Set("Content-Security-Policy",
		fmt.Sprintf("default-src 'self'; style-src 'unsafe-inline'; script-src 'nonce-%s'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'", nonce))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := &templateData{Stats: stats, Nonce: nonce}
	if err := d.template.Execute(w, data); err != nil {
		// SECURITY: Don't expose internal error details to clients
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (d *Dashboard) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	stats := d.getStats()
	if stats == nil {
		stats = &Stats{}
	}

	d.populate(stats)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		http.Error(w, "Failed to encode stats", http.StatusInternalServerError)
		return
	}
}

// populate adds the dashboard-owned fields to stats. The HTML page and the JSON
// API share it so the page's live polling can refresh the recent-downloads and
// peers tables, not just the counters.
func (d *Dashboard) populate(stats *Stats) {
	stats.Uptime = formatDuration(time.Since(d.startTime))
	stats.PeerID = d.peerID
	stats.Version = d.version
	stats.MaxUploadRate = d.maxUploadRate
	stats.MaxDownloadRate = d.maxDownRate

	d.recentMu.RLock()
	stats.RecentDownloads = make([]RecentDownload, len(d.recentDLs))
	copy(stats.RecentDownloads, d.recentDLs)
	d.recentMu.RUnlock()

	stats.Peers = []PeerInfo{}
	if d.getPeers != nil {
		if p := d.getPeers(); p != nil {
			stats.Peers = p
		}
	}
}

// displayRate maps a configured rate limit to its display form: "0" (the config
// value for no limit) shows as unlimited, like an unset one.
func displayRate(rate string) string {
	if strings.TrimSpace(rate) == "0" {
		return ""
	}
	return rate
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
    <noscript><meta http-equiv="refresh" content="5"><style>.chart-grid{display:none}</style></noscript>
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
        .stat-value.error { color: #f85149; }
        .score-excellent { color: #3fb950; }
        .score-good { color: #58a6ff; }
        .score-fair { color: #d29922; }
        .score-poor { color: #f85149; }
        .blacklisted { color: #f85149; text-decoration: line-through; }
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
        .source-mixed { color: #a371f7; }
        .empty-state {
            text-align: center;
            padding: 24px;
            color: #8b949e;
        }
        .chart-grid {
            display: grid;
            grid-template-columns: repeat(2, 1fr);
            gap: 16px;
            margin-bottom: 24px;
        }
        .chart-grid canvas {
            width: 100%;
            height: 180px;
            display: block;
        }
        .chart-legend {
            display: flex;
            gap: 16px;
            margin-top: 8px;
            font-size: 12px;
            color: #8b949e;
        }
        .legend-color {
            display: inline-block;
            width: 12px;
            height: 12px;
            border-radius: 2px;
            margin-right: 4px;
            vertical-align: middle;
        }
        @media (max-width: 768px) {
            .grid { grid-template-columns: 1fr; }
            .chart-grid { grid-template-columns: 1fr; }
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
            <div class="version">v{{.Version}} | Uptime: <span id="stat-uptime">{{.Uptime}}</span></div>
        </header>

        <div class="grid">
            <div class="card">
                <h2>Overview</h2>
                <div class="stat-row">
                    <span class="stat-label">P2P Ratio</span>
                    <span class="stat-value highlight" id="stat-p2p-ratio">{{printf "%.1f" .P2PRatioPercent}}%</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Total Requests</span>
                    <span class="stat-value" id="stat-requests-total">{{.RequestsTotal}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">From P2P</span>
                    <span class="stat-value success" id="stat-from-p2p">{{.RequestsP2P}} ({{.BytesFromP2PStr}})</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">From Mirror</span>
                    <span class="stat-value warning" id="stat-from-mirror">{{.RequestsMirror}} ({{.BytesFromMirStr}})</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Cache Hits</span>
                    <span class="stat-value" id="stat-cache-hits">{{.CacheHits}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Verification Failures</span>
                    <span class="stat-value{{if gt .VerificationFailures 0}} error{{end}}" id="stat-verify-failures">{{.VerificationFailures}}</span>
                </div>
            </div>

            <div class="card">
                <h2>Cache</h2>
                <div class="stat-row">
                    <span class="stat-label">Size</span>
                    <span class="stat-value" id="stat-cache-size">{{.CacheSizeStr}} / {{.CacheMaxSize}}</span>
                </div>
                <div class="progress-bar">
                    <div class="progress-fill" id="stat-cache-progress" style="width: {{printf "%.1f" .CacheUsagePercent}}%"></div>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Packages</span>
                    <span class="stat-value" id="stat-cache-count">{{.CacheCount}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Usage</span>
                    <span class="stat-value" id="stat-cache-usage">{{printf "%.1f" .CacheUsagePercent}}%</span>
                </div>
            </div>

            <div class="card">
                <h2>Network</h2>
                <div class="stat-row">
                    <span class="stat-label">Connected Peers</span>
                    <span class="stat-value highlight" id="stat-connected-peers">{{.ConnectedPeers}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">DHT Routing Table</span>
                    <span class="stat-value" id="stat-routing-table">{{.RoutingTableSize}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Active Downloads</span>
                    <span class="stat-value" id="stat-active-downloads">{{.ActiveDownloads}}</span>
                </div>
                <div class="stat-row">
                    <span class="stat-label">Active Uploads</span>
                    <span class="stat-value" id="stat-active-uploads">{{.ActiveUploads}}</span>
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

        <div class="chart-grid">
            <div class="card">
                <h2>Throughput</h2>
                <canvas id="chart-throughput"></canvas>
                <div class="chart-legend">
                    <span><span class="legend-color" style="background:#3fb950"></span>P2P</span>
                    <span><span class="legend-color" style="background:#d29922"></span>Mirror</span>
                </div>
            </div>
            <div class="card">
                <h2>Request Rate</h2>
                <canvas id="chart-requests"></canvas>
                <div class="chart-legend">
                    <span><span class="legend-color" style="background:#58a6ff"></span>Requests/sec</span>
                </div>
            </div>
            <div class="card">
                <h2>P2P Ratio</h2>
                <canvas id="chart-p2p-ratio"></canvas>
                <div class="chart-legend">
                    <span><span class="legend-color" style="background:#3fb950"></span>P2P %</span>
                </div>
            </div>
            <div class="card">
                <h2>Connected Peers</h2>
                <canvas id="chart-peers"></canvas>
                <div class="chart-legend">
                    <span><span class="legend-color" style="background:#58a6ff"></span>Peers</span>
                </div>
            </div>
        </div>

        <div class="card">
            <h2>Recent Downloads</h2>
            <div id="recent-downloads">
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

        <div class="card">
            <h2>Connected Peers</h2>
            <div id="peer-table">
            {{if .Peers}}
            <table>
                <thead>
                    <tr>
                        <th>Peer ID</th>
                        <th>Score</th>
                        <th>Latency</th>
                        <th>Throughput</th>
                        <th>Downloaded</th>
                        <th>Uploaded</th>
                        <th>Last Seen</th>
                    </tr>
                </thead>
                <tbody>
                    {{range .Peers}}
                    <tr{{if .Blacklisted}} class="blacklisted"{{end}}>
                        <td title="{{.ID}}">{{.ShortID}}</td>
                        <td class="score-{{.Category}}">{{printf "%.1f" .Score}}</td>
                        <td>{{.Latency}}</td>
                        <td>{{.Throughput}}</td>
                        <td>{{.Downloaded}}</td>
                        <td>{{.Uploaded}}</td>
                        <td>{{.LastSeen}}</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
            {{else}}
            <div class="empty-state">No connected peers</div>
            {{end}}
            </div>
        </div>
    </div>
    <script nonce="{{.Nonce}}">
    (function(){
        var MAX=60, INTERVAL=5000;
        var history=[], basePath=location.pathname.replace(/\/+$/,'');

        // Switch units at 1000 (not 1024) of the smaller one, so axes read
        // 1.0 MB/s rather than 1000.0 KB/s.
        function formatBps(b){
            if(b<1000)return b.toFixed(0)+' B/s';
            if(b<1000*1024)return(b/1024).toFixed(1)+' KB/s';
            if(b<1000*1048576)return(b/1048576).toFixed(1)+' MB/s';
            return(b/1073741824).toFixed(1)+' GB/s';
        }
        function formatBytes(b){
            if(b<1024)return b+' B';
            if(b<1048576)return(b/1024).toFixed(1)+' KB';
            if(b<1073741824)return(b/1048576).toFixed(1)+' MB';
            return(b/1073741824).toFixed(1)+' GB';
        }

        // niceStep rounds a raw tick step up to 1/2/2.5/5 x 10^n. With unit
        // (e.g. 1024 for bytes) the rounding happens in the unit the label will
        // be shown in, so byte axes read 250 KB/s rather than 244.1 KB/s.
        function niceStep(raw,minStep,unit){
            if(!(raw>0))return minStep||1;
            var scale=1;
            if(unit)while(raw/scale>=unit)scale*=unit;
            var r=raw/scale, p=Math.pow(10,Math.floor(Math.log10(r))), f=r/p;
            var n=f<=1?1:f<=2?2:f<=2.5?2.5:f<=5?5:10, step=n*p;
            // 1000 of a unit reads better as 1 of the next (1 MB/s, not 1000 KB/s).
            if(unit&&step>=1000)return Math.max(scale*unit,minStep);
            return Math.max(step*scale,minStep);
        }

        function drawChart(id,datasets,opts){
            var canvas=document.getElementById(id);
            if(!canvas)return;
            var dpr=window.devicePixelRatio||1;
            var rect=canvas.getBoundingClientRect();
            canvas.width=rect.width*dpr;
            canvas.height=rect.height*dpr;
            var ctx=canvas.getContext('2d');
            ctx.scale(dpr,dpr);
            var W=rect.width, H=rect.height;

            // Y scale. A fixed ceiling (maxY, e.g. 100%) is used as-is; integer
            // series (peer counts) get whole-number ticks; anything else gets a
            // "nice" tick step (1/2/2.5/5 x 10^n, at least minStep) covering the
            // peak plus 10% headroom, so labels never repeat — even when the
            // series is all zeros.
            var peak=0;
            for(var d=0;d<datasets.length;d++){
                for(var i=0;i<datasets[d].data.length;i++){
                    if(datasets[d].data[i]>peak)peak=datasets[d].data[i];
                }
            }
            var maxVal, gridLines=4;
            if(opts.maxY){
                maxVal=Math.max(opts.maxY,peak);
            }else if(opts.integer){
                maxVal=Math.max(1,Math.ceil(peak));
                gridLines=Math.min(4,maxVal);
                maxVal=Math.ceil(maxVal/gridLines)*gridLines;
            }else{
                maxVal=niceStep(peak*1.1/gridLines,opts.minStep||0,opts.unit)*gridLines;
            }

            // Size the left margin to the widest Y label so none is clipped.
            ctx.font='10px monospace';
            var labels=[], labelW=0;
            for(var g=0;g<=gridLines;g++){
                var v=maxVal*(g/gridLines);
                var txt=opts.formatY?opts.formatY(v):v.toFixed(1);
                labels.push(txt);
                labelW=Math.max(labelW,ctx.measureText(txt).width);
            }
            var pad={top:10,right:10,bottom:24,left:Math.ceil(labelW)+10};
            var cW=W-pad.left-pad.right, cH=H-pad.top-pad.bottom;

            // Background
            ctx.fillStyle='#161b22';
            ctx.fillRect(0,0,W,H);

            // Grid lines
            ctx.strokeStyle='#21262d';
            ctx.lineWidth=1;
            for(var g=0;g<=gridLines;g++){
                var gy=pad.top+cH-(g/gridLines)*cH;
                ctx.beginPath();
                ctx.moveTo(pad.left,gy);
                ctx.lineTo(pad.left+cW,gy);
                ctx.stroke();
                // Y label
                ctx.fillStyle='#484f58';
                ctx.textAlign='right';
                ctx.fillText(labels[g],pad.left-4,gy+3);
            }

            // X time labels: the outer two are anchored inside the plot so
            // they are not cut off at the canvas edges.
            var n=datasets[0].data.length;
            if(n>1){
                ctx.fillStyle='#484f58';
                var steps=[0,Math.floor(n/2),n-1], aligns=['left','center','right'];
                for(var s=0;s<steps.length;s++){
                    var si=steps[s];
                    if(si>=history.length)continue;
                    ctx.textAlign=aligns[s];
                    ctx.fillText(history[si].time,pad.left+(si/(n-1))*cW,H-2);
                }
            }

            // Draw datasets
            for(var d=0;d<datasets.length;d++){
                var ds=datasets[d], data=ds.data;
                if(data.length<2)continue;
                ctx.beginPath();
                for(var i=0;i<data.length;i++){
                    var x=pad.left+(i/(data.length-1))*cW;
                    var y=pad.top+cH-(data[i]/maxVal)*cH;
                    if(i===0)ctx.moveTo(x,y);else ctx.lineTo(x,y);
                }
                ctx.strokeStyle=ds.color;
                ctx.lineWidth=2;
                ctx.stroke();

                // Area fill
                if(opts.fill!==false){
                    ctx.lineTo(pad.left+cW,pad.top+cH);
                    ctx.lineTo(pad.left,pad.top+cH);
                    ctx.closePath();
                    ctx.fillStyle=ds.color+'22';
                    ctx.fill();
                }
            }

            // Placeholder if not enough data
            if(datasets[0].data.length<2){
                ctx.fillStyle='#484f58';
                ctx.font='13px sans-serif';
                ctx.textAlign='center';
                ctx.fillText('Collecting data...',W/2,H/2);
            }
        }

        function updateDOM(s){
            var el;
            el=document.getElementById('stat-uptime');if(el)el.textContent=s.uptime;
            el=document.getElementById('stat-p2p-ratio');if(el)el.textContent=s.p2p_ratio_percent.toFixed(1)+'%';
            el=document.getElementById('stat-requests-total');if(el)el.textContent=s.requests_total;
            el=document.getElementById('stat-from-p2p');if(el)el.textContent=s.requests_p2p+' ('+formatBytes(s.bytes_from_p2p)+')';
            el=document.getElementById('stat-from-mirror');if(el)el.textContent=s.requests_mirror+' ('+formatBytes(s.bytes_from_mirror)+')';
            el=document.getElementById('stat-cache-hits');if(el)el.textContent=s.cache_hits;
            el=document.getElementById('stat-verify-failures');if(el)el.textContent=s.verification_failures;
            el=document.getElementById('stat-connected-peers');if(el)el.textContent=s.connected_peers;
            el=document.getElementById('stat-routing-table');if(el)el.textContent=s.routing_table_size;
            el=document.getElementById('stat-active-downloads');if(el)el.textContent=s.active_downloads;
            el=document.getElementById('stat-active-uploads');if(el)el.textContent=s.active_uploads;
            el=document.getElementById('stat-cache-count');if(el)el.textContent=s.cache_count;
            el=document.getElementById('stat-cache-usage');if(el)el.textContent=s.cache_usage_percent.toFixed(1)+'%';
            el=document.getElementById('stat-cache-progress');if(el)el.style.width=s.cache_usage_percent.toFixed(1)+'%';
            el=document.getElementById('stat-cache-size');if(el)el.textContent=formatBytes(s.cache_size_bytes)+(s.cache_max_size?' / '+s.cache_max_size:'');
        }

        // renderTable rebuilds a table from API rows using textContent only, so
        // no server-supplied string is ever parsed as HTML.
        function cell(tr,text,cls,title){
            var td=document.createElement('td');
            td.textContent=text;
            if(cls)td.className=cls;
            if(title)td.title=title;
            tr.appendChild(td);
        }
        function cssToken(v){return String(v||'').replace(/[^a-z0-9-]/gi,'');}
        function renderTable(id,headers,rows,emptyText,fillRow){
            var box=document.getElementById(id);
            if(!box)return;
            box.textContent='';
            if(!rows||rows.length===0){
                var empty=document.createElement('div');
                empty.className='empty-state';
                empty.textContent=emptyText;
                box.appendChild(empty);
                return;
            }
            var table=document.createElement('table'), thead=document.createElement('thead'), hr=document.createElement('tr');
            for(var h=0;h<headers.length;h++){var th=document.createElement('th');th.textContent=headers[h];hr.appendChild(th);}
            thead.appendChild(hr);table.appendChild(thead);
            var tbody=document.createElement('tbody');
            for(var r=0;r<rows.length;r++){var tr=document.createElement('tr');fillRow(tr,rows[r]);tbody.appendChild(tr);}
            table.appendChild(tbody);box.appendChild(table);
        }
        function updateTables(s){
            renderTable('recent-downloads',['Time','Package','Size','Source','Duration'],s.recent_downloads,'No recent downloads',function(tr,d){
                cell(tr,d.time);cell(tr,d.filename);cell(tr,d.size);
                cell(tr,d.source,'source-'+cssToken(d.source));cell(tr,d.duration);
            });
            renderTable('peer-table',['Peer ID','Score','Latency','Throughput','Downloaded','Uploaded','Last Seen'],s.peers,'No connected peers',function(tr,p){
                if(p.blacklisted)tr.className='blacklisted';
                cell(tr,p.short_id,'',p.id);cell(tr,Number(p.score).toFixed(1),'score-'+cssToken(p.category));
                cell(tr,p.latency);cell(tr,p.throughput);cell(tr,p.downloaded);cell(tr,p.uploaded);cell(tr,p.last_seen);
            });
        }

        function updateCharts(){
            if(history.length<1)return;
            // Derive rates from counter diffs
            var p2pRate=[],mirRate=[],reqRate=[],p2pPct=[],peers=[];
            for(var i=0;i<history.length;i++){
                var cur=history[i];
                p2pPct.push(cur.stats.p2p_ratio_percent);
                peers.push(cur.stats.connected_peers);
                if(i===0){p2pRate.push(0);mirRate.push(0);reqRate.push(0);continue;}
                var prev=history[i-1];
                var dt=INTERVAL/1000;
                p2pRate.push(Math.max(0,(cur.stats.bytes_from_p2p-prev.stats.bytes_from_p2p)/dt));
                mirRate.push(Math.max(0,(cur.stats.bytes_from_mirror-prev.stats.bytes_from_mirror)/dt));
                reqRate.push(Math.max(0,(cur.stats.requests_total-prev.stats.requests_total)/dt));
            }
            drawChart('chart-throughput',[
                {data:p2pRate,color:'#3fb950'},
                {data:mirRate,color:'#d29922'}
            ],{formatY:formatBps,minStep:1,unit:1024});
            drawChart('chart-requests',[
                {data:reqRate,color:'#58a6ff'}
            ],{minStep:0.1,formatY:function(v){return v.toFixed(v<1?2:1)+'/s';}});
            drawChart('chart-p2p-ratio',[
                {data:p2pPct,color:'#3fb950'}
            ],{maxY:100,formatY:function(v){return v.toFixed(0)+'%';}});
            drawChart('chart-peers',[
                {data:peers,color:'#58a6ff'}
            ],{fill:false,integer:true,formatY:function(v){return v.toFixed(0);}});
        }

        function poll(){
            var url=basePath+'/api/stats';
            fetch(url).then(function(r){return r.json();}).then(function(s){
                var now=new Date();
                var ts=String(now.getHours()).padStart(2,'0')+':'+String(now.getMinutes()).padStart(2,'0')+':'+String(now.getSeconds()).padStart(2,'0');
                history.push({time:ts,stats:s});
                if(history.length>MAX)history.shift();
                updateDOM(s);
                updateTables(s);
                updateCharts();
            }).catch(function(){});
        }

        poll();
        setInterval(poll,INTERVAL);
        window.addEventListener('resize',updateCharts);
    })();
    </script>
</body>
</html>`
