package stats

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"glocker/internal/store"
)

// Password reset — two browser-facing pages (no JSON API, like /verify), so they
// render and submit as plain forms with no inline script (CSP-safe):
//
//	GET  /forgot          — enter your email
//	POST /forgot          — email a reset link if the account exists (generic reply)
//	GET  /reset?token=…    — set a new password (from the emailed link)
//	POST /reset           — consume the token, set the password, sign out everywhere
//
// Resetting the login password is independent of E2E: the vault key is wrapped
// under the encryption passphrase, not the login password, so a reset restores
// dashboard access only — the encrypted data still needs the passphrase.

// handleForgot renders the "forgot password" form (GET) and emails a reset link
// (POST). The POST reply is identical whether or not the address has an account,
// so it can't be used to probe which emails are registered.
func handleForgot(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodPost {
		_, _ = w.Write([]byte(forgotFormHTML("")))
		return
	}
	if !authEnabled {
		http.Error(w, "not available", http.StatusNotFound)
		return
	}
	if ok, _ := db.RateAllow("forgot:"+clientIP(r), 10*time.Minute, 5, time.Now()); !ok {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(resultHTML("Please wait", "Too many requests — wait a minute and try again.", false)))
		return
	}
	if mail == nil || !mail.Enabled() {
		_, _ = w.Write([]byte(resultHTML("Unavailable", "Password reset isn't available right now (email isn't configured).", false)))
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	// Generic confirmation regardless of outcome.
	done := func() {
		_, _ = w.Write([]byte(resultHTML("Check your email",
			"If that email has an account, a password-reset link is on its way. The link expires in 1 hour.", true)))
	}
	if !validEmail(email) {
		done()
		return
	}
	u, err := db.UserByEmail(email)
	if err != nil || !u.Verified {
		done() // no account, or never activated
		return
	}
	// One reset email per TTL per account — don't let a repeat request (or an
	// attacker) bomb a victim with links.
	if db.HasActivePasswordReset(u.ID) {
		done()
		return
	}
	tok, err := db.CreatePasswordResetToken(u.ID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(resultHTML("Something went wrong", "Couldn't start a password reset. Please try again.", false)))
		return
	}
	link := strings.TrimRight(appURL, "/") + "/reset?token=" + url.QueryEscape(tok)
	text := "Reset your glockpeek password:\n" + link +
		"\n\nThis link expires in 1 hour. If you didn't ask for this, ignore this email — nothing changes." +
		"\n\nNote: this resets your login only. Your encrypted data still needs your encryption passphrase."
	if err := mail.Send(r.Context(), u.Email, "Reset your glockpeek password", text, resetEmailHTML(link)); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(resultHTML("Couldn't send the email", "Please try again in a moment.", false)))
		return
	}
	done()
}

// handleReset renders the "set a new password" form (GET, from the emailed link)
// and applies it (POST). On success the account is signed out everywhere.
func handleReset(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodPost {
		tok := r.URL.Query().Get("token")
		if tok == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(resultHTML("Invalid link", "This password-reset link is missing or malformed. Request a new one.", false)))
			return
		}
		_, _ = w.Write([]byte(resetFormHTML(tok, "")))
		return
	}
	if ok, _ := db.RateAllow("reset:"+clientIP(r), 10*time.Minute, 10, time.Now()); !ok {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(resultHTML("Please wait", "Too many attempts — wait a minute and try again.", false)))
		return
	}
	tok := r.FormValue("token")
	pass := r.FormValue("password")
	confirm := r.FormValue("confirm")
	if len(pass) < 8 {
		_, _ = w.Write([]byte(resetFormHTML(tok, "Password must be at least 8 characters.")))
		return
	}
	if pass != confirm {
		_, _ = w.Write([]byte(resetFormHTML(tok, "The two passwords don't match.")))
		return
	}
	if err := db.ResetPassword(tok, pass); err != nil {
		if errors.Is(err, store.ErrInvalidToken) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(resultHTML("Link invalid or expired", "This reset link is no longer valid. Request a new one from the sign-in page.", false)))
			return
		}
		_, _ = w.Write([]byte(resetFormHTML(tok, html.EscapeString(err.Error()))))
		return
	}
	_, _ = w.Write([]byte(resultHTML("Password reset", "Your password has been changed and you've been signed out everywhere. Sign in with your new password.", true)))
}

// ── page HTML (self-contained, inline styles only — no scripts, CSP-safe) ──────

