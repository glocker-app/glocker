package stats

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		xff        string
		remoteAddr string
		want       string
	}{
		{"xff single", "203.0.113.9", "127.0.0.1:5000", "203.0.113.9"},
		// The trusted proxy appends what it saw LAST — take the right-most, not the
		// client-spoofable left-most.
		{"xff chain uses right-most", "evil-spoof, 10.0.0.1", "127.0.0.1:5000", "10.0.0.1"},
		{"no xff falls back to peer", "", "198.51.100.7:44321", "198.51.100.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/register", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := clientIP(r); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRateLimited_Returns429OverLimit exercises the DB-backed limiter: within one
// window, requests beyond the limit get 429 and the handler doesn't run.
func TestRateLimited_Returns429OverLimit(t *testing.T) {
	newMux(t) // sets the package db the limiter reads/writes
	var served int
	h := rateLimited("test", 1, time.Hour, func(w http.ResponseWriter, r *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	})
	call := func() int {
		r := httptest.NewRequest(http.MethodPost, "/x", nil)
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	if code := call(); code != http.StatusOK {
		t.Fatalf("first call: got %d, want 200", code)
	}
	if code := call(); code != http.StatusTooManyRequests {
		t.Fatalf("second call: got %d, want 429", code)
	}
	if served != 1 {
		t.Errorf("handler should have run once, ran %d times", served)
	}
}

// TestRateLimited_PerIPIndependent: a different IP has its own shared counter.
func TestRateLimited_PerIPIndependent(t *testing.T) {
	newMux(t)
	h := rateLimited("test2", 1, time.Hour, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	call := func(ip string) int {
		r := httptest.NewRequest(http.MethodPost, "/x", nil)
		r.Header.Set("X-Forwarded-For", ip)
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	if code := call("1.1.1.1"); code != http.StatusOK {
		t.Fatalf("ip1 first: %d", code)
	}
	if code := call("1.1.1.1"); code != http.StatusTooManyRequests {
		t.Fatalf("ip1 second (over limit): %d", code)
	}
	if code := call("2.2.2.2"); code != http.StatusOK {
		t.Fatalf("ip2 must have its own counter: %d", code)
	}
}

// TestLoginEndpointRateLimited guards that /api/login is wired through a limiter:
// repeated attempts from one IP start returning 429 once the limit is spent.
func TestLoginEndpointRateLimited(t *testing.T) {
	mux, _ := newMux(t)
	got429 := false
	for i := 0; i < 20; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/login",
			strings.NewReader(`{"username":"nobody","password":"wrong"}`))
		r.Header.Set("X-Forwarded-For", "203.0.113.50")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("/api/login should return 429 after the limit is spent")
	}
}
