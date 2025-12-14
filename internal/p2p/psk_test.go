package p2p

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratePSK(t *testing.T) {
	psk, err := GeneratePSK()
	if err != nil {
		t.Fatalf("GeneratePSK failed: %v", err)
	}

	if len(psk) != PSKKeySize {
		t.Errorf("PSK length = %d, want %d", len(psk), PSKKeySize)
	}
}

func TestGeneratePSK_Uniqueness(t *testing.T) {
	psk1, err := GeneratePSK()
	if err != nil {
		t.Fatalf("GeneratePSK failed: %v", err)
	}

	psk2, err := GeneratePSK()
	if err != nil {
		t.Fatalf("GeneratePSK failed: %v", err)
	}

	if bytes.Equal(psk1, psk2) {
		t.Error("GeneratePSK produced identical keys")
	}
}

func TestSaveAndLoadPSK(t *testing.T) {
	tmpDir := t.TempDir()
	pskPath := filepath.Join(tmpDir, "swarm.key")

	// Generate a PSK
	origPSK, err := GeneratePSK()
	if err != nil {
		t.Fatalf("GeneratePSK failed: %v", err)
	}

	// Save the PSK
	if err := SavePSK(origPSK, pskPath); err != nil {
		t.Fatalf("SavePSK failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(pskPath); os.IsNotExist(err) {
		t.Fatal("SavePSK did not create file")
	}

	// Verify file permissions (should be 0600)
	info, err := os.Stat(pskPath)
	if err != nil {
		t.Fatalf("Failed to stat PSK file: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected file permissions 0600, got %o", perm)
	}

	// Load the PSK back
	loadedPSK, err := LoadPSK(pskPath)
	if err != nil {
		t.Fatalf("LoadPSK failed: %v", err)
	}

	// Verify PSKs match
	if !bytes.Equal(origPSK, loadedPSK) {
		t.Error("Loaded PSK does not match original")
	}
}

func TestParsePSK_ValidFormat(t *testing.T) {
	// Create a valid PSK file content
	keyHex := strings.Repeat("ab", PSKKeySize) // 32 bytes = 64 hex chars
	content := "/key/swarm/psk/1.0.0/\n/base16/\n" + keyHex + "\n"

	psk, err := ParsePSK(strings.NewReader(content))
	if err != nil {
		t.Fatalf("ParsePSK failed: %v", err)
	}

	if len(psk) != PSKKeySize {
		t.Errorf("PSK length = %d, want %d", len(psk), PSKKeySize)
	}

	// Verify the key content
	expectedKey, _ := hex.DecodeString(keyHex)
	if !bytes.Equal(psk, expectedKey) {
		t.Error("Parsed PSK does not match expected key")
	}
}

func TestParsePSK_InvalidHeader(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "wrong version",
			content: "/key/swarm/psk/2.0.0/\n/base16/\n" + strings.Repeat("ab", PSKKeySize),
		},
		{
			name:    "wrong encoding",
			content: "/key/swarm/psk/1.0.0/\n/base64/\n" + strings.Repeat("ab", PSKKeySize),
		},
		{
			name:    "missing encoding line",
			content: "/key/swarm/psk/1.0.0/\n" + strings.Repeat("ab", PSKKeySize),
		},
		{
			name:    "empty file",
			content: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePSK(strings.NewReader(tc.content))
			if err == nil {
				t.Error("ParsePSK should fail with invalid header")
			}
		})
	}
}

func TestParsePSK_InvalidKey(t *testing.T) {
	tests := []struct {
		name    string
		keyPart string
	}{
		{
			name:    "invalid hex",
			keyPart: "not-valid-hex",
		},
		{
			name:    "too short",
			keyPart: "abcd", // Only 2 bytes
		},
		{
			name:    "too long",
			keyPart: strings.Repeat("ab", PSKKeySize+10), // Extra bytes
		},
		{
			name:    "empty key",
			keyPart: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := "/key/swarm/psk/1.0.0/\n/base16/\n" + tc.keyPart
			_, err := ParsePSK(strings.NewReader(content))
			if err == nil {
				t.Error("ParsePSK should fail with invalid key")
			}
		})
	}
}

