package config

import (
	"slices"
	"testing"
)

// A trusted repository that redirects to another host only works over https://
// if the redirect target is trusted too: APT follows the redirect inside its TLS
// tunnel and opens a second CONNECT to the target. These pairs were observed live.
func TestDefaultTrustedRepos_IncludesRedirectTargets(t *testing.T) {
	redirects := map[string]string{
		"pkgs.k8s.io":        "prod-cdn.packages.k8s.io",
		"mirrors.kernel.org": "mirrors.edge.kernel.org",
	}
	for from, to := range redirects {
		if !slices.Contains(DefaultTrustedRepos, from) {
			t.Errorf("%q is not trusted by default", from)
		}
		if !slices.Contains(DefaultTrustedRepos, to) {
			t.Errorf("%q redirects to %q, which is not trusted: https:// sources for it fail with 403", from, to)
		}
	}
}
