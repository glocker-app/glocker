package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"glocker/internal/accountability"
	"glocker/internal/store"
)

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func validEmail(e string) bool { return emailRE.MatchString(e) }

// handleRegister creates an unverified account and emails a verification link.
// Public; hosted mode only. No session is issued — the account must confirm its
// email (handleVerify) before it can sign in.
func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !authEnabled {
		http.Error(w, "registration is disabled", http.StatusNotFound)
		return
	}
	if mail == nil || !mail.Enabled() {
		http.Error(w, "registration is unavailable (email not configured)", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Altcha   string `json:"altcha"` // proof-of-work captcha solution
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if captchaEnabled && !verifyAltcha(in.Altcha) {
		http.Error(w, "captcha verification failed", http.StatusBadRequest)
		return
	}
	in.Email = strings.TrimSpace(in.Email)
	if !validEmail(in.Email) {
		http.Error(w, "a valid email address is required", http.StatusBadRequest)
		return
	}
	if len(in.Password) < 8 {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	u, tok, err := db.RegisterUser(in.Email, in.Password)
	if err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			http.Error(w, "that email is already registered", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Send the verification email; if it can't go out, roll the account back so
	// the address is free to try again.
	link := strings.TrimRight(appURL, "/") + "/verify?token=" + url.QueryEscape(tok)
	text := "Welcome to glockpeek.\n\nConfirm your email to activate your account:\n" + link + "\n\nThis link expires in 24 hours. If you didn't sign up, ignore this email."
	if err := mail.Send(r.Context(), u.Email, "Confirm your glockpeek account", text, verifyEmailHTML(link)); err != nil {
		_ = db.DeleteUser(u.ID)
		http.Error(w, "could not send the verification email; please try again", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Check your email to confirm your account."})
}

// handleVerify consumes the token from the emailed link, marks the account
// verified, and shows a small landing page (not JSON — it's opened in a browser).
func handleVerify(w http.ResponseWriter, r *http.Request) {
	u, err := db.VerifyEmail(r.URL.Query().Get("token"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, verifyLandingHTML(false))
		return
	}
	// Best-effort: the scheduler's setup reminder is the fallback if this fails.
	if billingEnabled && mail != nil && mail.Enabled() {
		subj, text, html := accountability.SetupEmail(appURL, false)
		if err := mail.Send(r.Context(), u.Email, subj, text, html); err != nil {
			slog.Warn("setup email failed", "user", u.ID, "err", err)
		}
	}
	_, _ = io.WriteString(w, verifyLandingHTML(true))
}

func verifyEmailHTML(link string) string {
	return fmt.Sprintf(`<!doctype html><html><body style="font-family:system-ui,sans-serif;background:#f4ecdd;padding:32px;color:#33281d">
<div style="max-width:480px;margin:0 auto;background:#fffaf0;border:1px solid #e5d7bf;border-radius:14px;padding:32px">
<h2 style="margin:0 0 12px">Confirm your email</h2>
<p style="color:#6d5f4f;margin:0 0 24px">One click and your glockpeek account is ready.</p>
<a href="%s" style="display:inline-block;background:#cf7f24;color:#fffaf0;text-decoration:none;font-weight:700;padding:12px 22px;border-radius:999px">Confirm my account</a>
<p style="color:#6d5f4f;font-size:13px;margin:24px 0 0">Or paste this link: <br>%s</p>
<p style="color:#9a8c78;font-size:12px;margin:16px 0 0">This link expires in 24 hours. If you didn't sign up, ignore this email.</p>
</div></body></html>`, link, link)
}

func verifyLandingHTML(ok bool) string {
	title, msg := "Email confirmed", "Your account is active — you can sign in now."
	if !ok {
		title, msg = "Link invalid or expired", "This verification link is no longer valid. Try signing up again."
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title></head><body style="font-family:system-ui,sans-serif;background:#f4ecdd;color:#33281d;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0">
<div style="max-width:420px;text-align:center;background:#fffaf0;border:1px solid #e5d7bf;border-radius:16px;padding:40px 32px">
<h1 style="font-size:24px;margin:0 0 12px">%s</h1>
<p style="color:#6d5f4f;margin:0 0 24px">%s</p>
<a href="/" style="display:inline-block;background:#cf7f24;color:#fffaf0;text-decoration:none;font-weight:700;padding:12px 22px;border-radius:999px">Go to sign in</a>
</div></body></html>`, title, title, msg)
}
