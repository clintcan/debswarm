// Package p2p - Private network support
package p2p

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/pnet"
)

const (
	// PSKKeySize is the size of the pre-shared key in bytes
	PSKKeySize = 32

	// PSK format header
	pskHeader = "/key/swarm/psk/1.0.0/\n/base16/\n"
)

// LoadPSK loads a PSK from a file
func LoadPSK(path string) (pnet.PSK, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open PSK file: %w", err)
	}
	defer f.Close()

	return ParsePSK(f)
}

// ParsePSK parses a PSK from a reader
func ParsePSK(r io.Reader) (pnet.PSK, error) {
	scanner := bufio.NewScanner(r)

	// Read and verify header
	expectedLines := []string{"/key/swarm/psk/1.0.0/", "/base16/"}
	for _, expected := range expectedLines {
		if !scanner.Scan() {
			return nil, fmt.Errorf("unexpected end of PSK file")
		}
		line := strings.TrimSpace(scanner.Text())
		if line != expected {
			return nil, fmt.Errorf("invalid PSK header: expected %q, got %q", expected, line)
		}
	}

	// Read the hex-encoded key
	if !scanner.Scan() {
		return nil, fmt.Errorf("PSK file missing key data")
	}

	keyHex := strings.TrimSpace(scanner.Text())
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid PSK hex encoding: %w", err)
	}

	if len(key) != PSKKeySize {
		return nil, fmt.Errorf("invalid PSK length: expected %d bytes, got %d", PSKKeySize, len(key))
	}

	return pnet.PSK(key), nil
}

// GeneratePSK generates a new random PSK
func GeneratePSK() (pnet.PSK, error) {
	key := make([]byte, PSKKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate random key: %w", err)
	}
	return pnet.PSK(key), nil
}

// SavePSK saves a PSK to a file with restricted permissions
func SavePSK(psk pnet.PSK, path string) error {
	// Create file with restricted permissions (owner read/write only)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create PSK file: %w", err)
	}
	defer f.Close()

	// Write header and key
	content := FormatPSK(psk)
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("failed to write PSK: %w", err)
	}

	return nil
}

// FormatPSK returns the PSK in the standard format
func FormatPSK(psk pnet.PSK) string {
	return pskHeader + hex.EncodeToString(psk) + "\n"
}

// PSKFingerprint returns a SHA256 fingerprint of the PSK (safe to log)
func PSKFingerprint(psk pnet.PSK) string {
	hash := sha256.Sum256(psk)
	return hex.EncodeToString(hash[:8]) // First 8 bytes as hex
}

// ParsePSKFromHex parses a PSK from a hex string (for inline config)
func ParsePSKFromHex(hexStr string) (pnet.PSK, error) {
	key, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid PSK hex: %w", err)
	}

	if len(key) != PSKKeySize {
		return nil, fmt.Errorf("invalid PSK length: expected %d bytes, got %d", PSKKeySize, len(key))
	}

	return pnet.PSK(key), nil
}
