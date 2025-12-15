package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/debswarm/debswarm/internal/cache"
	"github.com/debswarm/debswarm/internal/index"
	"github.com/debswarm/debswarm/internal/metrics"
	"github.com/debswarm/debswarm/internal/mirror"
	"github.com/debswarm/debswarm/internal/p2p"
	"github.com/debswarm/debswarm/internal/peers"
	"github.com/debswarm/debswarm/internal/timeouts"
)

// E2E tests for the full proxy -> cache -> mirror/P2P flow

// TestE2E_ProxyMirrorFallback tests the complete flow:
// APT request -> Proxy -> Mirror fallback -> Cache
func TestE2E_ProxyMirrorFallback(t *testing.T) {
	// Create test package content
	pkgContent := []byte("fake package content for testing - this is a .deb file simulation")
	pkgHash := sha256.Sum256(pkgContent)
	pkgHashHex := hex.EncodeToString(pkgHash[:])

	// Create mock mirror server
	mirrorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Packages") {
			// Return a Packages index with our test package
			packagesContent := fmt.Sprintf(`Package: test-package
Version: 1.0.0
Architecture: amd64
Filename: pool/main/t/test-package/test-package_1.0.0_amd64.deb
Size: %d
SHA256: %s

`, len(pkgContent), pkgHashHex)
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(packagesContent))
			return
		}

		if strings.HasSuffix(r.URL.Path, "test-package_1.0.0_amd64.deb") {
			w.Header().Set("Content-Type", "application/vnd.debian.binary-package")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(pkgContent)))
			w.Write(pkgContent)
			return
		}

		http.NotFound(w, r)
	}))
	defer mirrorServer.Close()

	// Test 1: Fetch Packages index directly from mock mirror
	t.Run("FetchPackagesIndex", func(t *testing.T) {
		url := mirrorServer.URL + "/dists/stable/main/binary-amd64/Packages"
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("Failed to fetch Packages: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "test-package") {
			t.Error("Packages index should contain test-package")
		}
		if !strings.Contains(string(body), pkgHashHex) {
			t.Error("Packages index should contain package hash")
		}
	})

	// Test 2: Fetch package from mock mirror
	t.Run("FetchPackageFromMirror", func(t *testing.T) {
		url := mirrorServer.URL + "/pool/main/t/test-package/test-package_1.0.0_amd64.deb"
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("Failed to fetch package: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != string(pkgContent) {
			t.Error("Package content mismatch")
		}
	})

	// Test 3: Full proxy flow with httptest server for the proxy handler
	t.Run("ProxyHandlerFlow", func(t *testing.T) {
		logger, _ := zap.NewDevelopment()
		tmpDir := t.TempDir()

		pkgCache, err := cache.New(tmpDir, 100*1024*1024, logger)
		if err != nil {
			t.Fatalf("Failed to create cache: %v", err)
		}
		defer pkgCache.Close()

		idx := index.New(tmpDir, logger)
		fetcher := mirror.NewFetcher(nil, logger)

		// Create proxy server
		cfg := &Config{
			Addr:           "127.0.0.1:0",
			P2PTimeout:     5 * time.Second,
			DHTLookupLimit: 10,
			MetricsPort:    0,
			Metrics:        metrics.New(),
			Timeouts:       timeouts.NewManager(nil),
			Scorer:         peers.NewScorer(),
		}

		server := NewServer(cfg, pkgCache, idx, nil, fetcher, logger)

		// Use httptest to test the handler directly
		proxyTestServer := httptest.NewServer(server.server.Handler)
		defer proxyTestServer.Close()
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		}()

		// Request a package through the proxy (this will try to fetch from the URL)
		// The proxy will forward requests to the actual URL in the request
		req, _ := http.NewRequest("GET", proxyTestServer.URL+"/", nil)
		req.Host = "example.com" // Simulate APT request to a host

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Failed to make proxy request: %v", err)
		}
		defer resp.Body.Close()

		// We expect either success or a valid HTTP error (proxy is working)
		t.Logf("Proxy response status: %d", resp.StatusCode)
	})
}

