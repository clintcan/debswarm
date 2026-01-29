package cache

import (
	"testing"
)

func FuzzParseDebFilename(f *testing.F) {
	// Seed corpus with valid examples
	f.Add("curl_7.88.1-10+deb12u5_amd64.deb")
	f.Add("libssl3_3.0.11-1~deb12u2_amd64.deb")
	f.Add("pool/main/c/curl/curl_7.88.1-10_amd64.deb")
	f.Add("linux-image-6.1.0-18-amd64_6.1.76-1_amd64.deb")
	f.Add("g++_12.2.0-14_amd64.deb")
	f.Add("libc6_2.36-9+deb12u4_i386.deb")

	// Edge cases
	f.Add("")
	f.Add(".deb")
	f.Add("___.deb")
	f.Add("a_b_c.deb")
	f.Add("pkg.deb")
	f.Add("pkg_version.deb")
	f.Add("name__arch.deb")
	f.Add("name_version_.deb")
	f.Add("../../../etc/passwd")
	f.Add("pkg_1.0_all.DEB")
	f.Add("pkg_1.0_all.Deb")

	f.Fuzz(func(t *testing.T, input string) {
		name, version, arch, ok := ParseDebFilename(input)

		// If parsing succeeded, verify invariants
		if ok {
			// Name, version, and arch must all be non-empty
			if name == "" {
				t.Error("ok=true but name is empty")
			}
			if version == "" {
				t.Error("ok=true but version is empty")
			}
			if arch == "" {
				t.Error("ok=true but arch is empty")
			}
		}

		// Function should never panic (implicit - test framework catches panics)

		// If ok=false, returned values should be empty
		if !ok {
			if name != "" || version != "" || arch != "" {
				t.Error("ok=false but returned non-empty values")
			}
		}
	})
}
