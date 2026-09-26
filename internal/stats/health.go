package stats

import "net/http"

// handleHealthz is an unauthenticated liveness probe for a load balancer: it
// pings the database and returns 200 "ok" or 503. Unlike /api/health (which is
// session-gated and account-scoped), this touches no account data — it only
// reports whether this node can serve. Nothing here leaks: no counts, no identity.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	sqldb, err := db.DB.DB()
	if err == nil {
		err = sqldb.PingContext(r.Context())
	}
	if err != nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}
