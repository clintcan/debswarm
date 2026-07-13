// Package gpg loads trusted OpenPGP public keys (from the host's APT keyrings or
// an operator-provided path) and verifies upstream repository Release signatures
// against them. It exists so debswarm can anchor the SHA256 it trusts to a
// GPG-signed Release, closing the upstream-MITM gap that its own content-hash
// check cannot — see docs/design/upstream-gpg-verification.md. This file is the
// keyring loader; signature verification lives alongside it.
package gpg

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"go.uber.org/zap"
)

// DefaultAPTKeyringPaths are the standard locations APT stores trusted repository
// signing keys. Files are read directly; directories are scanned (non-recursively)
// for key files. A missing path is skipped, so debswarm trusts exactly what APT
// trusts on a normal host with zero configuration.
var DefaultAPTKeyringPaths = []string{
	"/etc/apt/trusted.gpg",   // legacy single binary keyring
	"/etc/apt/trusted.gpg.d", // drop-in dir (*.gpg, *.asc)
	"/usr/share/keyrings",    // distro & third-party keyrings
	"/etc/apt/keyrings",      // modern signed-by= keyrings
}

// keyFileExts are the extensions treated as keyring files when scanning a
// directory. A file passed explicitly as a path is read regardless of extension
// (e.g. a bare /etc/apt/trusted.gpg).
var keyFileExts = map[string]bool{
	".gpg": true,
	".asc": true,
	".pgp": true,
	".pub": true,
}

// Keyring is an immutable set of trusted OpenPGP public keys.
type Keyring struct {
	entities openpgp.EntityList
}

// Empty reports whether the keyring holds no keys. In enforce mode an empty
// keyring means nothing can ever verify (the caller treats it as fatal); in warn
// mode it degrades to "unverified".
func (k *Keyring) Empty() bool { return k == nil || len(k.entities) == 0 }

// Count returns the number of loaded key entities.
func (k *Keyring) Count() int {
	if k == nil {
		return 0
	}
	return len(k.entities)
}

// Entities exposes the underlying key list for signature verification.
func (k *Keyring) Entities() openpgp.EntityList {
	if k == nil {
		return nil
	}
	return k.entities
}

// LoadAPT loads the default APT keyring locations plus any extra paths (e.g. an
// operator-configured keyring_path). It is the normal entry point.
func LoadAPT(logger *zap.Logger, extra ...string) (*Keyring, error) {
	paths := make([]string, 0, len(DefaultAPTKeyringPaths)+len(extra))
	paths = append(paths, DefaultAPTKeyringPaths...)
	for _, e := range extra {
		if strings.TrimSpace(e) != "" {
			paths = append(paths, e)
		}
	}
	return Load(logger, paths...)
}

// Load reads every key from the given paths (files or directories) into one
// keyring. Directories are scanned non-recursively for key files by extension.
// A missing path is skipped, not an error. An individual unreadable or
// unparseable file is logged and skipped, so one malformed keyring cannot disable
// verification for every repo. The returned Keyring may be empty.
func Load(logger *zap.Logger, paths ...string) (*Keyring, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	k := &Keyring{}
	files := collectKeyFiles(logger, paths)
	for _, f := range files {
		n, err := k.loadFile(f)
		if err != nil {
			logger.Warn("failed to load keyring file, skipping", zap.String("file", f), zap.Error(err))
			continue
		}
		if n > 0 {
			logger.Debug("loaded keys", zap.String("file", f), zap.Int("keys", n))
		}
	}
	logger.Info("loaded trusted keyring", zap.Int("keys", len(k.entities)), zap.Int("filesScanned", len(files)))
	return k, nil
}

// collectKeyFiles expands the given paths into a flat list of key files:
// directories are scanned non-recursively for known key extensions; an explicit
// file is included regardless of extension; missing paths are skipped.
func collectKeyFiles(logger *zap.Logger, paths []string) []string {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				logger.Debug("keyring path not accessible, skipping", zap.String("path", p), zap.Error(err))
			}
			continue
		}
		if !info.IsDir() {
			files = append(files, p) // explicit file: read regardless of extension
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			logger.Warn("failed to scan keyring directory", zap.String("path", p), zap.Error(err))
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if keyFileExts[strings.ToLower(filepath.Ext(e.Name()))] {
				files = append(files, filepath.Join(p, e.Name()))
			}
		}
	}
	return files
}

// loadFile reads one keyring file (binary or armored, auto-detected) and appends
// its entities. Returns the number of entities added.
func (k *Keyring) loadFile(path string) (int, error) {
	// #nosec G304 -- path comes from configured keyring dirs / operator config, not client input
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}
	before := len(k.entities)
	var el openpgp.EntityList
	if isArmored(data) {
		el, err = openpgp.ReadArmoredKeyRing(bytes.NewReader(data))
	} else {
		el, err = openpgp.ReadKeyRing(bytes.NewReader(data))
	}
	if err != nil {
		return 0, err
	}
	k.entities = append(k.entities, el...)
	return len(k.entities) - before, nil
}

// isArmored reports whether the data begins with an ASCII-armored PGP block
// (ignoring leading whitespace), vs a raw binary keyring.
func isArmored(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte("-----BEGIN PGP"))
}
