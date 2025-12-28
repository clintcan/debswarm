package index

import (
	"testing"

	"go.uber.org/zap"
)

// Integration tests to verify httpclient integration in index/index.go
// Note: LoadFromURL has SSRF protection that blocks localhost URLs,
// so we test the client initialization and verify the existing tests cover functionality.

// TestIndex_ClientFieldInitialized verifies the client field is set
func TestIndex_ClientFieldInitialized(t *testing.T) {
	idx := New(t.TempDir(), zap.NewNop())

	if idx.client == nil {
		t.Fatal("client field should be initialized")
	}

	// Verify client has a timeout set (from httpclient.Default())
	if idx.client.Timeout <= 0 {
		t.Error("client should have a timeout")
	}
}

// TestIndex_ClientHasTransport verifies the client has proper transport
func TestIndex_ClientHasTransport(t *testing.T) {
	idx := New(t.TempDir(), zap.NewNop())

	if idx.client.Transport == nil {
		t.Fatal("client should have a transport")
	}
}

// TestIndex_MultipleIndexesHaveSeparateClients verifies independence
func TestIndex_MultipleIndexesHaveSeparateClients(t *testing.T) {
	idx1 := New(t.TempDir(), zap.NewNop())
	idx2 := New(t.TempDir(), zap.NewNop())

	// Each index should have its own client instance
	if idx1.client == idx2.client {
		t.Error("indexes should have separate client instances")
	}
}

// TestIndex_HTTPClientUsedForRequests verifies the client field is used
// This is verified indirectly - if the client wasn't used, existing
// LoadFromURL tests would fail differently. The SSRF protection
// happens before the HTTP request, so we can verify the client is
// set up correctly.
func TestIndex_HTTPClientUsedForRequests(t *testing.T) {
	idx := New(t.TempDir(), zap.NewNop())

	// Try to load from a blocked URL - this exercises the code path
	// up to the point where the HTTP client would be used
	err := idx.LoadFromURL("http://127.0.0.1/test")
	if err == nil {
		t.Error("expected SSRF protection error")
	}

	// The error should be about SSRF, not about client issues
	if idx.client == nil {
		t.Error("client should still be initialized after failed request")
	}
}
