package stats

import (
	"sort"
	"time"

	"glocker/internal/reports"
	"glocker/internal/store"
)

// minUnmanagedDuration matches cmd/glockpeek and lib/parse.js: shorter
// uninstall→install gaps are upgrades, not real exposure.
const minUnmanagedDuration = 2 * time.Minute

type violationJSON struct {
	TS       int64  `json:"ts"`
	DeviceID uint   `json:"deviceId"` // which device this row came from (0 = local/legacy)
	RecordID string `json:"recordId"` // opaque, content-blind id — the ignore overlay's key
	Type     string `json:"type"`
	Keyword  string `json:"keyword"`
	URL      string `json:"url"`
	Domain   string `json:"domain"`
}

type unblockJSON struct {
	TS        int64  `json:"ts"`
	DeviceID  uint   `json:"deviceId"`
	RestoreTS *int64 `json:"restoreTs"`
	Reason    string `json:"reason"`
	Domain    string `json:"domain"`
}

type lifecycleJSON struct {
	TS       int64  `json:"ts"`
	DeviceID uint   `json:"deviceId"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
	Note     string `json:"note"`
}

type unmanagedJSON struct {
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Open     bool   `json:"open"`
	Reason   string `json:"reason"`
	Note     string `json:"note"`
	DeviceID uint   `json:"deviceId"` // which device was unmanaged (0 = local/legacy)
}

// downtimeJSON is a span the glockdoc watchdog observed glocker to be down.
// Unlike unmanaged (derived from clean uninstall/install events), this reflects
// real observed liveness, so it also catches crashes and unclean stops.
type downtimeJSON struct {
	Start    int64 `json:"start"`
	End      int64 `json:"end"`
	Open     bool  `json:"open"`
	DeviceID uint  `json:"deviceId"`
}

type sourcesJSON struct {
	Reports   bool `json:"reports"`
	Unblocks  bool `json:"unblocks"`
	Lifecycle bool `json:"lifecycle"`
	Usage     bool `json:"usage"`
	Heartbeat bool `json:"heartbeat"`
}

type dataResponse struct {
	Now        int64           `json:"now"`
	Sources    sourcesJSON     `json:"sources"`
	Violations []violationJSON `json:"violations"`
	Unblocks   []unblockJSON   `json:"unblocks"`
	Lifecycle  []lifecycleJSON `json:"lifecycle"`
	Unmanaged  []unmanagedJSON `json:"unmanaged"`
	Downtime   []downtimeJSON  `json:"downtime"`
	Usage      []usageSample   `json:"usage"`
}

// buildData assembles the full history the dashboard renders, reading from the
// glockpeek DB (populated by the glocker syncer). Times are epoch milliseconds
// and every slice is sorted ascending. Sources reflect whether each table has
// any rows.
func buildData(db *store.DB, userID uint, now time.Time) dataResponse {
	resp := dataResponse{
		Now:        now.UnixMilli(),
		Violations: []violationJSON{},
		Unblocks:   []unblockJSON{},
		Lifecycle:  []lifecycleJSON{},
		Unmanaged:  []unmanagedJSON{},
		Downtime:   []downtimeJSON{},
		Usage:      []usageSample{},
	}
	if db == nil {
		return resp
	}

	// All violations, unfiltered, with the domain exactly as stored. The
	// false-positive (ignored) overlay and the domain-from-URL fallback are
	// applied in the browser now — so this endpoint never reads violation
	// content. That's the prerequisite for serving ciphertext blobs here under
	// E2E (see docs/e2e-design.md): the server stays content-blind.
	if rows, err := db.Violations(userID); err == nil {
		resp.Sources.Reports = len(rows) > 0
		for _, e := range rows {
			resp.Violations = append(resp.Violations, violationJSON{
				TS: e.TS, DeviceID: e.DeviceID, RecordID: e.RecordID, Type: e.Type, Keyword: e.Keyword, URL: e.URL, Domain: e.Domain,
			})
		}
	}

	if rows, err := db.Unblocks(userID); err == nil {
		resp.Sources.Unblocks = len(rows) > 0
		for _, e := range rows {
			resp.Unblocks = append(resp.Unblocks, unblockJSON{
				TS: e.TS, DeviceID: e.DeviceID, RestoreTS: e.RestoreTS, Reason: e.Reason, Domain: e.Domain,
			})
		}
	}

	// Unmanaged is computed PER DEVICE — each device's own install/uninstall
	// timeline. Merging events across devices would interleave incorrectly (one
	// device's uninstall against another's install). The browser unions them for
	// the Combined view (ANY: the account is exposed if any device was off) and
	// filters to one device otherwise.
	lcByDev := map[uint][]reports.LifecycleEntry{}
	if rows, err := db.LifecycleEvents(userID); err == nil {
		resp.Sources.Lifecycle = len(rows) > 0
		for _, e := range rows {
			resp.Lifecycle = append(resp.Lifecycle, lifecycleJSON{
				TS: e.TS, DeviceID: e.DeviceID, Type: e.Type, Reason: e.Reason, Note: e.Note,
			})
			lcByDev[e.DeviceID] = append(lcByDev[e.DeviceID], reports.LifecycleEntry{
				Timestamp: time.UnixMilli(e.TS), Type: e.Type, Reason: e.Reason, Note: e.Note,
			})
		}
	}
	for dev, entries := range lcByDev {
		for _, p := range unmanagedPeriods(entries, now) {
			p.DeviceID = dev
			resp.Unmanaged = append(resp.Unmanaged, p)
		}
	}

	// Downtime (observed liveness) is likewise per device.
	if rows, err := db.Heartbeats(userID); err == nil {
		resp.Sources.Heartbeat = len(rows) > 0
		hbByDev := map[uint][]reports.HeartbeatSample{}
		for _, h := range rows {
			hbByDev[h.DeviceID] = append(hbByDev[h.DeviceID], reports.HeartbeatSample{Timestamp: time.UnixMilli(h.TS), Alive: h.Alive})
		}
		for dev, samples := range hbByDev {
			for _, d := range reports.DowntimePeriods(samples, now, minUnmanagedDuration) {
				resp.Downtime = append(resp.Downtime, downtimeJSON{
					Start: d.Start.UnixMilli(), End: d.End.UnixMilli(), Open: d.Open, DeviceID: dev,
				})
			}
		}
	}

	if rows, err := db.UsageSamples(userID); err == nil {
		resp.Sources.Usage = len(rows) > 0
		for _, s := range rows {
			us := usageSample{TS: s.TS, DeviceID: s.DeviceID, IdleMS: s.IdleMS, WindowCount: s.WindowCount}
			if s.ActiveClass != "" || s.ActiveInstance != "" || s.ActiveTitle != "" {
				us.Active = &usageActive{Class: s.ActiveClass, Instance: s.ActiveInstance, Title: s.ActiveTitle}
			}
			resp.Usage = append(resp.Usage, us)
		}
	}

	// Rows come back TS-ascending from the store, but keep the guarantee explicit.
	sort.Slice(resp.Violations, func(i, j int) bool { return resp.Violations[i].TS < resp.Violations[j].TS })
	sort.Slice(resp.Unblocks, func(i, j int) bool { return resp.Unblocks[i].TS < resp.Unblocks[j].TS })
	sort.Slice(resp.Lifecycle, func(i, j int) bool { return resp.Lifecycle[i].TS < resp.Lifecycle[j].TS })
	sort.Slice(resp.Usage, func(i, j int) bool { return resp.Usage[i].TS < resp.Usage[j].TS })
	// Per-device grouping above used maps (non-deterministic order); sort by start.
	sort.Slice(resp.Unmanaged, func(i, j int) bool { return resp.Unmanaged[i].Start < resp.Unmanaged[j].Start })
	sort.Slice(resp.Downtime, func(i, j int) bool { return resp.Downtime[i].Start < resp.Downtime[j].Start })

	return resp
}

// unmanagedPeriods pairs each uninstall with the next install; an open span
// (uninstall with no following install) ends at now. Mirrors getUnmanagedPeriods
// in cmd/glockpeek/main.go and unmanagedPeriods in lib/parse.js.
func unmanagedPeriods(entries []reports.LifecycleEntry, now time.Time) []unmanagedJSON {
	out := []unmanagedJSON{}
	var open *reports.LifecycleEntry
	for i := range entries {
		e := entries[i]
		switch {
		case e.Type == "uninstall":
			open = &entries[i]
		case e.Type == "install" && open != nil:
			if e.Timestamp.Sub(open.Timestamp) >= minUnmanagedDuration {
				out = append(out, unmanagedJSON{
					Start: open.Timestamp.UnixMilli(), End: e.Timestamp.UnixMilli(),
					Open: false, Reason: open.Reason, Note: open.Note,
				})
			}
			open = nil
		}
	}
	if open != nil {
		out = append(out, unmanagedJSON{
			Start: open.Timestamp.UnixMilli(), End: now.UnixMilli(),
			Open: true, Reason: open.Reason, Note: open.Note,
		})
	}
	return out
}
