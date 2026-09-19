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

var pageTemplates = template.Must(template.ParseFS(templatesFS, "templates/*.tmpl"))

// renderPage executes a named SSR page template.
func renderPage(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
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
