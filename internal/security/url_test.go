package security

import "testing"

func TestIsBlockedHost(t *testing.T) {
	tests := []struct {
		url     string
		blocked bool
	}{
		// Should be blocked
		{"http://localhost/test", true},
		{"http://127.0.0.1/test", true},
		{"http://[::1]/test", true},
		{"http://0.0.0.0/test", true},
		{"http://169.254.169.254/latest/meta-data/", true},
		{"http://metadata.google.internal/", true},
		{"http://10.0.0.1/internal", true},
		{"http://172.16.0.1/internal", true},
		{"http://172.31.255.255/internal", true},
		{"http://192.168.1.1/internal", true},
		{"http://[fd00::1]/internal", true},
		{"http://[fe80::1]/internal", true},

		// Should not be blocked
		{"http://deb.debian.org/debian/", false},
		{"http://archive.ubuntu.com/ubuntu/", false},
		{"http://mirror.example.com/debian/", false},
		{"https://packages.example.org/dists/", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := IsBlockedHost(tt.url)
			if got != tt.blocked {
				t.Errorf("IsBlockedHost(%q) = %v, want %v", tt.url, got, tt.blocked)
			}
		})
	}
}

func TestIsDebianRepoURL(t *testing.T) {
	tests := []struct {
		url   string
		valid bool
	}{
		// Valid repo URLs
		{"http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.gz", true},
		{"http://archive.ubuntu.com/ubuntu/pool/main/v/vim/vim_9.0.deb", true},
		{"https://mirror.example.com/debian/dists/stable/Release", true},
		{"http://example.com/ubuntu/pool/universe/", true},

		// Invalid URLs (not repo-like)
		{"http://example.com/api/internal", false},
		{"http://example.com/admin/", false},
		{"http://malicious.com/packages/test.deb", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := IsDebianRepoURL(tt.url)
			if got != tt.valid {
				t.Errorf("IsDebianRepoURL(%q) = %v, want %v", tt.url, got, tt.valid)
			}
		})
	}
}

func TestIsAllowedMirrorURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		allowed bool
	}{
		// Valid - external repo URLs
		{"debian dists", "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.gz", true},
		{"ubuntu pool", "http://archive.ubuntu.com/ubuntu/pool/main/v/vim/vim_9.0.deb", true},
		{"https mirror", "https://mirror.example.com/debian/dists/stable/Release", true},

		// Blocked - internal hosts with repo paths
		{"localhost with dists", "http://localhost/debian/dists/test", false},
		{"127.0.0.1 with pool", "http://127.0.0.1/pool/main/test.deb", false},
		{"private IP with debian", "http://192.168.1.1/debian/pool/test.deb", false},
		{"metadata service", "http://169.254.169.254/debian/dists/", false},

		// Blocked - external hosts without repo paths
		{"external without repo path", "http://example.com/api/packages", false},
		{"random URL", "http://malicious.com/download/test.deb", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAllowedMirrorURL(tt.url)
			if got != tt.allowed {
				t.Errorf("IsAllowedMirrorURL(%q) = %v, want %v", tt.url, got, tt.allowed)
			}
		})
	}
}
