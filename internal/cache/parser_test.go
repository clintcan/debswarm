package cache

import "testing"

func TestParseDebFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantName string
		wantVer  string
		wantArch string
		wantOK   bool
	}{
		{
			name:     "simple package",
			filename: "curl_7.88.1-10_amd64.deb",
			wantName: "curl",
			wantVer:  "7.88.1-10",
			wantArch: "amd64",
			wantOK:   true,
		},
		{
			name:     "package with debian revision",
			filename: "curl_7.88.1-10+deb12u5_amd64.deb",
			wantName: "curl",
			wantVer:  "7.88.1-10+deb12u5",
			wantArch: "amd64",
			wantOK:   true,
		},
		{
			name:     "package with epoch in version",
			filename: "vim_2:9.0.1378-2_amd64.deb",
			wantName: "vim",
			wantVer:  "2:9.0.1378-2",
			wantArch: "amd64",
			wantOK:   true,
		},
		{
			name:     "library package",
			filename: "libssl3_3.0.11-1~deb12u2_amd64.deb",
			wantName: "libssl3",
			wantVer:  "3.0.11-1~deb12u2",
			wantArch: "amd64",
			wantOK:   true,
		},
		{
			name:     "arm64 architecture",
			filename: "nginx_1.22.1-9_arm64.deb",
			wantName: "nginx",
			wantVer:  "1.22.1-9",
			wantArch: "arm64",
			wantOK:   true,
		},
		{
			name:     "all architecture",
			filename: "tzdata_2024a-0+deb12u1_all.deb",
			wantName: "tzdata",
			wantVer:  "2024a-0+deb12u1",
			wantArch: "all",
			wantOK:   true,
		},
		{
			name:     "full path",
			filename: "pool/main/c/curl/curl_7.88.1-10_amd64.deb",
			wantName: "curl",
			wantVer:  "7.88.1-10",
			wantArch: "amd64",
			wantOK:   true,
		},
		{
			name:     "absolute unix path",
			filename: "/var/cache/apt/archives/curl_7.88.1-10_amd64.deb",
			wantName: "curl",
			wantVer:  "7.88.1-10",
			wantArch: "amd64",
			wantOK:   true,
		},
		{
			name:     "package name with underscore",
			filename: "python3_typing_extensions_4.4.0-1_all.deb",
			wantName: "python3_typing_extensions",
			wantVer:  "4.4.0-1",
			wantArch: "all",
			wantOK:   true,
		},
		{
			name:     "i386 architecture",
			filename: "libc6_2.36-9_i386.deb",
			wantName: "libc6",
			wantVer:  "2.36-9",
			wantArch: "i386",
			wantOK:   true,
		},
		{
			name:     "armhf architecture",
			filename: "raspberrypi-kernel_1.20231016-1_armhf.deb",
			wantName: "raspberrypi-kernel",
			wantVer:  "1.20231016-1",
			wantArch: "armhf",
			wantOK:   true,
		},
		// Edge cases - should return ok=false
		{
			name:     "not a deb file",
			filename: "curl_7.88.1-10_amd64.tar.gz",
			wantOK:   false,
		},
		{
			name:     "missing architecture",
			filename: "curl_7.88.1-10.deb",
			wantOK:   false,
		},
		{
			name:     "missing version",
			filename: "curl_amd64.deb",
			wantOK:   false,
		},
		{
			name:     "just package name",
			filename: "curl.deb",
			wantOK:   false,
		},
		{
			name:     "empty string",
			filename: "",
			wantOK:   false,
		},
		{
			name:     "no extension",
			filename: "curl_7.88.1-10_amd64",
			wantOK:   false,
		},
		{
			name:     "uppercase DEB extension",
			filename: "curl_7.88.1-10_amd64.DEB",
			wantName: "curl",
			wantVer:  "7.88.1-10",
			wantArch: "amd64",
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotVer, gotArch, gotOK := ParseDebFilename(tt.filename)

			if gotOK != tt.wantOK {
				t.Errorf("ParseDebFilename(%q) ok = %v, want %v", tt.filename, gotOK, tt.wantOK)
				return
			}

			if !tt.wantOK {
				return // Don't check other values if we expected failure
			}

			if gotName != tt.wantName {
				t.Errorf("ParseDebFilename(%q) name = %q, want %q", tt.filename, gotName, tt.wantName)
			}
			if gotVer != tt.wantVer {
				t.Errorf("ParseDebFilename(%q) version = %q, want %q", tt.filename, gotVer, tt.wantVer)
			}
			if gotArch != tt.wantArch {
				t.Errorf("ParseDebFilename(%q) arch = %q, want %q", tt.filename, gotArch, tt.wantArch)
			}
		})
	}
}

func TestParseDebFilenameFromPath(t *testing.T) {
	name, version, arch := ParseDebFilenameFromPath("pool/main/c/curl/curl_7.88.1-10_amd64.deb")

	if name != "curl" {
		t.Errorf("name = %q, want %q", name, "curl")
	}
	if version != "7.88.1-10" {
		t.Errorf("version = %q, want %q", version, "7.88.1-10")
	}
	if arch != "amd64" {
		t.Errorf("arch = %q, want %q", arch, "amd64")
	}
}

func TestParseDebFilenameFromPathInvalid(t *testing.T) {
	name, version, arch := ParseDebFilenameFromPath("invalid.txt")

	if name != "" || version != "" || arch != "" {
		t.Errorf("expected empty strings for invalid input, got name=%q version=%q arch=%q",
			name, version, arch)
	}
}

func BenchmarkParseDebFilename(b *testing.B) {
	filename := "pool/main/c/curl/curl_7.88.1-10+deb12u5_amd64.deb"
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ParseDebFilename(filename)
	}
}
