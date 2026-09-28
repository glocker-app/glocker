package stats

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// startTime marks process start, for the uptime metric.
var startTime = time.Now()

// handleMetrics is an admin-only operational snapshot in Prometheus text format.
// The load test showed the managed DB is the ceiling and it queues before it
// errors, so the signal that matters is the connection pool: when in_use reaches
// max, requests wait (wait_count/wait_seconds climb). Also a live DB ping and
// build/uptime. Gated behind requireAdmin — for the operator, not a public scrape.
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "glockpeek_uptime_seconds %d\n", int64(time.Since(startTime).Seconds()))
	if buildVersion != "" {
		fmt.Fprintf(w, "glockpeek_build_info{version=%q} 1\n", buildVersion)
	}
	if db == nil {
		return
	}
	sqlDB, err := db.DB.DB()
	if err != nil {
		fmt.Fprintf(w, "glockpeek_db_up 0\n")
		return
	}
	s := sqlDB.Stats()
	fmt.Fprintf(w, "glockpeek_db_connections_open %d\n", s.OpenConnections)
	fmt.Fprintf(w, "glockpeek_db_connections_in_use %d\n", s.InUse)
	fmt.Fprintf(w, "glockpeek_db_connections_idle %d\n", s.Idle)
	fmt.Fprintf(w, "glockpeek_db_connections_max %d\n", s.MaxOpenConnections)
	fmt.Fprintf(w, "glockpeek_db_wait_count_total %d\n", s.WaitCount)
	fmt.Fprintf(w, "glockpeek_db_wait_seconds_total %f\n", s.WaitDuration.Seconds())

	t0 := time.Now()
	perr := sqlDB.PingContext(r.Context())
	up := 1
	if perr != nil {
		up = 0
	}
	fmt.Fprintf(w, "glockpeek_db_up %d\n", up)
	fmt.Fprintf(w, "glockpeek_db_ping_seconds %f\n", time.Since(t0).Seconds())
}

// adminMetricsJSON is the same operational snapshot as handleMetrics, but as JSON
// for the admin dashboard panel. The text form (handleMetrics) stays for scrapers.
type adminMetricsJSON struct {
	UptimeSeconds int64  `json:"uptimeSeconds"`
	Version       string `json:"version"`
	DB            struct {
		Up          bool    `json:"up"`
		PingSeconds float64 `json:"pingSeconds"`
		Open        int     `json:"open"`
		InUse       int     `json:"inUse"`
		Idle        int     `json:"idle"`
		Max         int     `json:"max"`
		WaitCount   int64   `json:"waitCount"`
		WaitSeconds float64 `json:"waitSeconds"`
	} `json:"db"`
}

// handleAdminMetricsJSON renders the same snapshot as /metrics in JSON, for the
// /admin panel. Admin-gated (mounted behind requireAdmin).
func handleAdminMetricsJSON(w http.ResponseWriter, r *http.Request) {
	var m adminMetricsJSON
	m.UptimeSeconds = int64(time.Since(startTime).Seconds())
	m.Version = buildVersion
	if db != nil {
		if sqlDB, err := db.DB.DB(); err == nil {
			s := sqlDB.Stats()
			m.DB.Open = s.OpenConnections
			m.DB.InUse = s.InUse
			m.DB.Idle = s.Idle
			m.DB.Max = s.MaxOpenConnections
			m.DB.WaitCount = s.WaitCount
			m.DB.WaitSeconds = s.WaitDuration.Seconds()
			t0 := time.Now()
			m.DB.Up = sqlDB.PingContext(r.Context()) == nil
			m.DB.PingSeconds = time.Since(t0).Seconds()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(m)
}
