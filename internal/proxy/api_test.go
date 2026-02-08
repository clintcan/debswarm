package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testPkg is a helper that inserts a package into the test cache and returns its hash.
func testPkg(t *testing.T, s *Server, content, filename string) string {
	t.Helper()
	h := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(h[:])
	if err := s.cache.Put(strings.NewReader(content), hash, filename); err != nil {
		t.Fatalf("Failed to put test package: %v", err)
	}
	return hash
}

func TestAPICacheStats_Empty(t *testing.T) {
	s := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cache", nil)
	s.handleAPICache(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var stats apiCacheStats
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stats.TotalPackages != 0 {
		t.Errorf("total_packages = %d, want 0", stats.TotalPackages)
	}
	if stats.TotalSize != 0 {
		t.Errorf("total_size = %d, want 0", stats.TotalSize)
	}
	if stats.PinnedCount != 0 {
		t.Errorf("pinned_count = %d, want 0", stats.PinnedCount)
	}
}

func TestAPICacheStats_Populated(t *testing.T) {
	s := newTestServer(t)

	testPkg(t, s, "pkg1-data", "pool/main/c/curl/curl_7.88.1-10_amd64.deb")
	testPkg(t, s, "pkg2-data", "pool/main/w/wget/wget_1.21-1_amd64.deb")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cache", nil)
	s.handleAPICache(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var stats apiCacheStats
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if stats.TotalPackages != 2 {
		t.Errorf("total_packages = %d, want 2", stats.TotalPackages)
	}
	if stats.TotalSize == 0 {
		t.Error("total_size should be > 0")
	}
}

func TestAPIListPackages_All(t *testing.T) {
	s := newTestServer(t)

	testPkg(t, s, "data-a", "pool/main/a/aaa/aaa_1.0_amd64.deb")
	testPkg(t, s, "data-b", "pool/main/b/bbb/bbb_2.0_amd64.deb")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cache/packages", nil)
	s.handleAPIListPackages(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var list apiPackageList
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total != 2 {
		t.Errorf("total = %d, want 2", list.Total)
	}
	if len(list.Packages) != 2 {
		t.Errorf("len(packages) = %d, want 2", len(list.Packages))
	}
}

func TestAPIListPackages_FilterByName(t *testing.T) {
	s := newTestServer(t)

	testPkg(t, s, "curl-data", "pool/main/c/curl/curl_7.88.1-10_amd64.deb")
	testPkg(t, s, "wget-data", "pool/main/w/wget/wget_1.21-1_amd64.deb")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cache/packages?name=curl", nil)
	s.handleAPIListPackages(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var list apiPackageList
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total != 1 {
		t.Errorf("total = %d, want 1", list.Total)
	}
	if len(list.Packages) > 0 && list.Packages[0].PackageName != "curl" {
		t.Errorf("package_name = %q, want %q", list.Packages[0].PackageName, "curl")
	}
}

func TestAPIListPackages_FilterByPinned(t *testing.T) {
	s := newTestServer(t)

	hash := testPkg(t, s, "pin-data", "pool/main/p/pinned/pinned_1.0_amd64.deb")
	testPkg(t, s, "unpin-data", "pool/main/u/unpinned/unpinned_1.0_amd64.deb")

	if err := s.cache.Pin(hash); err != nil {
		t.Fatalf("pin: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cache/packages?pinned=true", nil)
	s.handleAPIListPackages(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var list apiPackageList
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total != 1 {
		t.Errorf("total = %d, want 1", list.Total)
	}
	if len(list.Packages) > 0 && !list.Packages[0].Pinned {
		t.Error("expected pinned package")
	}
}

func TestAPIListPackages_Limit(t *testing.T) {
	s := newTestServer(t)

	for i := 0; i < 5; i++ {
		testPkg(t, s, fmt.Sprintf("data-%d", i), fmt.Sprintf("pool/main/p/pkg%d/pkg%d_1.0_amd64.deb", i, i))
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cache/packages?limit=2", nil)
	s.handleAPIListPackages(w, r)

	var list apiPackageList
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total != 5 {
		t.Errorf("total = %d, want 5", list.Total)
	}
	if len(list.Packages) != 2 {
		t.Errorf("len(packages) = %d, want 2", len(list.Packages))
	}
}

func TestAPIPopularPackages(t *testing.T) {
	s := newTestServer(t)

	testPkg(t, s, "pop-data", "pool/main/p/pop/pop_1.0_amd64.deb")
	testPkg(t, s, "rare-data", "pool/main/r/rare/rare_1.0_amd64.deb")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cache/packages/popular?limit=10", nil)
	s.handleAPIPopularPackages(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var list apiPackageList
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total != 2 {
		t.Errorf("total = %d, want 2", list.Total)
	}
}

func TestAPIRecentPackages(t *testing.T) {
	s := newTestServer(t)

	testPkg(t, s, "recent-data", "pool/main/r/recent/recent_1.0_amd64.deb")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cache/packages/recent", nil)
	s.handleAPIRecentPackages(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var list apiPackageList
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Total != 1 {
		t.Errorf("total = %d, want 1", list.Total)
	}
}

func TestAPIPinPackage_Success(t *testing.T) {
	s := newTestServer(t)
	hash := testPkg(t, s, "pin-me", "pool/main/p/pin/pin_1.0_amd64.deb")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/cache/packages/"+hash+"/pin", nil)
	r.SetPathValue("hash", hash)
	s.handleAPIPinPackage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if !s.cache.IsPinned(hash) {
		t.Error("package should be pinned after pin request")
	}
}

func TestAPIPinPackage_NotFound(t *testing.T) {
	s := newTestServer(t)

	fakeHash := "a" + strings.Repeat("0", 63)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/cache/packages/"+fakeHash+"/pin", nil)
	r.SetPathValue("hash", fakeHash)
	s.handleAPIPinPackage(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestAPIPinPackage_InvalidHash(t *testing.T) {
	s := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/cache/packages/badhash/pin", nil)
	r.SetPathValue("hash", "badhash")
	s.handleAPIPinPackage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAPIUnpinPackage_Success(t *testing.T) {
	s := newTestServer(t)
	hash := testPkg(t, s, "unpin-me", "pool/main/u/unpin/unpin_1.0_amd64.deb")

	if err := s.cache.Pin(hash); err != nil {
		t.Fatalf("pin: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/cache/packages/"+hash+"/unpin", nil)
	r.SetPathValue("hash", hash)
	s.handleAPIUnpinPackage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if s.cache.IsPinned(hash) {
		t.Error("package should not be pinned after unpin request")
	}
}

func TestAPIUnpinPackage_NotFound(t *testing.T) {
	s := newTestServer(t)

	fakeHash := "b" + strings.Repeat("0", 63)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/cache/packages/"+fakeHash+"/unpin", nil)
	r.SetPathValue("hash", fakeHash)
	s.handleAPIUnpinPackage(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestAPIDeletePackage_Success(t *testing.T) {
	s := newTestServer(t)
	hash := testPkg(t, s, "delete-me", "pool/main/d/del/del_1.0_amd64.deb")

	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/api/cache/packages/"+hash, nil)
	r.SetPathValue("hash", hash)
	s.handleAPIDeletePackage(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if s.cache.Has(hash) {
		t.Error("package should be deleted")
	}
}

func TestAPIDeletePackage_NotFound(t *testing.T) {
	s := newTestServer(t)

	fakeHash := "c" + strings.Repeat("0", 63)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/api/cache/packages/"+fakeHash, nil)
	r.SetPathValue("hash", fakeHash)
	s.handleAPIDeletePackage(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestAPIDeletePackage_InvalidHash(t *testing.T) {
	s := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/api/cache/packages/xyz/", nil)
	r.SetPathValue("hash", "xyz")
	s.handleAPIDeletePackage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAPISecurityHeaders(t *testing.T) {
	s := newTestServer(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/cache", nil)
	s.handleAPICache(w, r)

	headers := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Content-Type":           "application/json",
	}
	for key, want := range headers {
		got := w.Header().Get(key)
		if got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
}

func TestIsValidSHA256(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid lowercase", "a" + strings.Repeat("0", 63), true},
		{"valid mixed hex", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", true},
		{"too short", "abcdef", false},
		{"too long", strings.Repeat("a", 65), false},
		{"non-hex chars", "g" + strings.Repeat("0", 63), false},
		{"uppercase", "A" + strings.Repeat("0", 63), false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidSHA256(tt.input)
			if got != tt.want {
				t.Errorf("isValidSHA256(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
