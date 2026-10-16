package dashboard

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The page's polling script refreshes the recent-downloads and peers tables from
// /api/stats, so the API must carry both (it previously carried neither).
func TestHandler_APIStatsIncludesTables(t *testing.T) {
	peers := []PeerInfo{{ID: "12D3KooWPeer", ShortID: "12D3Ko...Peer", Score: 0.7, Category: "good", LastSeen: "1s ago"}}
	d := New(&Config{Version: "1.0.0"}, func() *Stats { return &Stats{} }, func() []PeerInfo { return peers })
	d.RecordDownload("pool/main/h/hello/hello_2.10_arm64.deb", 25000, "peer", 120*time.Millisecond)

	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/stats", nil))

	var stats Stats
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(stats.RecentDownloads) != 1 || stats.RecentDownloads[0].Source != "peer" {
		t.Errorf("recent_downloads = %+v, want one peer download", stats.RecentDownloads)
	}
	if len(stats.Peers) != 1 || stats.Peers[0].ID != "12D3KooWPeer" {
		t.Errorf("peers = %+v, want the provider's peer", stats.Peers)
	}
}

// With no peers the API must send an empty list, not null, so the script renders
// the empty state.
func TestHandler_APIStatsEmptyPeersIsList(t *testing.T) {
	d := New(&Config{}, func() *Stats { return &Stats{} }, func() []PeerInfo { return nil })

	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/stats", nil))

	if !strings.Contains(w.Body.String(), `"peers":[]`) {
		t.Errorf("body does not contain an empty peers list: %s", w.Body.String())
	}
}

// "0" is the config value for no rate limit and must show as Unlimited.
func TestDisplayRate(t *testing.T) {
	tests := map[string]string{"0": "", " 0 ": "", "": "", "10MB/s": "10MB/s"}
	for in, want := range tests {
		if got := displayRate(in); got != want {
			t.Errorf("displayRate(%q) = %q, want %q", in, got, want)
		}
	}

	d := New(&Config{MaxUploadRate: "0", MaxDownloadRate: "50MB/s"}, func() *Stats { return &Stats{} }, nil)
	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, "Unlimited") {
		t.Error(`dashboard does not show "Unlimited" for a "0" upload limit`)
	}
	if !strings.Contains(body, "50MB/s") {
		t.Error("dashboard does not show the configured download limit")
	}
}

// The live-refresh containers must exist in the page for the script to target.
func TestHandler_LiveTableContainers(t *testing.T) {
	d := New(&Config{}, func() *Stats { return &Stats{} }, nil)
	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	for _, id := range []string{`id="recent-downloads"`, `id="peer-table"`} {
		if !strings.Contains(w.Body.String(), id) {
			t.Errorf("page missing %s", id)
		}
	}
}
