// Package p2p - Identity key persistence
package p2p

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// IdentityKeyFile is the default filename for the identity key
	IdentityKeyFile = "identity.key"

	// identityHeader is the file format header
	identityHeader = "/debswarm/identity/1.0.0/ed25519/\n"
)

// LoadOrCreateIdentity loads an existing identity key or creates a new one.
// The key is stored in the specified directory with restricted permissions.
func LoadOrCreateIdentity(dataDir string) (crypto.PrivKey, error) {
	keyPath := filepath.Join(dataDir, IdentityKeyFile)

	// Try to load existing key
	if _, err := os.Stat(keyPath); err == nil {
		return LoadIdentity(keyPath)
	}

	// Generate new key
	privKey, err := GenerateIdentity()
	if err != nil {
		return nil, err
	}

	// Ensure directory exists
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create identity directory: %w", err)
	}

	// Save the new key
	if err := SaveIdentity(privKey, keyPath); err != nil {
		return nil, err
	}

	return privKey, nil
}

// GenerateIdentity creates a new Ed25519 identity key
func GenerateIdentity() (crypto.PrivKey, error) {
	privKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate identity key: %w", err)
	}
	return privKey, nil
}

// LoadIdentity loads an identity key from a file
func LoadIdentity(path string) (crypto.PrivKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read identity file: %w", err)
	}

	// Check header
	headerLen := len(identityHeader)
	if len(data) < headerLen {
		return nil, fmt.Errorf("invalid identity file: too short")
	}
	if string(data[:headerLen]) != identityHeader {
		return nil, fmt.Errorf("invalid identity file: wrong header")
	}

	// Decode hex key
	keyHex := string(data[headerLen:])
	// Trim any whitespace/newlines
	keyHex = trimWhitespace(keyHex)

	keyBytes, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid identity file: bad hex encoding: %w", err)
	}

	// Unmarshal the key (use generic unmarshal since MarshalPrivateKey includes type prefix)
	privKey, err := crypto.UnmarshalPrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid identity file: bad key data: %w", err)
	}

	return privKey, nil
}

// SaveIdentity saves an identity key to a file with restricted permissions
func SaveIdentity(privKey crypto.PrivKey, path string) error {
	// Marshal the key
	keyBytes, err := crypto.MarshalPrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("failed to marshal identity key: %w", err)
	}

	// Format: header + hex-encoded key
	content := identityHeader + hex.EncodeToString(keyBytes) + "\n"

	// Write with restricted permissions (owner read/write only)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write identity file: %w", err)
	}

	return nil
}

// IdentityFingerprint returns a safe-to-log fingerprint of the identity
func IdentityFingerprint(privKey crypto.PrivKey) string {
	pubKey := privKey.GetPublic()
	id, err := peer.IDFromPublicKey(pubKey)
	if err != nil {
		return "unknown"
	}
	return id.String()
}

// trimWhitespace removes spaces, tabs, and newlines from a string
func trimWhitespace(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			result = append(result, c)
		}
	}
	return string(result)
}
