package stats

import (
	"net"
	"net/http"
	"strings"
	"time"
)

// Per-IP rate limiting for the open, unauthenticated endpoints (register, login) —
// it guards against bulk signups that would flood real inboxes and burn the mail
// quota, and against login brute force. The counters live in the DB (shared across
// the fleet), so N app nodes enforce ONE limit rather than N× it.

// clientIP is the best-effort client address used as the rate-limit key. Behind a
// trusted reverse proxy (Caddy / a DO load balancer), X-Forwarded-For is a list
// "client, proxy1, proxy2, …" where each hop APPENDS the address it saw. A client
// can PREPEND fake entries, but it can't control what our trusted upstream appends
// last — so take the RIGHT-most entry (the address our proxy actually observed),
// not the spoofable left-most. Assumes a single trusted proxy hop; if a second is
// added (an LB in front of Caddy), trust N-from-the-right instead. Falls back to
// the transport peer when there's no proxy header.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// rateLimited wraps h with a shared fixed-window limit of `limit` requests per
// `window` per client IP, keyed under `name`. Over the limit → 429 + Retry-After.
// The counter is DB-backed and fail-open (a DB blip never locks out real users).
func rateLimited(name string, limit int, window time.Duration, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, _ := db.RateAllow(name+":"+clientIP(r), window, limit, time.Now())
		if !ok {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests; please wait and try again", http.StatusTooManyRequests)
			return
		}
		h(w, r)
	}
}
