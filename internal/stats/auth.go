package stats

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"glocker/internal/store"
)

// sessionCookie is the httpOnly cookie holding the opaque browser session token.
const sessionCookie = "glockpeek_session"

// secureCookies is set from Options; true marks session cookies Secure (for a
// hosted instance served over HTTPS, even when TLS is terminated by a proxy).
var secureCookies bool

// Mailer is the subset of email sending the dashboard needs (account
// verification). *mailer.Mailer satisfies it; tests inject a fake.
type Mailer interface {
	Enabled() bool
	Send(ctx context.Context, to, subject, text, html string) error
}

// mail + appURL are set from Options; used by the registration flow.
var (
	mail   Mailer
	appURL string
)

// authEnabled gates whether logins/tokens are required. When false (the
// self-hosted default), every request runs as the single implicit account
// defaultUserID — no login, no token.
var (
	authEnabled   bool
	defaultUserID uint
)

// adminEmail names the account granted admin powers (user management). Set from
// Options.AdminEmail; empty means no admin. Compared case-insensitively.
var adminEmail string

// isAdmin reports whether u is the configured admin account.
func isAdmin(u *store.User) bool {
	return adminEmail != "" && u != nil && strings.EqualFold(strings.TrimSpace(u.Email), adminEmail)
}

// requireAdmin wraps requireUser and additionally requires the admin account.
func requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !isAdmin(userFrom(r)) {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

// defaultUser is the implicit account injected when auth is disabled.
func defaultUser() *store.User {
	return &store.User{ID: defaultUserID, Email: store.DefaultEmail}
}

type ctxKey int

const userKey ctxKey = 0

// userFrom returns the authenticated user attached by requireUser/requireToken.
func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// withUser stores the user on the request context.
func withUser(r *http.Request, u *store.User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userKey, u))
}

// requireUser gates a handler behind a valid browser session (cookie). With auth
// disabled it injects the implicit account instead.
func requireUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authEnabled {
			h(w, withUser(r, defaultUser()))
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		u, err := db.UserBySession(c.Value)
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		h(w, withUser(r, u))
	}
}

// requireToken gates a handler behind a valid API bearer token (the syncer).
// With auth disabled it injects the implicit account, so a local syncer can push
// without a token.
func requireToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authEnabled {
			h(w, withUser(r, defaultUser()))
			return
		}
		tok := bearer(r)
		u, err := db.UserByAPIToken(tok)
		if err != nil {
			http.Error(w, "invalid or missing API token", http.StatusUnauthorized)
			return
		}
		h(w, withUser(r, u))
	}
}

// requireSessionOrToken accepts EITHER a browser session (cookie) or an API
// bearer token, injecting the resolved account. It backs read endpoints the agent
// needs (fetching its own key vault via its ingest token) that a human also reads
// from the dashboard. With auth disabled it injects the implicit account.
func requireSessionOrToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authEnabled {
			h(w, withUser(r, defaultUser()))
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil {
			if u, err := db.UserBySession(c.Value); err == nil {
				h(w, withUser(r, u))
				return
			}
		}
		if u, err := db.UserByAPIToken(bearer(r)); err == nil {
			h(w, withUser(r, u))
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	}
}

// bearer extracts a token from "Authorization: Bearer <tok>".
func bearer(r *http.Request) string {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}

// handleLogin authenticates and starts a session (public route).
func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u, err := db.Authenticate(in.Email, in.Password)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCredentials) {
			http.Error(w, "invalid email or password", http.StatusUnauthorized)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !u.Verified {
		http.Error(w, "please confirm your email before signing in", http.StatusForbidden)
		return
	}
	tok, err := db.CreateSession(u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(store.SessionTTL.Seconds()),
	})
	writeJSON(w, map[string]any{"user": map[string]any{"id": u.ID, "email": u.Email}})
}

// handleLogout ends the current session (requires a session).
func handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = db.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: secureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	writeJSON(w, map[string]any{"ok": true})
}

// handleMe returns the current account plus whether auth is enabled (so the
// frontend can hide the sign-out control in single-user mode).
func handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	writeJSON(w, map[string]any{"id": u.ID, "email": u.Email, "auth": authEnabled, "admin": isAdmin(u), "e2e": db.HasVault(u.ID), "encrypted": db.AccountEncrypted(u.ID)})
}
