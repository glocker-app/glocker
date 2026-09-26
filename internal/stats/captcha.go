package stats

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Altcha-compatible proof-of-work captcha for the open signup endpoint. The
// server issues an HMAC-signed challenge; the browser brute-forces the number
// whose SHA-256 matches, then submits the solution, which the server
// re-verifies. This is the Altcha wire format implemented directly over stdlib
// crypto — no external library or its frontend widget — so a tiny JS solver can
// drive it. It adds per-attempt CPU cost on top of the existing rate limiter.

const (
	altchaMaxNumber = 50000            // upper bound the client searches (~25k hashes avg)
	altchaTTL       = 10 * time.Minute // how long an issued challenge stays valid
)

// captchaEnabled gates enforcement (set from Options). altchaHMAC is a
// per-process random key that signs challenges; outstanding challenges die on
// restart, which is fine — the client just fetches a new one.
var (
	captchaEnabled bool
	altchaHMAC     []byte
)

// initCaptcha sets the enable flag and, when on, mints the signing key once.
func initCaptcha(enabled bool) {
	captchaEnabled = enabled
	if enabled && altchaHMAC == nil {
		altchaHMAC = make([]byte, 32)
		_, _ = rand.Read(altchaHMAC)
	}
}

type altchaChallenge struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	MaxNumber int    `json:"maxnumber"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

type altchaSolution struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	Number    int64  `json:"number"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacHex(key []byte, msg string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// handleAltchaChallenge issues a fresh challenge (public GET). Only mounted when
// captcha is enabled.
func handleAltchaChallenge(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, newAltchaChallenge())
}

func newAltchaChallenge() altchaChallenge {
	saltBytes := make([]byte, 12)
	_, _ = rand.Read(saltBytes)
	expires := time.Now().Add(altchaTTL).Unix()
	salt := hex.EncodeToString(saltBytes) + "?expires=" + strconv.FormatInt(expires, 10)

	nb := make([]byte, 4)
	_, _ = rand.Read(nb)
	number := int64(binary.BigEndian.Uint32(nb) % uint32(altchaMaxNumber))

	challenge := sha256Hex(salt + strconv.FormatInt(number, 10))
	return altchaChallenge{
		Algorithm: "SHA-256",
		Challenge: challenge,
		MaxNumber: altchaMaxNumber,
		Salt:      salt,
		Signature: hmacHex(altchaHMAC, challenge),
	}
}

// verifyAltcha checks a base64 Altcha solution payload: it must be our
// (HMAC-signed) challenge, not expired, actually solved, and not replayed.
func verifyAltcha(payloadB64 string) bool {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payloadB64))
	if err != nil {
		return false
	}
	var s altchaSolution
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	if s.Algorithm != "SHA-256" {
		return false
	}
	exp, ok := altchaExpiry(s.Salt)
	if !ok || time.Now().After(exp) {
		return false
	}
	// The challenge must be one we signed (binds salt+number to our key).
	if !hmac.Equal([]byte(hmacHex(altchaHMAC, s.Challenge)), []byte(s.Signature)) {
		return false
	}
	// The proof-of-work must actually solve the challenge.
	want := sha256Hex(s.Salt + strconv.FormatInt(s.Number, 10))
	if subtle.ConstantTimeCompare([]byte(want), []byte(s.Challenge)) != 1 {
		return false
	}
	// Single use within its lifetime — recorded in the DB so the solution can't be
	// replayed on another node. Fail closed on a DB error (a captcha bypass is a
	// security hole, not a UX blip).
	used, uErr := db.CaptchaUseOnce(s.Salt, exp)
	return uErr == nil && used
}

// altchaExpiry pulls the expiry stamped into the salt (…?expires=<unix>).
func altchaExpiry(salt string) (time.Time, bool) {
	i := strings.IndexByte(salt, '?')
	if i < 0 {
		return time.Time{}, false
	}
	q, err := url.ParseQuery(salt[i+1:])
	if err != nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(q.Get("expires"), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}
