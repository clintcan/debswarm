package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/debswarm/debswarm/internal/dashboard"
)

// A served package must appear in the dashboard's recent-downloads list; the
// proxy previously never called RecordDownload, so the list stayed empty.
func TestRecentDownloads_CacheHitIsRecorded(t *testing.T) {
	c, idx, _ := offlineCache(t, true)
	srv := serverWith(t, c, idx)
	dash := dashboard.New(&dashboard.Config{}, srv.GetDashboardStats, srv.GetPeerInfo)
	srv.SetDashboard(dash)

	w := httptest.NewRecorder()
	srv.handlePackageRequest(w, httptest.NewRequest("GET", "/"+offlineDebURL, nil), offlineDebURL)
	if w.Code != http.StatusOK {
		t.Fatalf("cached .deb: code=%d want 200", w.Code)
	}

	apiW := httptest.NewRecorder()
	dash.Handler().ServeHTTP(apiW, httptest.NewRequest("GET", "/api/stats", nil))
	var stats dashboard.Stats
	if err := json.NewDecoder(apiW.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if len(stats.RecentDownloads) != 1 {
		t.Fatalf("recent downloads = %d, want 1", len(stats.RecentDownloads))
	}
	got := stats.RecentDownloads[0]
	if got.Filename != "hello_2.10-2_amd64.deb" || got.Source != "cache" {
		t.Errorf("recent download = %+v, want hello_2.10-2_amd64.deb from cache", got)
	}
}
