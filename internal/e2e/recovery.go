package e2e

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
)

// A recovery code is a second way to open the vault if the passphrase is
// forgotten. Unlike the passphrase, it's full-entropy random, so its wrapping key
// is derived with a fast HKDF (no slow Argon2 needed — there's nothing to make
// guessing harder against, the code itself is unguessable).

const (
	// recoveryCodeBytes is the recovery code's entropy: 160 bits.
	recoveryCodeBytes = 20
	// recoveryHKDFInfo domain-separates the recovery wrapping key.
	recoveryHKDFInfo = "glocker/e2e:recovery-kek:v1"
	// aadRecoveryWrap binds the recovery wrap to its purpose (distinct from the
	// passphrase wrap), so the two wraps of the same key can't be confused.
	aadRecoveryWrap = "glocker/e2e:mkwrap-recovery:v1"
)

// recoveryEncoding is unpadded base32 — case-insensitive and free of ambiguous
// punctuation, so it's reasonable to read off paper and type back.
var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateRecoveryCode returns a printable recovery code and the raw bytes it
// encodes. Show the code to the user once; derive keys from the raw bytes.
func GenerateRecoveryCode() (code string, raw []byte, err error) {
	raw, err = randomBytes(recoveryCodeBytes)
	if err != nil {
		return "", nil, err
	}
	return recoveryEncoding.EncodeToString(raw), raw, nil
}

// recoveryKEK derives the recovery wrapping key from the code's raw bytes.
func recoveryKEK(raw []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, raw, nil, recoveryHKDFInfo, 32)
}

// decodeRecoveryCode parses a user-entered code back to its raw bytes, forgiving
// case, spaces, and dashes (so grouped/lowercased input still works).
func decodeRecoveryCode(code string) ([]byte, error) {
	clean := strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(code))
	raw, err := recoveryEncoding.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("e2e: invalid recovery code: %w", err)
	}
	return raw, nil
}
