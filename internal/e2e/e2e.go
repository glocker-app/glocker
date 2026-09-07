// Package e2e holds glockpeek's end-to-end encryption primitives, built up one
// step at a time (see docs/e2e-design.md). So far: generate a master key,
// wrap/unwrap it under a passphrase, and encrypt/decrypt records under it. The
// recovery-code path lands later. The browser (native WebCrypto — PBKDF2 + AES-GCM,
// no wasm) must match each primitive as it's added.
//
//   - master key (MK):  32 random bytes (AES-256)
//   - KEK = PBKDF2-HMAC-SHA256(passphrase, salt, iterations); wrappedMK = AES-256-GCM(KEK, MK)
//   - record blob = AES-256-GCM(MK, plaintext, aad)   ← the thing sent to the server
package e2e

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// recordIDSep separates the fields of a record's canonical identity. It's the
// ASCII unit separator (0x1f) — it can't appear in a domain, URL, keyword, or
// decimal timestamp, so the joined form is unambiguous.
const recordIDSep = "\x1f"

// RecordID is a stable, content-hiding identity for a synced record — the dedup
// key the server stores in place of the (now-encrypted) natural-key columns, so a
// re-sync of the same record is idempotent even though every field re-encrypts to
// fresh ciphertext.
//
// With a key (E2E on) it's HMAC-SHA256(key, canonical): keyed so the server, which
// never holds the master key, can't dictionary-attack low-entropy content (e.g. a
// domain) back out of the id. With no key (nil/empty — local plaintext mode) it's
// a plain SHA-256 of the canonical form, which is fine there because the server
// sees the plaintext anyway. Same canonical form both ways, so the id is stable
// across a plaintext→ciphertext migration only if the key is stable — which is why
// the migration purges and re-syncs rather than trying to preserve ids.
//
// parts are joined with recordIDSep; include a kind prefix and the timestamp so
// ids can't collide across record types. The browser mirrors this (step 6) to
// match encrypted records to dashboard-local state.
func RecordID(key []byte, parts ...string) string {
	canonical := []byte(strings.Join(parts, recordIDSep))
	if len(key) > 0 {
		mac := hmac.New(sha256.New, key)
		mac.Write(canonical)
		return hex.EncodeToString(mac.Sum(nil))
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

const (
	// MasterKeyLen is 32 bytes = AES-256.
	MasterKeyLen = 32
	// SaltLen is the PBKDF2 salt length.
	SaltLen = 16
	// nonceLen is the AES-GCM standard nonce size. Both Go and WebCrypto default
	// to 12; don't change without changing both sides.
	nonceLen = 12
)

// ErrBadKeyLen is returned when a key isn't exactly MasterKeyLen bytes.
var ErrBadKeyLen = errors.New("e2e: key must be 32 bytes")

// GenerateMasterKey returns a fresh random 256-bit master key — the root secret
// all record encryption derives from. It's generated on the client and must never
// be sent to the server in the clear.
func GenerateMasterKey() ([]byte, error) { return randomBytes(MasterKeyLen) }

// KDFParams are the PBKDF2 cost parameters, stored in each vault so the iteration
// count can be raised later without breaking existing vaults.
type KDFParams struct {
	Iterations int `json:"iterations"`
}

// DefaultKDF is PBKDF2-HMAC-SHA256 at 600,000 iterations (OWASP 2023 / Bitwarden's
// long-time default). PBKDF2 is native in both WebCrypto and the Go stdlib, so no
// wasm is needed; the cost of the choice is that it's not memory-hard (see
// docs/e2e-design.md).
var DefaultKDF = KDFParams{Iterations: 600_000}

// DeriveKEK derives a 256-bit key-encryption key from a passphrase and salt via
// PBKDF2-HMAC-SHA256. Deterministic — same inputs give the same key on any
// platform, which is what lets the browser (WebCrypto PBKDF2) and the agent reach
// the same key from one passphrase.
func DeriveKEK(passphrase, salt []byte, p KDFParams) ([]byte, error) {
	return pbkdf2.Key(sha256.New, string(passphrase), salt, p.Iterations, 32)
}

// Box is one AES-GCM ciphertext: a random nonce and the ciphertext-with-tag. It's
// the on-the-wire shape of both a wrapped key and an encrypted record. Marshals to
// JSON with base64 fields (Go encodes []byte as base64).
type Box struct {
	Nonce []byte `json:"nonce"`
	Ct    []byte `json:"ct"`
}

// EncryptRecord encrypts one record's plaintext under the master key. aad
// ("additional authenticated data") is authenticated but not encrypted, and
// should uniquely bind the record (e.g. user|kind|id|timestamp) so the server
// can't move a blob to a different record without decryption failing. This Box is
// exactly what gets stored server-side.
func EncryptRecord(mk, plaintext, aad []byte) (Box, error) { return seal(mk, plaintext, aad) }

// DecryptRecord reverses EncryptRecord. It fails (authentication error) if the
// key, nonce, ciphertext, or aad don't all match — so tampering, a wrong key, or
// the wrong aad are all caught.
func DecryptRecord(mk []byte, b Box, aad []byte) ([]byte, error) { return open(mk, b, aad) }

// seal encrypts plaintext under a 32-byte key with AES-256-GCM and a fresh random
// nonce.
func seal(key, plaintext, aad []byte) (Box, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return Box{}, err
	}
	nonce, err := randomBytes(nonceLen)
	if err != nil {
		return Box{}, err
	}
	return Box{Nonce: nonce, Ct: gcm.Seal(nil, nonce, plaintext, aad)}, nil
}

// open reverses seal.
func open(key []byte, b Box, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(b.Nonce) != nonceLen {
		return nil, errors.New("e2e: bad nonce length")
	}
	return gcm.Open(nil, b.Nonce, b.Ct, aad)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, ErrBadKeyLen
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("e2e: random: %w", err)
	}
	return b, nil
}
