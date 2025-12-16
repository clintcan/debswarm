package sanitize

import (
	"errors"
	"strings"
	"testing"
)

func TestString_Empty(t *testing.T) {
	if got := String(""); got != "" {
		t.Errorf("String(\"\") = %q, want \"\"", got)
	}
}

func TestString_Normal(t *testing.T) {
	input := "http://example.com/pool/main/v/vim/vim_9.0_amd64.deb"
	got := String(input)
	if got != input {
		t.Errorf("String(%q) = %q, want %q", input, got, input)
	}
}

func TestString_Newlines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single newline",
			input: "line1\nline2",
			want:  "line1\\nline2",
		},
		{
			name:  "carriage return",
			input: "line1\rline2",
			want:  "line1\\rline2",
		},
		{
			name:  "CRLF",
			input: "line1\r\nline2",
			want:  "line1\\r\\nline2",
		},
		{
			name:  "fake log injection",
			input: "http://evil.com/\n2024-01-01 INFO Fake entry",
			want:  "http://evil.com/\\n2024-01-01 INFO Fake entry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String(tt.input)
			if got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestString_ControlCharacters(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "tab",
			input: "col1\tcol2",
			want:  "col1\\tcol2",
		},
		{
			name:  "null byte",
			input: "before\x00after",
			want:  "before\\x00after",
		},
		{
			name:  "bell",
			input: "alert\x07here",
			want:  "alert\\x07here",
		},
		{
			name:  "escape sequence",
			input: "text\x1b[31mred\x1b[0m",
			want:  "text\\x1b[31mred\\x1b[0m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := String(tt.input)
			if got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestString_Backslash(t *testing.T) {
	input := "path\\to\\file"
	want := "path\\\\to\\\\file"
	got := String(input)
	if got != want {
		t.Errorf("String(%q) = %q, want %q", input, got, want)
	}
}

func TestString_Truncation(t *testing.T) {
	// Create a string longer than MaxLogStringLength
	long := strings.Repeat("a", MaxLogStringLength+100)
	got := String(long)

	if len(got) > MaxLogStringLength+10 { // Allow some buffer for "..."
		t.Errorf("String did not truncate: len=%d, want <= %d", len(got), MaxLogStringLength+10)
	}

	if !strings.HasSuffix(got, "...") {
		t.Errorf("Truncated string should end with '...', got %q", got[len(got)-10:])
	}
}

func TestString_Unicode(t *testing.T) {
	// Normal unicode should pass through
	input := "パッケージ名"
	got := String(input)
	if got != input {
		t.Errorf("String(%q) = %q, want %q", input, got, input)
	}
}

func TestURL(t *testing.T) {
	// URL should behave the same as String
	input := "http://example.com/path?q=1\ninjected"
	want := "http://example.com/path?q=1\\ninjected"
	got := URL(input)
	if got != want {
		t.Errorf("URL(%q) = %q, want %q", input, got, want)
	}
}

func TestFilename(t *testing.T) {
	input := "package_1.0\nfake.deb"
	want := "package_1.0\\nfake.deb"
	got := Filename(input)
	if got != want {
		t.Errorf("Filename(%q) = %q, want %q", input, got, want)
	}
}

func TestPath(t *testing.T) {
	input := "/var/cache/\n/etc/passwd"
	want := "/var/cache/\\n/etc/passwd"
	got := Path(input)
	if got != want {
		t.Errorf("Path(%q) = %q, want %q", input, got, want)
	}
}

func TestError_Nil(t *testing.T) {
	if got := Error(nil); got != "" {
		t.Errorf("Error(nil) = %q, want \"\"", got)
	}
}

func TestError_WithNewline(t *testing.T) {
	err := errors.New("error\nwith\nnewlines")
	want := "error\\nwith\\nnewlines"
	got := Error(err)
	if got != want {
		t.Errorf("Error(%v) = %q, want %q", err, got, want)
	}
}

func BenchmarkString_Short(b *testing.B) {
	input := "http://example.com/package.deb"
	for i := 0; i < b.N; i++ {
		_ = String(input)
	}
}

func BenchmarkString_WithNewlines(b *testing.B) {
	input := "http://example.com/\ninjected\nlog\nentry"
	for i := 0; i < b.N; i++ {
		_ = String(input)
	}
}

func BenchmarkString_Long(b *testing.B) {
	input := strings.Repeat("a", 1000)
	for i := 0; i < b.N; i++ {
		_ = String(input)
	}
}