const pwCardOpen = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title></head>
<body style="font-family:system-ui,sans-serif;background:#f4ecdd;color:#33281d;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0">
<div style="max-width:420px;width:100%%;box-sizing:border-box;background:#fffaf0;border:1px solid #e5d7bf;border-radius:16px;padding:36px 32px">`
const pwCardClose = `</div></body></html>`
const pwInput = `width:100%%;box-sizing:border-box;padding:11px 13px;margin:0 0 14px;border:1px solid #d8c7a9;border-radius:10px;font-size:15px;background:#fff`
const pwBtn = `display:inline-block;background:#cf7f24;color:#fffaf0;border:0;text-decoration:none;font-weight:700;font-size:15px;padding:12px 22px;border-radius:999px;cursor:pointer`

func forgotFormHTML(errMsg string) string {
	var e string
	if errMsg != "" {
		e = fmt.Sprintf(`<p style="color:#b4472e;margin:0 0 14px;font-size:14px">%s</p>`, html.EscapeString(errMsg))
	}
	return fmt.Sprintf(pwCardOpen+
		`<h1 style="font-size:22px;margin:0 0 8px">Reset your password</h1>
<p style="color:#6d5f4f;margin:0 0 22px;font-size:14px">Enter your account email and we'll send a reset link.</p>%s
<form method="POST" action="/forgot">
<input name="email" type="email" autocomplete="email" autocapitalize="none" spellcheck="false" placeholder="you@example.com" required style="`+pwInput+`">
<button type="submit" style="`+pwBtn+`">Send reset link</button>
</form>
<p style="margin:22px 0 0;font-size:13px"><a href="/" style="color:#9a6a24">Back to sign in</a></p>`+
		pwCardClose, "Reset your password", e)
}

func resetFormHTML(token, errMsg string) string {
	var e string
	if errMsg != "" {
		e = fmt.Sprintf(`<p style="color:#b4472e;margin:0 0 14px;font-size:14px">%s</p>`, errMsg)
	}
	return fmt.Sprintf(pwCardOpen+
		`<h1 style="font-size:22px;margin:0 0 8px">Choose a new password</h1>%s
<form method="POST" action="/reset">
<input type="hidden" name="token" value="%s">
<input name="password" type="password" autocomplete="new-password" placeholder="New password" minlength="8" required style="`+pwInput+`">
<input name="confirm" type="password" autocomplete="new-password" placeholder="Confirm new password" minlength="8" required style="`+pwInput+`">
<button type="submit" style="`+pwBtn+`">Reset password</button>
</form>
<p style="color:#9a8c78;font-size:12px;margin:18px 0 0">This resets your <b>login</b> only. Your encrypted data still needs your encryption passphrase — a login reset can't recover it.</p>`+
		pwCardClose, "Choose a new password", e, html.EscapeString(token))
}

func resultHTML(title, msg string, ok bool) string {
	accent := "#b4472e"
	if ok {
		accent = "#33281d"
	}
	return fmt.Sprintf(pwCardOpen+
		`<h1 style="font-size:22px;margin:0 0 10px;color:%s">%s</h1>
<p style="color:#6d5f4f;margin:0 0 24px;font-size:14px">%s</p>
<a href="/" style="`+pwBtn+`">Go to sign in</a>`+
		pwCardClose, title, accent, html.EscapeString(title), html.EscapeString(msg))
}

func resetEmailHTML(link string) string {
	return fmt.Sprintf(`<!doctype html><html><body style="font-family:system-ui,sans-serif;background:#f4ecdd;padding:32px;color:#33281d">
<div style="max-width:480px;margin:0 auto;background:#fffaf0;border:1px solid #e5d7bf;border-radius:14px;padding:32px">
<h2 style="margin:0 0 12px">Reset your password</h2>
<p style="color:#6d5f4f;margin:0 0 24px">Click below to choose a new glockpeek login password.</p>
<a href="%s" style="display:inline-block;background:#cf7f24;color:#fffaf0;text-decoration:none;font-weight:700;padding:12px 22px;border-radius:999px">Reset my password</a>
<p style="color:#6d5f4f;font-size:13px;margin:24px 0 0">Or paste this link:<br>%s</p>
<p style="color:#9a8c78;font-size:12px;margin:16px 0 0">This link expires in 1 hour. If you didn't ask for this, ignore this email — nothing changes. This resets your login only; your encrypted data still needs your encryption passphrase.</p>
</div></body></html>`, link, link)
}
