package proxy

import "testing"

func TestServer_UpstreamFetchURL(t *testing.T) {
	s := &Server{httpsUpstreamHosts: []string{"pkgs.k8s.io", "apt.example.com"}}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "upgrades configured host",
			in:   "http://pkgs.k8s.io/core:/stable:/v1.30/deb/Packages",
			want: "https://pkgs.k8s.io/core:/stable:/v1.30/deb/Packages",
		},
		{
			name: "upgrades configured host case-insensitively",
			in:   "http://Pkgs.K8s.IO/core:/stable:/v1.30/deb/Packages",
			want: "https://Pkgs.K8s.IO/core:/stable:/v1.30/deb/Packages",
		},
		{
			name: "upgrades subdomain of configured host",
			in:   "http://cdn.apt.example.com/dists/stable/Release",
			want: "https://cdn.apt.example.com/dists/stable/Release",
		},
		{
			name: "drops explicit :80 on upgrade",
			in:   "http://pkgs.k8s.io:80/deb/Packages",
			want: "https://pkgs.k8s.io/deb/Packages",
		},
		{
			name: "leaves unlisted host unchanged",
			in:   "http://deb.debian.org/debian/dists/stable/Release",
			want: "http://deb.debian.org/debian/dists/stable/Release",
		},
		{
			name: "leaves already-HTTPS URL unchanged",
			in:   "https://pkgs.k8s.io/deb/Packages",
			want: "https://pkgs.k8s.io/deb/Packages",
		},
		{
			name: "leaves non-http scheme unchanged",
			in:   "ftp://pkgs.k8s.io/deb/Packages",
			want: "ftp://pkgs.k8s.io/deb/Packages",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.upstreamFetchURL(tc.in); got != tc.want {
				t.Errorf("upstreamFetchURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestServer_UpstreamFetchURL_NoHostsConfigured(t *testing.T) {
	s := &Server{}
	in := "http://pkgs.k8s.io/deb/Packages"
	if got := s.upstreamFetchURL(in); got != in {
		t.Errorf("with no configured hosts, upstreamFetchURL(%q) = %q, want unchanged", in, got)
	}
}

func TestServer_IsHTTPSUpstreamHost(t *testing.T) {
	s := &Server{httpsUpstreamHosts: []string{"pkgs.k8s.io", " Apt.Example.com "}}

	tests := []struct {
		host string
		want bool
	}{
		{"pkgs.k8s.io", true},
		{"PKGS.K8S.IO", true},
		{"cdn.pkgs.k8s.io", true},
		{"apt.example.com", true},
		{"deb.debian.org", false},
		{"evil-pkgs.k8s.io", false}, // not a subdomain, just a suffix of the label
		{"", false},
	}

	for _, tc := range tests {
		if got := s.isHTTPSUpstreamHost(tc.host); got != tc.want {
			t.Errorf("isHTTPSUpstreamHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
