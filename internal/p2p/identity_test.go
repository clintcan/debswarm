package p2p

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
)

func TestGenerateIdentity(t *testing.T) {
	privKey, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity failed: %v", err)
	}

	if privKey == nil {
		t.Fatal("GenerateIdentity returned nil key")
	}

	// Verify it's an Ed25519 key
	if privKey.Type() != crypto.Ed25519 {
		t.Errorf("expected Ed25519 key, got %v", privKey.Type())
	}

	// Verify we can get a public key
	pubKey := privKey.GetPublic()
	if pubKey == nil {
		t.Fatal("GetPublic returned nil")
	}
}

func TestGenerateIdentity_Uniqueness(t *testing.T) {
	key1, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity failed: %v", err)
	}

	key2, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity failed: %v", err)
	}

	// Keys should be different
	bytes1, _ := crypto.MarshalPrivateKey(key1)
	bytes2, _ := crypto.MarshalPrivateKey(key2)

	if string(bytes1) == string(bytes2) {
		t.Error("GenerateIdentity produced identical keys")
	}
}

func TestSaveAndLoadIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "test.key")

	// Generate a key
	origKey, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity failed: %v", err)
	}

	// Save the key
	if err := SaveIdentity(origKey, keyPath); err != nil {
		t.Fatalf("SaveIdentity failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Fatal("SaveIdentity did not create file")
	}

	// Verify file permissions (should be 0600)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Failed to stat key file: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected file permissions 0600, got %o", perm)
	}

	// Load the key back
	loadedKey, err := LoadIdentity(keyPath)
	if err != nil {
		t.Fatalf("LoadIdentity failed: %v", err)
	}

	// Verify keys match
	origBytes, _ := crypto.MarshalPrivateKey(origKey)
	loadedBytes, _ := crypto.MarshalPrivateKey(loadedKey)

	if string(origBytes) != string(loadedBytes) {
		t.Error("Loaded key does not match original")
	}
}

func TestLoadIdentity_InvalidHeader(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "invalid.key")

	// Write file with invalid header
	if err := os.WriteFile(keyPath, []byte("invalid header\n"), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err := LoadIdentity(keyPath)
	if err == nil {
		t.Error("LoadIdentity should fail with invalid header")
	}
}

func TestLoadIdentity_TooShort(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "short.key")

	// Write file that's too short
	if err := os.WriteFile(keyPath, []byte("short"), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err := LoadIdentity(keyPath)
	if err == nil {
		t.Error("LoadIdentity should fail with file too short")
	}
}

func TestLoadIdentity_InvalidHex(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "badhex.key")

	// Write file with valid header but invalid hex
	content := identityHeader + "not-valid-hex\n"
	if err := os.WriteFile(keyPath, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	_, err := LoadIdentity(keyPath)
	if err == nil {
		t.Error("LoadIdentity should fail with invalid hex")
	}
}

func TestLoadIdentity_FileNotFound(t *testing.T) {
	_, err := LoadIdentity("/nonexistent/path/identity.key")
	if err == nil {
		t.Error("LoadIdentity should fail with nonexistent file")
	}
}

func TestLoadOrCreateIdentity_CreateNew(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")

	// Directory doesn't exist yet
	key, err := LoadOrCreateIdentity(dataDir)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity failed: %v", err)
	}

	if key == nil {
		t.Fatal("LoadOrCreateIdentity returned nil key")
	}

	// Verify file was created
	keyPath := filepath.Join(dataDir, IdentityKeyFile)
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Fatal("LoadOrCreateIdentity did not create identity file")
	}
}

func TestLoadOrCreateIdentity_LoadExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial key
	key1, err := LoadOrCreateIdentity(tmpDir)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity failed: %v", err)
	}

	// Load again - should get same key
	key2, err := LoadOrCreateIdentity(tmpDir)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity failed on second call: %v", err)
	}

	// Keys should match
	bytes1, _ := crypto.MarshalPrivateKey(key1)
	bytes2, _ := crypto.MarshalPrivateKey(key2)

	if string(bytes1) != string(bytes2) {
		t.Error("LoadOrCreateIdentity returned different keys for same directory")
	}
}

func TestIdentityFingerprint(t *testing.T) {
	key, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity failed: %v", err)
	}

	fingerprint := IdentityFingerprint(key)

	// Fingerprint should be a valid peer ID string (starts with "12D3" for Ed25519)
	if len(fingerprint) < 10 {
		t.Errorf("Fingerprint too short: %s", fingerprint)
	}

	// Same key should produce same fingerprint
	fingerprint2 := IdentityFingerprint(key)
	if fingerprint != fingerprint2 {
		t.Error("Same key produced different fingerprints")
	}
}

func TestTrimWhitespace(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{" hello ", "hello"},
		{"\thello\t", "hello"},
		{"\nhello\n", "hello"},
		{"\r\nhello\r\n", "hello"},
		{"  hello  world  ", "helloworld"},
		{"", ""},
		{"   ", ""},
		{"\t\n\r ", ""},
	}

	for _, tc := range tests {
		result := trimWhitespace(tc.input)
		if result != tc.expected {
			t.Errorf("trimWhitespace(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}
