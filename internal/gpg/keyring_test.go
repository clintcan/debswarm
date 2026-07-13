package gpg

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

// genTestKey creates a throwaway OpenPGP entity for tests.
func genTestKey(t *testing.T) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity("debswarm test", "test", "test@example.com", nil)
	if err != nil {
		t.Fatalf("NewEntity: %v", err)
	}
	return e
}

// writeBinaryPubKey serializes the entity's public key in binary form.
func writeBinaryPubKey(t *testing.T, e *openpgp.Entity, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := e.Serialize(f); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
}

// writeArmoredPubKey serializes the entity's public key in ASCII-armored form.
func writeArmoredPubKey(t *testing.T, e *openpgp.Entity, path string) {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatalf("armor.Encode: %v", err)
	}
	if err := e.Serialize(w); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("armor close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoad_BinaryAndArmoredFromDir(t *testing.T) {
	dir := t.TempDir()
	writeBinaryPubKey(t, genTestKey(t), filepath.Join(dir, "a.gpg"))
	writeArmoredPubKey(t, genTestKey(t), filepath.Join(dir, "b.asc"))
	// A non-key file in the dir must be ignored (wrong extension).
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}

	k, err := Load(nil, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if k.Empty() {
		t.Fatal("keyring empty, want 2 keys")
	}
	if k.Count() != 2 {
		t.Fatalf("Count = %d, want 2", k.Count())
	}
}

func TestLoad_ExplicitFileRegardlessOfExtension(t *testing.T) {
	dir := t.TempDir()
	// No key extension, but passed explicitly as a file path — must be read
	// (mirrors a bare /etc/apt/trusted.gpg style path).
	p := filepath.Join(dir, "keyring-noext")
	writeBinaryPubKey(t, genTestKey(t), p)

	k, err := Load(nil, p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if k.Count() != 1 {
		t.Fatalf("Count = %d, want 1", k.Count())
	}
}

func TestLoad_MissingPathSkipped(t *testing.T) {
	k, err := Load(nil, filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing path should not error: %v", err)
	}
	if !k.Empty() {
		t.Fatalf("expected empty keyring, got %d keys", k.Count())
	}
}

func TestLoad_BadFileSkippedOthersLoad(t *testing.T) {
	dir := t.TempDir()
	// A garbage .gpg file must be skipped, not fatal.
	if err := os.WriteFile(filepath.Join(dir, "bad.gpg"), []byte("\x00\x01not a real keyring\xff"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeBinaryPubKey(t, genTestKey(t), filepath.Join(dir, "good.gpg"))

	k, err := Load(nil, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if k.Count() != 1 {
		t.Fatalf("Count = %d, want 1 (bad file skipped, good file loaded)", k.Count())
	}
}

func TestLoadAPT_ExtraPathLoads(t *testing.T) {
	dir := t.TempDir()
	writeArmoredPubKey(t, genTestKey(t), filepath.Join(dir, "extra.asc"))

	// The default APT paths won't exist on the test host and are skipped; the
	// extra path must still contribute its key.
	k, err := LoadAPT(nil, dir)
	if err != nil {
		t.Fatalf("LoadAPT: %v", err)
	}
	if k.Count() < 1 {
		t.Fatalf("Count = %d, want >= 1 from extra path", k.Count())
	}
}

func TestLoadAPT_BlankExtraIgnored(t *testing.T) {
	// Blank/whitespace extra paths (an unset keyring_path) must not be treated
	// as a path.
	if _, err := LoadAPT(nil, "", "   "); err != nil {
		t.Fatalf("LoadAPT with blank extras: %v", err)
	}
}

func TestIsArmored(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"armored", []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n"), true},
		{"armored leading ws", []byte("\n  -----BEGIN PGP PUBLIC KEY BLOCK-----\n"), true},
		{"binary", []byte{0x99, 0x01, 0x0d}, false},
		{"empty", []byte{}, false},
	}
	for _, c := range cases {
		if got := isArmored(c.data); got != c.want {
			t.Errorf("%s: isArmored = %v, want %v", c.name, got, c.want)
		}
	}
}
