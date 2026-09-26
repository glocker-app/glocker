package stats

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"glocker/internal/store"
)

// solveAltchaTest brute-forces a challenge the way the browser would.
func solveAltchaTest(ch altchaChallenge) string {
	for n := 0; n <= ch.MaxNumber; n++ {
		if sha256Hex(ch.Salt+strconv.Itoa(n)) == ch.Challenge {
			b, _ := json.Marshal(altchaSolution{
				Algorithm: ch.Algorithm, Challenge: ch.Challenge,
				Number: int64(n), Salt: ch.Salt, Signature: ch.Signature,
			})
			return base64.StdEncoding.EncodeToString(b)
		}
	}
	return ""
}

func reencode(sol altchaSolution) string {
	b, _ := json.Marshal(sol)
	return base64.StdEncoding.EncodeToString(b)
}

func decodeSol(t *testing.T, payload string) altchaSolution {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var s altchaSolution
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAltchaVerify(t *testing.T) {
	// verifyAltcha records spent salts in the DB (fleet-shared replay set).
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	db = sdb
	initCaptcha(true)

	ch := newAltchaChallenge()
	payload := solveAltchaTest(ch)
	if payload == "" {
		t.Fatal("could not solve challenge within range")
	}
	if !verifyAltcha(payload) {
		t.Error("a valid solution should verify")
	}
	// Single-use: replaying the same salt is rejected.
	if verifyAltcha(payload) {
		t.Error("replayed solution should be rejected")
	}

	// Tampered number → recomputed hash won't match the (signed) challenge.
	sol := decodeSol(t, solveAltchaTest(newAltchaChallenge()))
	sol.Number++
	if verifyAltcha(reencode(sol)) {
		t.Error("tampered number should be rejected")
	}

	// Forged signature → not our challenge.
	sol = decodeSol(t, solveAltchaTest(newAltchaChallenge()))
	sol.Signature = strings.Repeat("0", len(sol.Signature))
	if verifyAltcha(reencode(sol)) {
		t.Error("forged signature should be rejected")
	}

	// Expired (validly signed but past its expiry) → rejected.
	salt := "deadbeef?expires=" + strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	challenge := sha256Hex(salt + "1")
	expired := altchaSolution{Algorithm: "SHA-256", Challenge: challenge, Number: 1, Salt: salt, Signature: hmacHex(altchaHMAC, challenge)}
	if verifyAltcha(reencode(expired)) {
		t.Error("expired challenge should be rejected")
	}

	// Garbage → rejected, not panic.
	if verifyAltcha("!!not base64!!") {
		t.Error("garbage payload should be rejected")
	}
}

func TestRegisterCaptchaGate(t *testing.T) {
	sdb, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	mux := http.NewServeMux()
	Register(mux, sdb, Options{Auth: true, Mailer: &fakeMailer{}, AppURL: "https://x", Captcha: true})

	// Without a captcha solution, registration is refused.
	rec := do(mux, httptest.NewRequest("POST", "/api/register",
		strings.NewReader(`{"email":"a@b.com","password":"longenough1"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("register without captcha: got %d, want 400", rec.Code)
	}

	// Fetch + solve a real challenge, then register succeeds.
	chRec := do(mux, httptest.NewRequest("GET", "/api/altcha", nil))
	if chRec.Code != http.StatusOK {
		t.Fatalf("challenge endpoint: %d", chRec.Code)
	}
	var ch altchaChallenge
	if err := json.Unmarshal(chRec.Body.Bytes(), &ch); err != nil {
		t.Fatal(err)
	}
	body := `{"email":"a@b.com","password":"longenough1","altcha":"` + solveAltchaTest(ch) + `"}`
	rec = do(mux, httptest.NewRequest("POST", "/api/register", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("register with captcha: got %d %s", rec.Code, rec.Body.String())
	}
}
