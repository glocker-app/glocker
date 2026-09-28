package stats

import (
	"fmt"
	"net/http"
)

// handleHealthz is an unauthenticated liveness probe for the load balancer /
// uptime check: it pings the database and returns 200 with the build version, or
// 503 if the DB is unreachable. Unlike /api/health (session-gated, account-scoped)
// it touches no account data — no counts, no identity. It also reports the build
// version so `curl /healthz` confirms what's deployed; that's a git hash of a
// private repo, so it maps to nothing publicly (no CVE surface).
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	sqldb, err := db.DB.DB()
	if err == nil {
		err = sqldb.PingContext(r.Context())
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"ok":false,"version":%q,"error":"db unavailable"}`+"\n", buildVersion)
		return
	}
	fmt.Fprintf(w, `{"ok":true,"version":%q}`+"\n", buildVersion)
}
