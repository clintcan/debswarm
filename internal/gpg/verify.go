package gpg

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
)

var (
	// ErrNoKeys is returned when verification is attempted with an empty keyring.
	ErrNoKeys = errors.New("gpg: no trusted keys loaded")
	// ErrNotClearsigned is returned when InRelease data is not a clearsigned message.
	ErrNotClearsigned = errors.New("gpg: data is not a clearsigned message")
)

// VerifyClearsigned verifies a clearsigned message — an APT InRelease file —
// against the keyring and returns the verified plaintext body (the Release
// content) on success. Callers MUST parse hashes only from the returned body,
// never from the raw input: the returned bytes are exactly what the signature
// covers.
//
// Note: go-crypto (like the modern OpenPGP spec) does not accept legacy v3
// signatures; it silently ignores a v3 signature packet, so verification of such
// a message fails with "unknown entity" even when the issuing key is loaded. Some
// repositories still emit v3 signatures (e.g. pkgs.k8s.io) that only GnuPG will
// check; debswarm cannot verify those and treats them as unverifiable.
func (k *Keyring) VerifyClearsigned(data []byte) ([]byte, error) {
	if k.Empty() {
		return nil, ErrNoKeys
	}
	block, _ := clearsign.Decode(data)
	if block == nil {
		return nil, ErrNotClearsigned
	}
	if _, err := openpgp.CheckDetachedSignature(k.entities, bytes.NewReader(block.Bytes), block.ArmoredSignature.Body, nil); err != nil {
		return nil, fmt.Errorf("gpg: clearsign verification failed: %w", err)
	}
	return block.Bytes, nil
}

// VerifyDetached verifies a detached signature — an APT Release.gpg, armored or
// binary — over the given body (the Release file) against the keyring.
func (k *Keyring) VerifyDetached(body, sig []byte) error {
	if k.Empty() {
		return ErrNoKeys
	}
	var err error
	if isArmored(sig) {
		_, err = openpgp.CheckArmoredDetachedSignature(k.entities, bytes.NewReader(body), bytes.NewReader(sig), nil)
	} else {
		_, err = openpgp.CheckDetachedSignature(k.entities, bytes.NewReader(body), bytes.NewReader(sig), nil)
	}
	if err != nil {
		return fmt.Errorf("gpg: detached verification failed: %w", err)
	}
	return nil
}
