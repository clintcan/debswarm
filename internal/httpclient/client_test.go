package httpclient

import (
	"net/http"
	"testing"
	"time"
)

func TestNew_NilConfig(t *testing.T) {
	client := New(nil)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Timeout != DefaultTimeout {
		t.Errorf("expected timeout %v, got %v", DefaultTimeout, client.Timeout)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}
	if transport.MaxIdleConnsPerHost != DefaultMaxIdleConnsPerHost {
		t.Errorf("expected MaxIdleConnsPerHost %d, got %d", DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != DefaultIdleConnTimeout {
		t.Errorf("expected IdleConnTimeout %v, got %v", DefaultIdleConnTimeout, transport.IdleConnTimeout)
	}
}

func TestNew_EmptyConfig(t *testing.T) {
	client := New(&Config{})
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	// Should use defaults for zero values
	if client.Timeout != DefaultTimeout {
		t.Errorf("expected timeout %v, got %v", DefaultTimeout, client.Timeout)
	}
}

func TestNew_CustomConfig(t *testing.T) {
	cfg := &Config{
		Timeout:             30 * time.Second,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     120 * time.Second,
	}

	client := New(cfg)
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	if client.Timeout != 30*time.Second {
		t.Errorf("expected timeout 30s, got %v", client.Timeout)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}
	if transport.MaxIdleConnsPerHost != 20 {
		t.Errorf("expected MaxIdleConnsPerHost 20, got %d", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 120*time.Second {
		t.Errorf("expected IdleConnTimeout 120s, got %v", transport.IdleConnTimeout)
	}
}

func TestNew_PartialConfig(t *testing.T) {
	// Only set timeout, others should use defaults
	cfg := &Config{
		Timeout: 45 * time.Second,
	}

	client := New(cfg)
	if client.Timeout != 45*time.Second {
		t.Errorf("expected timeout 45s, got %v", client.Timeout)
	}

	transport := client.Transport.(*http.Transport)
	if transport.MaxIdleConnsPerHost != DefaultMaxIdleConnsPerHost {
		t.Errorf("expected default MaxIdleConnsPerHost, got %d", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != DefaultIdleConnTimeout {
		t.Errorf("expected default IdleConnTimeout, got %v", transport.IdleConnTimeout)
	}
}

func TestDefault(t *testing.T) {
	client := Default()
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Timeout != DefaultTimeout {
		t.Errorf("expected timeout %v, got %v", DefaultTimeout, client.Timeout)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}
	if transport.MaxIdleConnsPerHost != DefaultMaxIdleConnsPerHost {
		t.Errorf("expected MaxIdleConnsPerHost %d, got %d", DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	}
}

func TestWithTimeout(t *testing.T) {
	timeout := 15 * time.Second
	client := WithTimeout(timeout)

	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Timeout != timeout {
		t.Errorf("expected timeout %v, got %v", timeout, client.Timeout)
	}
	// Should not have custom transport
	if client.Transport != nil {
		t.Error("expected nil transport for simple timeout client")
	}
}

func TestWithTimeout_Zero(t *testing.T) {
	client := WithTimeout(0)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Timeout != 0 {
		t.Errorf("expected zero timeout, got %v", client.Timeout)
	}
}

func TestNew_ZeroValuesUseDefaults(t *testing.T) {
	tests := []struct {
		name   string
		cfg    *Config
		expect func(*testing.T, *http.Client)
	}{
		{
			name: "zero timeout uses default",
			cfg:  &Config{Timeout: 0, MaxIdleConnsPerHost: 5},
			expect: func(t *testing.T, c *http.Client) {
				if c.Timeout != DefaultTimeout {
					t.Errorf("expected default timeout, got %v", c.Timeout)
				}
			},
		},
		{
			name: "negative timeout uses default",
			cfg:  &Config{Timeout: -1 * time.Second},
			expect: func(t *testing.T, c *http.Client) {
				if c.Timeout != DefaultTimeout {
					t.Errorf("expected default timeout, got %v", c.Timeout)
				}
			},
		},
		{
			name: "zero max idle conns uses default",
			cfg:  &Config{MaxIdleConnsPerHost: 0},
			expect: func(t *testing.T, c *http.Client) {
				transport := c.Transport.(*http.Transport)
				if transport.MaxIdleConnsPerHost != DefaultMaxIdleConnsPerHost {
					t.Errorf("expected default MaxIdleConnsPerHost, got %d", transport.MaxIdleConnsPerHost)
				}
			},
		},
		{
			name: "negative max idle conns uses default",
			cfg:  &Config{MaxIdleConnsPerHost: -1},
			expect: func(t *testing.T, c *http.Client) {
				transport := c.Transport.(*http.Transport)
				if transport.MaxIdleConnsPerHost != DefaultMaxIdleConnsPerHost {
					t.Errorf("expected default MaxIdleConnsPerHost, got %d", transport.MaxIdleConnsPerHost)
				}
			},
		},
		{
			name: "zero idle conn timeout uses default",
			cfg:  &Config{IdleConnTimeout: 0},
			expect: func(t *testing.T, c *http.Client) {
				transport := c.Transport.(*http.Transport)
				if transport.IdleConnTimeout != DefaultIdleConnTimeout {
					t.Errorf("expected default IdleConnTimeout, got %v", transport.IdleConnTimeout)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := New(tt.cfg)
			tt.expect(t, client)
		})
	}
}

func TestClientIsUsable(t *testing.T) {
	// Verify the client can be used (doesn't panic on basic operations)
	client := New(&Config{
		Timeout:             5 * time.Second,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     30 * time.Second,
	})

	// Create a request (won't execute, just verify client is functional)
	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	// Verify transport is properly configured
	transport := client.Transport.(*http.Transport)
	if transport == nil {
		t.Fatal("transport should not be nil")
	}

	// The request object should be usable with the client
	_ = req
}

func TestMultipleClientsIndependent(t *testing.T) {
	client1 := New(&Config{Timeout: 10 * time.Second})
	client2 := New(&Config{Timeout: 20 * time.Second})

	if client1.Timeout == client2.Timeout {
		t.Error("clients should have different timeouts")
	}
	if client1 == client2 {
		t.Error("clients should be different instances")
	}
}
