package index

import "testing"

func TestIsAllowedIndexURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		allowed bool
	}{
		// Valid Debian/Ubuntu URLs
		{
			name:    "valid debian dists URL",
			url:     "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.gz",
			allowed: true,
		},
		{
			name:    "valid ubuntu dists URL",
			url:     "http://archive.ubuntu.com/ubuntu/dists/jammy/main/binary-amd64/Packages.gz",
			allowed: true,
		},
		{
			name:    "valid https URL",
			url:     "https://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.xz",
			allowed: true,
		},

		// SSRF attack vectors - should be blocked
		{
			name:    "localhost",
			url:     "http://localhost/dists/test/Packages",
			allowed: false,
		},
		{
			name:    "127.0.0.1",
			url:     "http://127.0.0.1/dists/test/Packages",
			allowed: false,
		},
		{
			name:    "IPv6 localhost",
			url:     "http://[::1]/dists/test/Packages",
			allowed: false,
		},
		{
			name:    "AWS metadata service",
			url:     "http://169.254.169.254/latest/meta-data/",
			allowed: false,
		},
		{
			name:    "cloud metadata",
			url:     "http://metadata.google.internal/dists/test",
			allowed: false,
		},
		{
			name:    "private network 10.x",
			url:     "http://10.0.0.1/debian/dists/test/Packages",
			allowed: false,
		},
		{
			name:    "private network 172.16.x",
			url:     "http://172.16.0.1/debian/dists/test/Packages",
			allowed: false,
		},
		{
			name:    "private network 192.168.x",
			url:     "http://192.168.1.1/debian/dists/test/Packages",
			allowed: false,
		},
		{
			name:    "zero address",
			url:     "http://0.0.0.0/dists/test/Packages",
			allowed: false,
		},

		// URLs without repository markers - should be blocked
		{
			name:    "random URL without repo markers",
			url:     "http://example.com/api/internal",
			allowed: false,
		},
		{
			name:    "URL without dists or debian/ubuntu",
			url:     "http://mirror.example.com/packages/test.deb",
			allowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAllowedIndexURL(tt.url)
			if got != tt.allowed {
				t.Errorf("isAllowedIndexURL(%q) = %v, want %v", tt.url, got, tt.allowed)
			}
		})
	}
}
