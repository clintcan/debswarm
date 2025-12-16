// Package sanitize provides utilities for sanitizing user-controlled data
// before logging to prevent log injection attacks.
package sanitize

import (
	"strings"
	"unicode"
)

const (
	// MaxLogStringLength is the maximum length for logged strings.
	// Longer strings are truncated with "..." suffix.
	MaxLogStringLength = 500
)

// String sanitizes a user-controlled string for safe logging.
// It replaces control characters (including newlines) with their
// escaped representations and truncates very long strings.
func String(s string) string {
	if len(s) == 0 {
		return s
	}

	// Pre-allocate with some extra space for escapes
	var b strings.Builder
	b.Grow(min(len(s)+32, MaxLogStringLength+16))

	for i, r := range s {
		if i >= MaxLogStringLength {
			b.WriteString("...")
			break
		}

		switch r {
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		case '\\':
			b.WriteString("\\\\")
		default:
			if unicode.IsControl(r) {
				// Replace other control characters with escaped hex
				b.WriteString("\\x")
				b.WriteByte(hexChar(byte(r) >> 4))
				b.WriteByte(hexChar(byte(r) & 0x0f))
			} else {
				b.WriteRune(r)
			}
		}
	}

	return b.String()
}

// URL sanitizes a URL for safe logging.
// URLs are a common injection vector as they come directly from HTTP requests.
func URL(url string) string {
	return String(url)
}

// Filename sanitizes a filename for safe logging.
// Filenames from .deb packages could contain malicious characters.
func Filename(filename string) string {
	return String(filename)
}

// Path sanitizes a file path for safe logging.
func Path(path string) string {
	return String(path)
}

// Error sanitizes an error message for safe logging.
// Error messages may contain user-controlled data.
func Error(err error) string {
	if err == nil {
		return ""
	}
	return String(err.Error())
}

func hexChar(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + b - 10
}
