package httpclient_test

import (
	"fmt"
	"net/http"
	"time"

	"github.com/debswarm/debswarm/internal/httpclient"
)

func ExampleNew() {
	// Create a client with custom configuration
	client := httpclient.New(&httpclient.Config{
		Timeout:             30 * time.Second,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     60 * time.Second,
	})

	fmt.Printf("Timeout: %v\n", client.Timeout)
	// Output: Timeout: 30s
}

func ExampleNew_defaults() {
	// Passing nil uses default configuration
	client := httpclient.New(nil)

	fmt.Printf("Timeout: %v\n", client.Timeout)
	fmt.Printf("Has transport: %v\n", client.Transport != nil)
	// Output:
	// Timeout: 1m0s
	// Has transport: true
}

func ExampleDefault() {
	// Default() returns a client with sensible defaults:
	// - 60s timeout
	// - 10 max idle connections per host
	// - 90s idle connection timeout
	client := httpclient.Default()

	fmt.Printf("Timeout: %v\n", client.Timeout)

	transport := client.Transport.(*http.Transport)
	fmt.Printf("MaxIdleConnsPerHost: %d\n", transport.MaxIdleConnsPerHost)
	// Output:
	// Timeout: 1m0s
	// MaxIdleConnsPerHost: 10
}

func ExampleWithTimeout() {
	// WithTimeout creates a simple client with only a timeout
	// No custom transport (uses http.DefaultTransport)
	client := httpclient.WithTimeout(5 * time.Second)

	fmt.Printf("Timeout: %v\n", client.Timeout)
	fmt.Printf("Uses default transport: %v\n", client.Transport == nil)
	// Output:
	// Timeout: 5s
	// Uses default transport: true
}

func Example_connectionPooling() {
	// Use New() when you need connection pooling for multiple requests
	// to the same host (e.g., mirror fetching)
	client := httpclient.New(&httpclient.Config{
		Timeout:             60 * time.Second,
		MaxIdleConnsPerHost: 10, // Keep up to 10 idle connections
	})

	// Make multiple requests - connections will be reused
	_ = client
}

func Example_simpleRequest() {
	// Use WithTimeout() for simple one-off requests
	// (e.g., connectivity checks)
	client := httpclient.WithTimeout(5 * time.Second)

	// Make a simple request
	_ = client
}