func TestParsePSK_MissingKeyData(t *testing.T) {
	content := "/key/swarm/psk/1.0.0/\n/base16/\n"
	_, err := ParsePSK(strings.NewReader(content))
	if err == nil {
		t.Error("ParsePSK should fail when key data is missing")
	}
}

func TestLoadPSK_FileNotFound(t *testing.T) {
	_, err := LoadPSK("/nonexistent/path/swarm.key")
	if err == nil {
		t.Error("LoadPSK should fail with nonexistent file")
	}
}

func TestFormatPSK(t *testing.T) {
	keyBytes := make([]byte, PSKKeySize)
	for i := range keyBytes {
		keyBytes[i] = byte(i)
	}

	formatted := FormatPSK(keyBytes)

	// Should start with header
	if !strings.HasPrefix(formatted, "/key/swarm/psk/1.0.0/\n/base16/\n") {
		t.Error("FormatPSK missing correct header")
	}

	// Should end with newline
	if !strings.HasSuffix(formatted, "\n") {
		t.Error("FormatPSK should end with newline")
	}

	// Should be parseable
	parsed, err := ParsePSK(strings.NewReader(formatted))
	if err != nil {
		t.Fatalf("FormatPSK output is not parseable: %v", err)
	}

	if !bytes.Equal(parsed, keyBytes) {
		t.Error("Round-trip through FormatPSK/ParsePSK failed")
	}
}

func TestPSKFingerprint(t *testing.T) {
	psk, err := GeneratePSK()
	if err != nil {
		t.Fatalf("GeneratePSK failed: %v", err)
	}

	fingerprint := PSKFingerprint(psk)

	// Fingerprint should be 16 hex characters (8 bytes)
	if len(fingerprint) != 16 {
		t.Errorf("Fingerprint length = %d, want 16", len(fingerprint))
	}

	// Same PSK should produce same fingerprint
	fingerprint2 := PSKFingerprint(psk)
	if fingerprint != fingerprint2 {
		t.Error("Same PSK produced different fingerprints")
	}

	// Different PSK should produce different fingerprint
	psk2, _ := GeneratePSK()
	fingerprint3 := PSKFingerprint(psk2)
	if fingerprint == fingerprint3 {
		t.Error("Different PSKs produced same fingerprint")
	}
}

func TestParsePSKFromHex(t *testing.T) {
	// Valid hex string
	keyHex := strings.Repeat("ab", PSKKeySize)
	psk, err := ParsePSKFromHex(keyHex)
	if err != nil {
		t.Fatalf("ParsePSKFromHex failed: %v", err)
	}

	if len(psk) != PSKKeySize {
		t.Errorf("PSK length = %d, want %d", len(psk), PSKKeySize)
	}
}

func TestParsePSKFromHex_Invalid(t *testing.T) {
	tests := []struct {
		name   string
		hexStr string
	}{
		{
			name:   "invalid hex",
			hexStr: "not-valid-hex",
		},
		{
			name:   "too short",
			hexStr: "abcd",
		},
		{
			name:   "too long",
			hexStr: strings.Repeat("ab", PSKKeySize+10),
		},
		{
			name:   "empty",
			hexStr: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePSKFromHex(tc.hexStr)
			if err == nil {
				t.Error("ParsePSKFromHex should fail")
			}
		})
	}
}

func TestParsePSK_WhitespaceHandling(t *testing.T) {
	// Test that whitespace around key is handled
	keyHex := strings.Repeat("ab", PSKKeySize)
	content := "/key/swarm/psk/1.0.0/\n/base16/\n  " + keyHex + "  \n"

	psk, err := ParsePSK(strings.NewReader(content))
	if err != nil {
		t.Fatalf("ParsePSK should handle whitespace: %v", err)
	}

	if len(psk) != PSKKeySize {
		t.Errorf("PSK length = %d, want %d", len(psk), PSKKeySize)
	}
}