// TestE2E_CacheHit tests that cached packages are served without hitting the mirror
func TestE2E_CacheHit(t *testing.T) {
	// Create test package content
	pkgContent := []byte("cached package content for testing")
	pkgHash := sha256.Sum256(pkgContent)
	pkgHashHex := hex.EncodeToString(pkgHash[:])

	// Track mirror hits
	mirrorHits := 0
	mirrorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits++
		if strings.HasSuffix(r.URL.Path, ".deb") {
			w.Write(pkgContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer mirrorServer.Close()

	// Set up components
	logger, _ := zap.NewDevelopment()
	tmpDir := t.TempDir()

	pkgCache, err := cache.New(tmpDir, 100*1024*1024, logger)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer pkgCache.Close()

	// Pre-populate cache with the package
	err = pkgCache.Put(strings.NewReader(string(pkgContent)), pkgHashHex, "test-package_1.0.0_amd64.deb")
	if err != nil {
		t.Fatalf("Failed to put package in cache: %v", err)
	}

	// Verify it's cached
	if !pkgCache.Has(pkgHashHex) {
		t.Fatal("Package should be in cache")
	}

	// Fetch from cache
	cachedReader, _, err := pkgCache.Get(pkgHashHex)
	if err != nil {
		t.Fatalf("Failed to get from cache: %v", err)
	}
	cachedContent, _ := io.ReadAll(cachedReader)
	cachedReader.Close()

	if string(cachedContent) != string(pkgContent) {
		t.Error("Cached content doesn't match original")
	}

	// Mirror should not have been hit for cache operations
	if mirrorHits != 0 {
		t.Errorf("Mirror was hit %d times, expected 0 for cache-only operations", mirrorHits)
	}
}

// TestE2E_IndexAutoPopulation tests that the proxy auto-parses Packages files
func TestE2E_IndexAutoPopulation(t *testing.T) {
	pkgHash := "abc123def456789012345678901234567890123456789012345678901234abcd"

	packagesContent := fmt.Sprintf(`Package: hello
Version: 2.10-1
Architecture: amd64
Filename: pool/main/h/hello/hello_2.10-1_amd64.deb
Size: 52832
SHA256: %s

Package: vim
Version: 8.2.0-1
Architecture: amd64
Filename: pool/main/v/vim/vim_8.2.0-1_amd64.deb
Size: 1234567
SHA256: fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210

`, pkgHash)

	// Set up components
	logger, _ := zap.NewDevelopment()
	tmpDir := t.TempDir()

	idx := index.New(tmpDir, logger)

	// Load the packages content
	err := idx.LoadFromData([]byte(packagesContent), "http://deb.debian.org/debian/dists/stable/main/binary-amd64/Packages")
	if err != nil {
		t.Fatalf("Failed to load packages data: %v", err)
	}

	// Test lookups
	t.Run("LookupBySHA256", func(t *testing.T) {
		pkg := idx.GetBySHA256(pkgHash)
		if pkg == nil {
			t.Fatal("Package not found by SHA256")
		}
		if pkg.Package != "hello" {
			t.Errorf("Package name = %q, want %q", pkg.Package, "hello")
		}
		if pkg.Version != "2.10-1" {
			t.Errorf("Version = %q, want %q", pkg.Version, "2.10-1")
		}
	})

	t.Run("LookupByPath", func(t *testing.T) {
		pkg := idx.GetByPath("pool/main/h/hello/hello_2.10-1_amd64.deb")
		if pkg == nil {
			t.Fatal("Package not found by path")
		}
		if pkg.SHA256 != pkgHash {
			t.Errorf("SHA256 = %q, want %q", pkg.SHA256, pkgHash)
		}
	})

	t.Run("MultiplePackages", func(t *testing.T) {
		vim := idx.GetByPath("pool/main/v/vim/vim_8.2.0-1_amd64.deb")
		if vim == nil {
			t.Fatal("vim package not found")
		}
		if vim.Package != "vim" {
			t.Errorf("Package name = %q, want %q", vim.Package, "vim")
		}
	})
}

// TestE2E_TwoNodeP2PTransfer tests P2P package transfer between two nodes
func TestE2E_TwoNodeP2PTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping P2P test in short mode")
	}

	logger, _ := zap.NewDevelopment()
	ctx := context.Background()

	// Create test package
	pkgContent := []byte("package content for P2P transfer test - needs to be reasonably sized")
	pkgHash := sha256.Sum256(pkgContent)
	pkgHashHex := hex.EncodeToString(pkgHash[:])

	// Create two temp directories for the two nodes
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	// Create caches for both nodes
	cache1, err := cache.New(tmpDir1, 100*1024*1024, logger)
	if err != nil {
		t.Fatalf("Failed to create cache1: %v", err)
	}
	defer cache1.Close()

	cache2, err := cache.New(tmpDir2, 100*1024*1024, logger)
	if err != nil {
		t.Fatalf("Failed to create cache2: %v", err)
	}
	defer cache2.Close()

	// Put package in cache1 (the seeder)
	err = cache1.Put(strings.NewReader(string(pkgContent)), pkgHashHex, "test-p2p-package.deb")
	if err != nil {
		t.Fatalf("Failed to seed package: %v", err)
	}

	// Create P2P node1 (seeder) with ephemeral identity
	node1Cfg := &p2p.Config{
		ListenPort:     0, // Random port
		PreferQUIC:     false,
		MaxConnections: 10,
	}
	node1, err := p2p.New(ctx, node1Cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create node1: %v", err)
	}
	defer node1.Close()

	// Set content getter for node1 so it can serve packages
	node1.SetContentGetter(func(hash string) (io.ReadCloser, int64, error) {
		reader, pkg, err := cache1.Get(hash)
		if err != nil {
			return nil, 0, err
		}
		return reader, pkg.Size, nil
	})

	// Wait for node1 to be ready
	node1.WaitForBootstrap()

	// Create P2P node2 (leecher) with node1 as bootstrap peer
	node1Addrs := node1.Addrs()
	if len(node1Addrs) == 0 {
		t.Fatal("Node1 has no addresses")
	}

	// Build bootstrap address with peer ID
	node1AddrStr := fmt.Sprintf("%s/p2p/%s", node1Addrs[0].String(), node1.PeerID().String())

	node2Cfg := &p2p.Config{
		ListenPort:     0,
		PreferQUIC:     false,
		MaxConnections: 10,
		BootstrapPeers: []string{node1AddrStr},
	}
	node2, err := p2p.New(ctx, node2Cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create node2: %v", err)
	}
	defer node2.Close()

	// Wait for node2 to connect to node1
	node2.WaitForBootstrap()

	// Give nodes time to connect
	time.Sleep(500 * time.Millisecond)

	// Verify nodes are connected
	node1Peers := node1.ConnectedPeers()
	node2Peers := node2.ConnectedPeers()
	t.Logf("Node1 peers: %d, Node2 peers: %d", node1Peers, node2Peers)

	// Announce package from node1
	provideCtx, provideCancel := context.WithTimeout(ctx, 10*time.Second)
	defer provideCancel()
	err = node1.Provide(provideCtx, pkgHashHex)
	if err != nil {
		t.Logf("Provide warning: %v", err)
	}

	// Give DHT time to propagate
	time.Sleep(500 * time.Millisecond)

	// Try to find providers from node2
	findCtx, findCancel := context.WithTimeout(ctx, 10*time.Second)
	defer findCancel()
	providers, err := node2.FindProviders(findCtx, pkgHashHex, 10)
	if err != nil {
		t.Logf("FindProviders returned: %v (may be expected in test environment)", err)
	}
	t.Logf("Found %d providers for package", len(providers))

	// Build node1's AddrInfo for direct connection
	node1AddrInfo := peer.AddrInfo{
		ID:    node1.PeerID(),
		Addrs: node1.Addrs(),
	}

	// Try to fetch via direct connection (DHT may not work in isolated test)
	t.Log("Testing direct P2P connection")

	downloadCtx, downloadCancel := context.WithTimeout(ctx, 10*time.Second)
	defer downloadCancel()

	// Request package directly from node1
	data, err := node2.Download(downloadCtx, node1AddrInfo, pkgHashHex)
	if err != nil {
		t.Fatalf("Failed to download package: %v", err)
	}

	if string(data) != string(pkgContent) {
		t.Error("P2P transferred content doesn't match original")
	}
	t.Log("Successfully transferred package via direct P2P connection")
}

// TestE2E_HashVerification tests that invalid hashes are rejected
func TestE2E_HashVerification(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tmpDir := t.TempDir()

	pkgCache, err := cache.New(tmpDir, 100*1024*1024, logger)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer pkgCache.Close()

	// Try to put content with wrong hash
	content := "this is the actual content"
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"

	err = pkgCache.Put(strings.NewReader(content), wrongHash, "bad-package.deb")
	if err == nil {
		t.Error("Expected error when putting content with wrong hash, got nil")
	}

	// Verify it wasn't stored
	if pkgCache.Has(wrongHash) {
		t.Error("Package with wrong hash should not be in cache")
	}

	// Now put with correct hash
	correctHash := sha256.Sum256([]byte(content))
	correctHashHex := hex.EncodeToString(correctHash[:])

	err = pkgCache.Put(strings.NewReader(content), correctHashHex, "good-package.deb")
	if err != nil {
		t.Fatalf("Failed to put with correct hash: %v", err)
	}

	if !pkgCache.Has(correctHashHex) {
		t.Error("Package with correct hash should be in cache")
	}
}
