package e2e

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWrapUnwrapRoundTrip(t *testing.T) {
	mk, _ := GenerateMasterKey()
	pass := []byte("correct horse battery staple")

	v, code, err := WrapMasterKey(mk, pass)
	if err != nil {
		t.Fatalf("WrapMasterKey: %v", err)
	}
	if code == "" {
		t.Fatal("expected a recovery code")
	}

	got, err := v.Open(pass)
	if err != nil {
		t.Fatalf("Open with correct passphrase: %v", err)
	}
	if !bytes.Equal(got, mk) {
		t.Fatal("unwrapped key != original")
	}
}

func TestOpenWrongPassphraseFails(t *testing.T) {
	mk, _ := GenerateMasterKey()
	v, _, err := WrapMasterKey(mk, []byte("right"))
	if err != nil {
		t.Fatalf("WrapMasterKey: %v", err)
	}
	if _, err := v.Open([]byte("wrong")); err == nil {
		t.Fatal("Open with wrong passphrase should fail (auth error), got nil")
	}
}

func TestRecoveryCodeOpensVault(t *testing.T) {
	mk, _ := GenerateMasterKey()
	v, code, err := WrapMasterKey(mk, []byte("passphrase"))
	if err != nil {
		t.Fatalf("WrapMasterKey: %v", err)
	}

	// The recovery code recovers the exact same master key as the passphrase.
	got, err := v.OpenWithRecovery(code)
	if err != nil {
		t.Fatalf("OpenWithRecovery: %v", err)
	}
	if !bytes.Equal(got, mk) {
		t.Fatal("recovery gave a different key")
	}

	// It's forgiving of case, spaces, and dashes.
	messy := "  " + strings.ToLower(code[:8]) + "-" + code[8:] + " "
	got2, err := v.OpenWithRecovery(messy)
	if err != nil {
		t.Fatalf("OpenWithRecovery(messy): %v", err)
	}
	if !bytes.Equal(got2, mk) {
		t.Fatal("a messy-but-equivalent recovery code should still work")
	}
}

func TestWrongRecoveryCodeFails(t *testing.T) {
	mk, _ := GenerateMasterKey()
	v, _, err := WrapMasterKey(mk, []byte("passphrase"))
	if err != nil {
		t.Fatalf("WrapMasterKey: %v", err)
	}
	other, _, _ := GenerateRecoveryCode()
	if _, err := v.OpenWithRecovery(other); err == nil {
		t.Fatal("a different recovery code should fail")
	}
}

func TestVaultLeaksNoPlaintext(t *testing.T) {
	mk, _ := GenerateMasterKey()
	pass := []byte("hunter2")
	v, code, err := WrapMasterKey(mk, pass)
	if err != nil {
		t.Fatalf("WrapMasterKey: %v", err)
	}
	blob, _ := json.Marshal(v)
	if bytes.Contains(blob, mk) {
		t.Error("serialized vault contains the raw master key")
	}
	if bytes.Contains(blob, pass) {
		t.Error("serialized vault contains the passphrase")
	}
	if bytes.Contains(blob, []byte(code)) {
		t.Error("serialized vault contains the recovery code")
	}
}

func TestVaultSurvivesJSON(t *testing.T) {
	mk, _ := GenerateMasterKey()
	pass := []byte("s3cret")
	v, code, err := WrapMasterKey(mk, pass)
	if err != nil {
		t.Fatalf("WrapMasterKey: %v", err)
	}
	blob, _ := json.Marshal(v)
	var v2 Vault
	if err := json.Unmarshal(blob, &v2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Both open paths survive a JSON round-trip.
	if got, err := v2.Open(pass); err != nil || !bytes.Equal(got, mk) {
		t.Fatalf("passphrase open after JSON failed: %v", err)
	}
	if got, err := v2.OpenWithRecovery(code); err != nil || !bytes.Equal(got, mk) {
		t.Fatalf("recovery open after JSON failed: %v", err)
	}
}

func TestWrapRejectsBadKeyLen(t *testing.T) {
	if _, _, err := WrapMasterKey([]byte("too short"), []byte("pw")); err != ErrBadKeyLen {
		t.Fatalf("want ErrBadKeyLen, got %v", err)
	}
}
