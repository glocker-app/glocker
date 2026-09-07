// Package stats serves the glockpeek dashboard (usage + exposure analysis) and
// its JSON API. It is served at the root ("/") by the standalone glockpeek
// process, reading from glockpeek's DB (populated by the glocker syncer via the
// ingest API). The frontend assets are embedded in the binary, so nothing
// external is required at runtime; access is restricted to localhost since the
// page shows personal browsing/usage data.
package stats

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"glocker/internal/store"
)

// assets is a copy of glockpeek-web/public (the standalone node dev app). Keep
// the two in sync; the frontend is written path-agnostically so the same files
// serve at "/" (node) and "/" (here).
//
//go:embed assets
var assetsFS embed.FS

// db is the store the handlers read/write. Set by Register.
var db *store.DB

// Options configures the served dashboard.
type Options struct {
	// Auth turns on logins + ingest tokens. When false (self-hosted default),
	// every request runs as DefaultUserID with no login.
	Auth bool
	// DefaultUserID is the implicit account used when Auth is false.
	DefaultUserID uint
	// SecureCookies marks session cookies Secure (set true when the instance is
	// reached over HTTPS, including behind a TLS-terminating proxy).
	SecureCookies bool
	// Mailer sends account-verification email (registration). Nil disables
	// self-service signup.
	Mailer Mailer
	// AppURL is glockpeek's own public base URL, used to build email links.
	AppURL string
	// AdminEmail names the account granted admin powers (user management). Empty
	// disables the admin panel/endpoints.
	AdminEmail string
	// Captcha turns on the proof-of-work captcha on the signup endpoint.
	Captcha bool
}

// Register mounts the dashboard and its API onto mux, backed by database.
// glockpeek owns the whole listener, so the dashboard is served at the root
// ("/"); legacy "/stats/" 301-redirects there. Static assets and the login route
// are public (so the login page can load); the dashboard data/settings APIs
// require a browser session; the ingest API requires an API bearer token. This
// replaces the old loopback-only guard so the instance can be hosted remotely.
func Register(mux *http.ServeMux, database *store.DB, o Options) {
	db = database
	secureCookies = o.SecureCookies
	authEnabled = o.Auth
	defaultUserID = o.DefaultUserID
	mail = o.Mailer
	appURL = o.AppURL
	adminEmail = strings.TrimSpace(o.AdminEmail)
	initCaptcha(o.Captcha)
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return // embed failure is a build-time bug; nothing to serve
	}
	fileServer := http.FileServer(http.FS(sub))

	// Legacy /stats and /stats/* -> / (dashboard moved to the root).
	redirectToRoot := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusMovedPermanently)
	}
	mux.HandleFunc("/stats", redirectToRoot)
	mux.HandleFunc("/stats/", redirectToRoot)

	// Public: static dashboard assets, login, self-service signup + verify.
	// Registration is unauthenticated and sends real email, so it is rate-limited
	// per client IP: a small burst for honest retries, then a slow refill.
	regLimiter := newIPRateLimiter(rate.Every(10*time.Minute), 3, time.Hour)
	// Login is also unauthenticated; throttle per IP to blunt password guessing
	// while leaving room for a legitimate user's typos: a burst of 10, then ~10/min.
	loginLimiter := newIPRateLimiter(rate.Every(6*time.Second), 10, time.Hour)
	mux.Handle("/", fileServer)
	mux.HandleFunc("/api/login", rateLimited(loginLimiter, handleLogin))
	mux.HandleFunc("/api/register", rateLimited(regLimiter, handleRegister))
	mux.HandleFunc("/verify", handleVerify) // browser link from the email, not /api/
	if o.Captcha {
		// Public: the signup captcha challenge (the frontend fetches + solves it).
		mux.HandleFunc("/api/altcha", handleAltchaChallenge)
	}

	// Session-gated: everything that shows or edits account data.
	mux.HandleFunc("/api/me", requireUser(handleMe))
	mux.HandleFunc("/api/logout", requireUser(handleLogout))
	mux.HandleFunc("/api/data", requireUser(handleData))
	mux.HandleFunc("/api/health", requireUser(handleHealth))
	mux.HandleFunc("/api/rules", requireUser(handleRules))
	mux.HandleFunc("/api/ignored", requireUser(handleIgnored))
	mux.HandleFunc("/api/sync", requireUser(handleSync))
	mux.HandleFunc("/api/tokens", requireUser(handleTokens))
	mux.HandleFunc("/api/vault", requireUser(handleVault))

	// Admin-gated: account management (only the configured admin account).
	mux.HandleFunc("/api/admin/users", requireAdmin(handleAdminUsers))

	// Token-gated: the syncer's ingest endpoint.
	mux.HandleFunc("/api/ingest", requireToken(handleIngest))
}

