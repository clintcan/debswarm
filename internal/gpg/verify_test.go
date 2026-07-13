package gpg

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
)

// keyringOf wraps an entity's keys as a Keyring for verification tests. The
// entity carries both its private and public key; verification uses the public.
func keyringOf(entities ...*openpgp.Entity) *Keyring {
	return &Keyring{entities: openpgp.EntityList(entities)}
}

func clearsignBody(t *testing.T, e *openpgp.Entity, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := clearsign.Encode(&buf, e.PrivateKey, nil)
	if err != nil {
		t.Fatalf("clearsign.Encode: %v", err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("clearsign close: %v", err)
	}
	return buf.Bytes()
}

func TestVerifyClearsigned_RoundTripAndTamper(t *testing.T) {
	e := genTestKey(t)
	kr := keyringOf(e)
	body := []byte("Origin: Debian\nSuite: stable\nSHA256:\n " + strings.Repeat("a", 64) + " 1234 main/binary-amd64/Packages\n")

	signed := clearsignBody(t, e, body)

	got, err := kr.VerifyClearsigned(signed)
	if err != nil {
		t.Fatalf("VerifyClearsigned: %v", err)
	}
	if !bytes.Contains(got, []byte("Suite: stable")) {
		t.Fatalf("verified body missing content: %q", got)
	}

	// Tamper with a byte inside the signed body (same length) — must fail.
	tampered := bytes.Replace(signed, []byte("Suite: stable"), []byte("Suite: XXXXXX"), 1)
	if _, err := kr.VerifyClearsigned(tampered); err == nil {
		t.Fatal("tampered clearsigned message verified — must fail")
	}
}

func TestVerifyClearsigned_WrongKey(t *testing.T) {
	signer := genTestKey(t)
	other := genTestKey(t)
	signed := clearsignBody(t, signer, []byte("Suite: stable\n"))

	if _, err := keyringOf(other).VerifyClearsigned(signed); err == nil {
		t.Fatal("clearsigned message verified against an unrelated key — must fail")
	}
}

func TestVerifyClearsigned_NotClearsigned(t *testing.T) {
	e := genTestKey(t)
	if _, err := keyringOf(e).VerifyClearsigned([]byte("Suite: stable\nnot signed at all\n")); !errors.Is(err, ErrNotClearsigned) {
		t.Fatalf("want ErrNotClearsigned, got %v", err)
	}
}

func TestVerifyDetached_ArmoredAndBinary(t *testing.T) {
	e := genTestKey(t)
	kr := keyringOf(e)
	body := []byte("Origin: Debian\nSuite: stable\n")

	var asc bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&asc, e, bytes.NewReader(body), nil); err != nil {
		t.Fatalf("ArmoredDetachSign: %v", err)
	}
	if err := kr.VerifyDetached(body, asc.Bytes()); err != nil {
		t.Fatalf("armored VerifyDetached: %v", err)
	}

	var bin bytes.Buffer
	if err := openpgp.DetachSign(&bin, e, bytes.NewReader(body), nil); err != nil {
		t.Fatalf("DetachSign: %v", err)
	}
	if err := kr.VerifyDetached(body, bin.Bytes()); err != nil {
		t.Fatalf("binary VerifyDetached: %v", err)
	}

	// Tampered body against a valid signature — must fail.
	if err := kr.VerifyDetached([]byte("Origin: Debian\nSuite: STABLE\n"), asc.Bytes()); err == nil {
		t.Fatal("tampered body verified against detached signature — must fail")
	}
}

func TestVerify_EmptyKeyring(t *testing.T) {
	empty := &Keyring{}
	if _, err := empty.VerifyClearsigned([]byte("x")); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("VerifyClearsigned empty keyring: want ErrNoKeys, got %v", err)
	}
	if err := empty.VerifyDetached([]byte("x"), []byte("y")); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("VerifyDetached empty keyring: want ErrNoKeys, got %v", err)
	}
}
