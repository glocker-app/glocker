package syncer

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"glocker/internal/config"
	"glocker/internal/e2e"
)

func TestEncPlaintextWhenOff(t *testing.T) {
	s := &Syncer{e2e: false}
	if got := s.enc("https://x.com", 100, "violation", "url"); got != "https://x.com" {
		t.Fatalf("e2e off must pass through unchanged, got %q", got)
	}
}

func TestEncRoundTrip(t *testing.T) {
	mk, _ := e2e.GenerateMasterKey()
	s := &Syncer{e2e: true, mk: mk}

	pt := "https://reddit.com/r/x"
	ct := s.enc(pt, 100, "violation", "url")
	if ct == pt || ct == "" {
		t.Fatalf("expected ciphertext, got %q", ct)
	}

	// The wire form is base64(nonce||ciphertext); decode and decrypt with the
	// same aad the syncer bound.
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	box := e2e.Box{Nonce: raw[:12], Ct: raw[12:]}
	got, err := e2e.DecryptRecord(mk, box, []byte("100|violation|url"))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != pt {
		t.Fatalf("round-trip mismatch: %q", got)
	}

	// A different field/record context must not decrypt (context binding).
	if _, err := e2e.DecryptRecord(mk, box, []byte("100|violation|domain")); err == nil {
		t.Fatal("wrong aad should fail to decrypt")
	}
}

func TestEncEmptyPassesThrough(t *testing.T) {
	mk, _ := e2e.GenerateMasterKey()
	s := &Syncer{e2e: true, mk: mk}
	if got := s.enc("", 1, "violation", "url"); got != "" {
		t.Fatalf("an empty field must stay empty, got %q", got)
	}
}

func TestNewLoadsKeyAndEncrypts(t *testing.T) {
	mk, _ := e2e.GenerateMasterKey()
	keyPath := filepath.Join(t.TempDir(), "e2e.key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(mk)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	on := true
	cfg := &config.Config{}
	cfg.Sync.Encrypt = &on
	cfg.Sync.EncryptKeyFile = keyPath

	s := New(cfg)
	if !s.e2e || s.e2eBroken {
		t.Fatalf("expected e2e on and not broken; e2e=%v broken=%v", s.e2e, s.e2eBroken)
	}
	if got := s.enc("secret", 1, "violation", "url"); got == "secret" || got == "" {
		t.Fatalf("expected encryption via the loaded key, got %q", got)
	}
}

func TestNewE2ERequestedButKeyMissingIsInert(t *testing.T) {
	on := true
	cfg := &config.Config{}
	cfg.Sync.Encrypt = &on
	cfg.Sync.EncryptKeyFile = filepath.Join(t.TempDir(), "nope.key") // does not exist

	s := New(cfg)
	if s.e2e {
		t.Fatal("e2e must be OFF when the key can't load")
	}
	if !s.e2eBroken {
		t.Fatal("e2eBroken must be set so Run refuses to sync (no plaintext leak)")
	}
}
