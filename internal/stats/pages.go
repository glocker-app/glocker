package stats

import (
	"embed"
	"html/template"
	"net/http"

	"glocker/internal/store"
)

// templatesFS holds the server-rendered SSR pages (the chrome + flows). The
// encrypted-analytics view and client crypto stay as JS assets under assets/ —
// the server can't see record content, so that part must render in the browser.
//
//go:embed templates/*.tmpl
var templatesFS embed.FS

// The `asset` func resolves a static asset to its URL + SRI hash — a CDN URL
// (content-hashed, integrity-pinned) when a CDN base is configured, else the
// same-origin path. It reads assetManifest, which Register fills before any
// request is served.
var pageTemplates = template.Must(template.New("").Funcs(template.FuncMap{
	"asset": assetOf,
}).ParseFS(templatesFS, "templates/*.tmpl"))

// renderPage executes a named SSR page template.
func renderPage(w http.ResponseWriter, name string, data any) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// setSecurityHeaders hardens the HTML responses that carry the app — and, once the
// vault is unlocked, the decrypted master key in memory. CSP is the important one:
// the templates have NO inline scripts, so 'self' (plus the asset CDN for Chart.js)
// blocks any injected script — the vector that could exfiltrate the key. Styles
// allow 'unsafe-inline' + Google Fonts, a far lower-risk surface than scripts.
func setSecurityHeaders(w http.ResponseWriter) {
	script, style, font := "'self'", "'self' https://fonts.googleapis.com 'unsafe-inline'", "'self' https://fonts.gstatic.com"
	if assetCDNBase != "" { // Chart.js/styles.css come from the CDN when configured
		script += " " + assetCDNBase
		style += " " + assetCDNBase
		font += " " + assetCDNBase
	}
	csp := "default-src 'self'; script-src " + script + "; style-src " + style + "; font-src " + font +
		"; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; object-src 'none'"
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Strict-Transport-Security", "max-age=31536000")
}

// pageUser resolves the signed-in user for a page route, or nil when not signed
// in (hosted mode). Unlike requireUser it never writes a 401 — page gates redirect
// to the login screen instead. In local mode there's an implicit default user.
func pageUser(r *http.Request) *store.User {
	if !authEnabled {
		return defaultUser()
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	u, err := db.UserBySession(c.Value)
	if err != nil {
		return nil
	}
	return u
}

// handleEncryptionSetup serves the E2E setup page at /settings/encryption — the
// SSR replacement for the standalone setup-encryption.html plus the client-side
// `location.href` redirect. The crypto itself stays in the browser
// (encryption-setup.js); the server only gates access and renders the chrome.
func handleEncryptionSetup(w http.ResponseWriter, r *http.Request) {
	if pageUser(r) == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther) // sign in on the dashboard first
		return
	}
	renderPage(w, "encryption.html.tmpl", nil)
}