func handleData(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, buildData(db, userFrom(r).ID, time.Now()))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "counts": db.Counts(userFrom(r).ID)})
}

// ingestPayload is the batch the glocker syncer POSTs. Every field is optional
// so the syncer can send only what changed (or everything, on the one-shot
// backfill). Ingest is idempotent, so overlapping batches are safe.
type ingestPayload struct {
	Violations []store.Violation      `json:"violations"`
	Unblocks   []store.Unblock        `json:"unblocks"`
	Lifecycle  []store.LifecycleEvent `json:"lifecycle"`
	Usage      []store.UsageSample    `json:"usage"`
	Heartbeat  []store.Heartbeat      `json:"heartbeat"`
}

// handleIngest accepts a batch of records from the syncer (POST) and upserts
// them. A GET returns the per-source high-water marks so the syncer can seed its
// cursors from what glockpeek already has (only sending the gap).
func handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"cursors": db.HighWaterMarks(userFrom(r).ID)})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var in ingestPayload
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	uid := userFrom(r).ID
	for _, step := range []func() error{
		func() error { return db.IngestViolations(uid, in.Violations) },
		func() error { return db.IngestUnblocks(uid, in.Unblocks) },
		func() error { return db.IngestLifecycle(uid, in.Lifecycle) },
		func() error { return db.IngestUsage(uid, in.Usage) },
		func() error { return db.IngestHeartbeats(uid, in.Heartbeat) },
	} {
		if err := step(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	// Stamp the last-sync marker the dashboard's sync panel reads.
	_ = db.RecordIngest(uid, store.SyncStatus{
		LastViolations: len(in.Violations), LastUnblocks: len(in.Unblocks),
		LastLifecycle: len(in.Lifecycle), LastUsage: len(in.Usage),
		LastHeartbeat: len(in.Heartbeat),
	})
	writeJSON(w, map[string]any{
		"accepted": map[string]int{
			"violations": len(in.Violations), "unblocks": len(in.Unblocks),
			"lifecycle": len(in.Lifecycle), "usage": len(in.Usage),
			"heartbeat": len(in.Heartbeat),
		},
	})
}

// handleSync reports when the account last received an ingest batch (the daemon's
// sync), the size of that batch, and the current totals — for the dashboard's
// "last sync" panel. lastSyncAt is epoch ms, or null if it has never synced.
func handleSync(w http.ResponseWriter, r *http.Request) {
	uid := userFrom(r).ID
	st, ok, err := db.SyncStatusFor(uid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var lastAt *int64
	if ok {
		ms := st.LastIngestAt.UnixMilli()
		lastAt = &ms
	}
	writeJSON(w, map[string]any{
		"lastSyncAt": lastAt,
		"last": map[string]int{
			"violations": st.LastViolations, "unblocks": st.LastUnblocks,
			"lifecycle": st.LastLifecycle, "usage": st.LastUsage,
			"heartbeat": st.LastHeartbeat,
		},
		"totals": db.Counts(uid),
	})
}

// handleRules serves the usage categorization config: GET returns it, PUT
// replaces it. The dashboard sends the full {rules, colors} on every change.
func handleRules(w http.ResponseWriter, r *http.Request) {
	uid := userFrom(r).ID
	switch r.Method {
	case http.MethodGet:
		rules, err := db.Rules(uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		colors, err := db.Colors(uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, rulesConfig{Rules: toStatsRules(rules), Colors: colors})
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rules, colors := parseConfigBytes(body)
		if err := db.SetRulesConfig(uid, toStoreRules(rules), colors); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, rulesConfig{Rules: rules, Colors: colors})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleIgnored serves the false-positive ignore list: GET returns it, PUT
// replaces it.
func handleIgnored(w http.ResponseWriter, r *http.Request) {
	uid := userFrom(r).ID
	switch r.Method {
	case http.MethodGet:
		list, err := loadIgnored(db, uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ignored": list})
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			Ignored []IgnoredEntry `json:"ignored"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved, err := saveIgnored(in.Ignored, db, uid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ignored": saved})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// toStatsRules / toStoreRules convert between the API rule shape (rules.go) and
// the persisted store.Rule.
func toStatsRules(in []store.Rule) []Rule {
	out := make([]Rule, 0, len(in))
	for _, r := range in {
		out = append(out, Rule{Program: r.Program, Title: r.Title, Tag: r.Tag})
	}
	return out
}

func toStoreRules(in []Rule) []store.Rule {
	out := make([]store.Rule, 0, len(in))
	for _, r := range in {
		out = append(out, store.Rule{Program: r.Program, Title: r.Title, Tag: r.Tag})
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	buf, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(bytes.TrimRight(buf, "\n"))
}
