package e2e

import (
	"bytes"
	"testing"
)

func TestEncryptDecryptRecord(t *testing.T) {
	mk, _ := GenerateMasterKey()
	pt := []byte("https://reddit.com/r/distractions")
	aad := []byte("user1|web_access|rec42|1699999999")

	blob, err := EncryptRecord(mk, pt, aad)
	if err != nil {
		t.Fatalf("EncryptRecord: %v", err)
	}
	if bytes.Contains(blob.Ct, pt) {
		t.Fatal("ciphertext contains the plaintext")
	}

	got, err := DecryptRecord(mk, blob, aad)
	if err != nil {
		t.Fatalf("DecryptRecord: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatal("decrypted != original")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	mk, _ := GenerateMasterKey()
	other, _ := GenerateMasterKey()
	blob, _ := EncryptRecord(mk, []byte("secret"), nil)
	if _, err := DecryptRecord(other, blob, nil); err == nil {
		t.Fatal("decrypt with the wrong key should fail")
	}
}

func TestDecryptWrongAADFails(t *testing.T) {
	mk, _ := GenerateMasterKey()
	blob, _ := EncryptRecord(mk, []byte("secret"), []byte("ctxA"))
	if _, err := DecryptRecord(mk, blob, []byte("ctxB")); err == nil {
		t.Fatal("decrypt with the wrong aad should fail — the record is bound to its context")
	}
}

func TestDecryptTamperFails(t *testing.T) {
	mk, _ := GenerateMasterKey()
	blob, _ := EncryptRecord(mk, []byte("secret"), nil)
	blob.Ct[0] ^= 0xff // flip a bit in the ciphertext
	if _, err := DecryptRecord(mk, blob, nil); err == nil {
		t.Fatal("decrypt of tampered ciphertext should fail")
	}
}
