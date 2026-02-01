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

		// Linux Mint URLs
		{"http://packages.linuxmint.com/dists/zara/InRelease", true},
		{"http://packages.linuxmint.com/pool/main/m/mint-meta/mint-meta_1.0.deb", true},
		{"http://example.com/linuxmint/pool/main/", true},

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

		// Valid - Linux Mint
		{"mint dists", "http://packages.linuxmint.com/dists/zara/InRelease", true},
		{"mint pool", "http://packages.linuxmint.com/pool/main/m/mint-meta/mint-meta_1.0.deb", true},

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

func TestIsAllowedConnectTarget(t *testing.T) {
	tests := []struct {
		name     string
		hostPort string
		allowed  bool
	}{
		// Valid Debian/Ubuntu mirrors on standard ports
		{"deb.debian.org:443", "deb.debian.org:443", true},
		{"archive.ubuntu.com:443", "archive.ubuntu.com:443", true},
		{"security.debian.org:443", "security.debian.org:443", true},
		{"security.ubuntu.com:443", "security.ubuntu.com:443", true},
		{"deb.debian.org:80", "deb.debian.org:80", true},
		{"mirrors.kernel.org:443", "mirrors.kernel.org:443", true},
		{"mirror.example.com:443", "mirror.example.com:443", true},
		{"ftp.debian.org:443", "ftp.debian.org:443", true},

		// Valid Linux Mint mirrors
		{"packages.linuxmint.com:443", "packages.linuxmint.com:443", true},
		{"packages.linuxmint.com:80", "packages.linuxmint.com:80", true},

		// Blocked - non-standard ports
		{"debian on port 8080", "deb.debian.org:8080", false},
		{"debian on port 22", "deb.debian.org:22", false},
		{"debian on port 3128", "deb.debian.org:3128", false},

		// Blocked - private/internal hosts
		{"localhost:443", "localhost:443", false},
		{"127.0.0.1:443", "127.0.0.1:443", false},
		{"192.168.1.1:443", "192.168.1.1:443", false},
		{"10.0.0.1:443", "10.0.0.1:443", false},
		{"172.16.0.1:443", "172.16.0.1:443", false},
		{"169.254.169.254:443", "169.254.169.254:443", false},

		// Blocked - unknown hosts
		{"random.com:443", "random.com:443", false},
		{"evil.com:443", "evil.com:443", false},
		{"example.com:443", "example.com:443", false},

		// Host without port (defaults to 443)
		{"deb.debian.org no port", "deb.debian.org", true},
		{"localhost no port", "localhost", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAllowedConnectTarget(tt.hostPort)
			if got != tt.allowed {
				t.Errorf("IsAllowedConnectTarget(%q) = %v, want %v", tt.hostPort, got, tt.allowed)
			}
		})
	}
}

func TestIsKnownDebianMirror(t *testing.T) {
	tests := []struct {
		host    string
		isKnown bool
	}{
		{"deb.debian.org", true},
		{"archive.debian.org", true},
		{"security.debian.org", true},
		{"archive.ubuntu.com", true},
		{"security.ubuntu.com", true},
		{"packages.linuxmint.com", true},
		{"mirrors.example.com", true},
		{"mirror.example.org", true},
		{"ftp.us.debian.org", true},
		{"random.example.com", false},
		{"evil.com", false},
		{"google.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			got := isKnownDebianMirror(tt.host)
			if got != tt.isKnown {
				t.Errorf("isKnownDebianMirror(%q) = %v, want %v", tt.host, got, tt.isKnown)
			}
		})
	}
}

