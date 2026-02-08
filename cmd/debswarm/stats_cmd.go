package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// statsResponse matches the JSON from the /stats endpoint.
type statsResponse struct {
	RequestsTotal     int64   `json:"requests_total"`
	RequestsP2P       int64   `json:"requests_p2p"`
	RequestsMirror    int64   `json:"requests_mirror"`
	BytesFromP2P      int64   `json:"bytes_from_p2p"`
	BytesFromMirror   int64   `json:"bytes_from_mirror"`
	CacheHits         int64   `json:"cache_hits"`
	ActiveConnections int64   `json:"active_connections"`
	P2PRatioPercent   float64 `json:"p2p_ratio_percent"`
	CacheSizeBytes    int64   `json:"cache_size_bytes"`
	CacheCount        int     `json:"cache_count"`
	Scheduler         *struct {
		InWindow       bool      `json:"InWindow"`
		CurrentRate    int64     `json:"CurrentRate"`
		NextWindowOpen time.Time `json:"NextWindowOpen"`
		Timezone       string    `json:"Timezone"`
		WindowCount    int       `json:"WindowCount"`
	} `json:"scheduler,omitempty"`
	Fleet *struct {
		InFlightCount int `json:"InFlightCount"`
		PeerCount     int `json:"PeerCount"`
	} `json:"fleet,omitempty"`
}

func statsCmd() *cobra.Command {
	var (
		watch      bool
		interval   time.Duration
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show live daemon statistics",
		Long: `Fetch and display live statistics from the running debswarm daemon.

Requires the daemon to be running with metrics enabled (default port 9978).
Use --watch for a continuously refreshing display, and --json for
machine-readable output.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if cfg.Metrics.Port == 0 {
				return fmt.Errorf("metrics are disabled in configuration (metrics.port = 0)")
			}

			url := fmt.Sprintf("http://%s:%d/stats", cfg.Metrics.Bind, cfg.Metrics.Port)
			client := &http.Client{Timeout: 5 * time.Second}

			if !watch {
				return fetchAndPrint(client, url, jsonOutput)
			}

			return watchStats(client, url, interval, jsonOutput)
		},
	}

	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Continuously refresh stats")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "Refresh interval (with --watch)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output raw JSON")

	return cmd
}

func fetchStats(client *http.Client, url string) (*statsResponse, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon not running or metrics disabled: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("unexpected status %d from daemon", resp.StatusCode)
	}

	var stats statsResponse
	if err := json.Unmarshal(body, &stats); err != nil {
		return nil, nil, fmt.Errorf("failed to parse stats: %w", err)
	}

	return &stats, body, nil
}

func fetchAndPrint(client *http.Client, url string, jsonOutput bool) error {
	stats, raw, err := fetchStats(client, url)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(raw))
		return nil
	}

	printStats(stats, "")
	return nil
}

func watchStats(client *http.Client, url string, interval time.Duration, jsonOutput bool) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Print immediately on start
	if err := printWatchTick(client, url, jsonOutput); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}

	for {
		select {
		case <-sigChan:
			fmt.Println()
			return nil
		case <-ticker.C:
			if err := printWatchTick(client, url, jsonOutput); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
		}
	}
}

func printWatchTick(client *http.Client, url string, jsonOutput bool) error {
	stats, raw, err := fetchStats(client, url)
	if err != nil {
		return err
	}

	if jsonOutput {
		fmt.Println(string(raw))
		return nil
	}

	// Clear screen
	fmt.Print("\033[2J\033[H")

	timestamp := time.Now().Format("15:04:05")
	printStats(stats, fmt.Sprintf("(updated %s, Ctrl+C to stop)", timestamp))
	return nil
}

func printStats(stats *statsResponse, subtitle string) {
	fmt.Printf("debswarm Stats\n")
	fmt.Printf("══════════════════════════════════════\n")

	if subtitle != "" {
		fmt.Printf("%s\n\n", subtitle)
	}

	fmt.Printf("Requests:   %d total (%d P2P, %d mirror)\n",
		stats.RequestsTotal, stats.RequestsP2P, stats.RequestsMirror)
	fmt.Printf("P2P Ratio:  %.1f%%\n", stats.P2PRatioPercent)
	fmt.Printf("Cache:      %s (%d packages)\n",
		formatBytes(stats.CacheSizeBytes), stats.CacheCount)
	fmt.Printf("Cache Hits: %d\n", stats.CacheHits)
	fmt.Printf("Active:     %d connections\n", stats.ActiveConnections)
	fmt.Printf("Bandwidth:  P2P %s | Mirror %s\n",
		formatBytes(stats.BytesFromP2P), formatBytes(stats.BytesFromMirror))

	if stats.Fleet != nil {
		fmt.Printf("Fleet:      %d in-flight, %d peers\n",
			stats.Fleet.InFlightCount, stats.Fleet.PeerCount)
	}

	if stats.Scheduler != nil {
		if stats.Scheduler.InWindow {
			fmt.Printf("Scheduler:  in window (rate: %s/s)\n",
				formatBytes(stats.Scheduler.CurrentRate))
		} else {
			fmt.Printf("Scheduler:  outside window\n")
		}
	}
}
