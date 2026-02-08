package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/cache"
)

// API response types

type apiError struct {
	Error string `json:"error"`
}

type apiOK struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type apiCacheStats struct {
	TotalPackages  int     `json:"total_packages"`
	TotalSize      int64   `json:"total_size"`
	TotalSizeStr   string  `json:"total_size_str"`
	MaxSize        int64   `json:"max_size"`
	MaxSizeStr     string  `json:"max_size_str"`
	UsagePercent   float64 `json:"usage_percent"`
	BandwidthSaved int64   `json:"bandwidth_saved"`
	PinnedCount    int     `json:"pinned_count"`
	OldestAccess   string  `json:"oldest_access"`
	NewestAccess   string  `json:"newest_access"`
}

type apiPackage struct {
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	SizeStr      string `json:"size_str"`
	Filename     string `json:"filename"`
	PackageName  string `json:"package_name,omitempty"`
	Version      string `json:"version,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	AddedAt      string `json:"added_at"`
	LastAccessed string `json:"last_accessed"`
	AccessCount  int64  `json:"access_count"`
	Pinned       bool   `json:"pinned"`
	Announced    bool   `json:"announced"`
}

type apiPackageList struct {
	Packages []*apiPackage `json:"packages"`
	Total    int           `json:"total"`
}

// registerAPIRoutes registers all cache management REST API routes on the given mux.
func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cache", s.handleAPICache)
	mux.HandleFunc("GET /api/cache/packages", s.handleAPIListPackages)
	mux.HandleFunc("GET /api/cache/packages/popular", s.handleAPIPopularPackages)
	mux.HandleFunc("GET /api/cache/packages/recent", s.handleAPIRecentPackages)
	mux.HandleFunc("POST /api/cache/packages/{hash}/pin", s.handleAPIPinPackage)
	mux.HandleFunc("POST /api/cache/packages/{hash}/unpin", s.handleAPIUnpinPackage)
	mux.HandleFunc("DELETE /api/cache/packages/{hash}", s.handleAPIDeletePackage)
}

// Helpers

func isValidSHA256(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func parseLimit(r *http.Request, defaultLimit, maxLimit int) int {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return defaultLimit
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

func packageToAPI(pkg *cache.Package) *apiPackage {
	return &apiPackage{
		SHA256:       pkg.SHA256,
		Size:         pkg.Size,
		SizeStr:      formatBytes(pkg.Size),
		Filename:     pkg.Filename,
		PackageName:  pkg.PackageName,
		Version:      pkg.PackageVersion,
		Architecture: pkg.Architecture,
		AddedAt:      pkg.AddedAt.UTC().Format(time.RFC3339),
		LastAccessed: pkg.LastAccessed.UTC().Format(time.RFC3339),
		AccessCount:  pkg.AccessCount,
		Pinned:       pkg.Pinned,
		Announced:    !pkg.Announced.IsZero() && pkg.Announced.Unix() > 0,
	}
}

// Handlers

func (s *Server) handleAPICache(w http.ResponseWriter, r *http.Request) {
	stats, err := s.cache.Stats()
	if err != nil {
		s.logger.Error("Failed to get cache stats", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to retrieve cache stats")
		return
	}

	usagePercent := float64(0)
	if stats.MaxSize > 0 {
		usagePercent = float64(stats.TotalSize) / float64(stats.MaxSize) * 100
	}

	oldestAccess := ""
	newestAccess := ""
	if stats.TotalPackages > 0 {
		oldestAccess = stats.OldestAccess.UTC().Format(time.RFC3339)
		newestAccess = stats.NewestAccess.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, apiCacheStats{
		TotalPackages:  stats.TotalPackages,
		TotalSize:      stats.TotalSize,
		TotalSizeStr:   formatBytes(stats.TotalSize),
		MaxSize:        stats.MaxSize,
		MaxSizeStr:     formatBytes(stats.MaxSize),
		UsagePercent:   usagePercent,
		BandwidthSaved: stats.BandwidthSaved,
		PinnedCount:    s.cache.PinnedCount(),
		OldestAccess:   oldestAccess,
		NewestAccess:   newestAccess,
	})
}

func (s *Server) handleAPIListPackages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := parseLimit(r, 20, 100)

	var packages []*cache.Package
	var err error

	// Filter by pinned status
	if query.Get("pinned") == "true" {
		packages, err = s.cache.ListPinned()
	} else if name := strings.TrimSpace(query.Get("name")); name != "" {
		// Filter by package name
		packages, err = s.cache.ListByPackageName(name)
	} else {
		packages, err = s.cache.List()
	}

	if err != nil {
		s.logger.Error("Failed to list packages", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to list packages")
		return
	}

	total := len(packages)
	if limit < total {
		packages = packages[:limit]
	}

	apiPkgs := make([]*apiPackage, len(packages))
	for i, pkg := range packages {
		apiPkgs[i] = packageToAPI(pkg)
	}

	writeJSON(w, http.StatusOK, apiPackageList{
		Packages: apiPkgs,
		Total:    total,
	})
}

func (s *Server) handleAPIPopularPackages(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r, 20, 100)

	packages, err := s.cache.PopularPackages(limit)
	if err != nil {
		s.logger.Error("Failed to get popular packages", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to get popular packages")
		return
	}

	apiPkgs := make([]*apiPackage, len(packages))
	for i, pkg := range packages {
		apiPkgs[i] = packageToAPI(pkg)
	}

	writeJSON(w, http.StatusOK, apiPackageList{
		Packages: apiPkgs,
		Total:    len(apiPkgs),
	})
}

func (s *Server) handleAPIRecentPackages(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r, 20, 100)

	packages, err := s.cache.RecentPackages(limit)
	if err != nil {
		s.logger.Error("Failed to get recent packages", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to get recent packages")
		return
	}

	apiPkgs := make([]*apiPackage, len(packages))
	for i, pkg := range packages {
		apiPkgs[i] = packageToAPI(pkg)
	}

	writeJSON(w, http.StatusOK, apiPackageList{
		Packages: apiPkgs,
		Total:    len(apiPkgs),
	})
}

func (s *Server) handleAPIPinPackage(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !isValidSHA256(hash) {
		writeError(w, http.StatusBadRequest, "invalid SHA256 hash format")
		return
	}

	err := s.cache.Pin(hash)
	if err != nil {
		if errors.Is(err, cache.ErrNotFound) {
			writeError(w, http.StatusNotFound, "package not found")
			return
		}
		s.logger.Error("Failed to pin package", zap.String("hash", hash[:16]+"..."), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to pin package")
		return
	}

	writeJSON(w, http.StatusOK, apiOK{OK: true, Message: "package pinned"})
}

func (s *Server) handleAPIUnpinPackage(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !isValidSHA256(hash) {
		writeError(w, http.StatusBadRequest, "invalid SHA256 hash format")
		return
	}

	err := s.cache.Unpin(hash)
	if err != nil {
		if errors.Is(err, cache.ErrNotFound) {
			writeError(w, http.StatusNotFound, "package not found")
			return
		}
		s.logger.Error("Failed to unpin package", zap.String("hash", hash[:16]+"..."), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to unpin package")
		return
	}

	writeJSON(w, http.StatusOK, apiOK{OK: true, Message: "package unpinned"})
}

func (s *Server) handleAPIDeletePackage(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !isValidSHA256(hash) {
		writeError(w, http.StatusBadRequest, "invalid SHA256 hash format")
		return
	}

	// Check if package exists first
	if !s.cache.Has(hash) {
		writeError(w, http.StatusNotFound, "package not found")
		return
	}

	err := s.cache.Delete(hash)
	if err != nil {
		if errors.Is(err, cache.ErrFileInUse) {
			writeError(w, http.StatusConflict, "package is currently being read")
			return
		}
		s.logger.Error("Failed to delete package", zap.String("hash", hash[:16]+"..."), zap.Error(err))
		writeError(w, http.StatusInternalServerError, "failed to delete package")
		return
	}

	writeJSON(w, http.StatusOK, apiOK{OK: true, Message: "package deleted"})
}