func TestIsAllowedMirrorURLWithHosts(t *testing.T) {
	allowedHosts := []string{
		"download.docker.com",
		"ppa.launchpad.net",
		"apt.postgresql.org",
	}

	tests := []struct {
		name    string
		url     string
		allowed bool
	}{
		// Built-in hosts still work
		{"debian dists", "http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.gz", true},
		{"ubuntu pool", "http://archive.ubuntu.com/ubuntu/pool/main/v/vim/vim_9.0.deb", true},
		{"mint dists", "http://packages.linuxmint.com/dists/zara/InRelease", true},

		// Configured hosts work with Debian-style paths
		{"docker dists", "https://download.docker.com/linux/ubuntu/dists/noble/InRelease", true},
		{"docker pool", "https://download.docker.com/linux/ubuntu/pool/stable/amd64/docker-ce.deb", true},
		{"ppa dists", "http://ppa.launchpad.net/user/ppa/ubuntu/dists/noble/InRelease", true},
		{"postgresql dists", "https://apt.postgresql.org/pub/repos/apt/dists/noble-pgdg/InRelease", true},

		// Configured hosts without Debian-style paths are blocked
		{"docker no path", "https://download.docker.com/linux/static/stable/x86_64/docker.tgz", false},

		// Non-configured hosts are blocked
		{"random with dists", "http://evil.com/dists/stable/InRelease", false},
		{"not configured", "https://packages.microsoft.com/dists/noble/InRelease", false},

		// Private hosts always blocked
		{"localhost with dists", "http://localhost/dists/stable/InRelease", false},
		{"private ip with dists", "http://192.168.1.1/dists/stable/InRelease", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAllowedMirrorURLWithHosts(tt.url, allowedHosts)
			if got != tt.allowed {
				t.Errorf("IsAllowedMirrorURLWithHosts(%q, allowedHosts) = %v, want %v", tt.url, got, tt.allowed)
			}
		})
	}
}

func TestIsAllowedConnectTargetWithHosts(t *testing.T) {
	allowedHosts := []string{
		"download.docker.com",
		"ppa.launchpad.net",
	}

	tests := []struct {
		name     string
		hostPort string
		allowed  bool
	}{
		// Built-in hosts still work
		{"deb.debian.org:443", "deb.debian.org:443", true},
		{"archive.ubuntu.com:443", "archive.ubuntu.com:443", true},
		{"packages.linuxmint.com:443", "packages.linuxmint.com:443", true},

		// Configured hosts work
		{"docker:443", "download.docker.com:443", true},
		{"docker:80", "download.docker.com:80", true},
		{"ppa:443", "ppa.launchpad.net:443", true},

		// Non-standard ports blocked
		{"docker:8080", "download.docker.com:8080", false},

		// Non-configured hosts blocked
		{"microsoft:443", "packages.microsoft.com:443", false},
		{"random:443", "random.com:443", false},

		// Private hosts always blocked
		{"localhost:443", "localhost:443", false},
		{"private:443", "192.168.1.1:443", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAllowedConnectTargetWithHosts(tt.hostPort, allowedHosts)
			if got != tt.allowed {
				t.Errorf("IsAllowedConnectTargetWithHosts(%q, allowedHosts) = %v, want %v", tt.hostPort, got, tt.allowed)
			}
		})
	}
}

func TestIsAllowedWithNilHosts(t *testing.T) {
	// nil allowedHosts should work (use only built-in)
	if !IsAllowedMirrorURLWithHosts("http://deb.debian.org/debian/dists/stable/InRelease", nil) {
		t.Error("Expected built-in host to be allowed with nil allowedHosts")
	}
	if IsAllowedMirrorURLWithHosts("http://download.docker.com/linux/ubuntu/dists/noble/InRelease", nil) {
		t.Error("Expected non-built-in host to be blocked with nil allowedHosts")
	}

	if !IsAllowedConnectTargetWithHosts("deb.debian.org:443", nil) {
		t.Error("Expected built-in host to be allowed with nil allowedHosts")
	}
	if IsAllowedConnectTargetWithHosts("download.docker.com:443", nil) {
		t.Error("Expected non-built-in host to be blocked with nil allowedHosts")
	}
}
