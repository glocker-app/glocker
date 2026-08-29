// Package report builds a rolling 24-hour activity summary for one glockpeek
// account from the stored (synced) records. It is the server-side successor to
// the agent's old daily-report email: same shape of summary, but sourced from
// the database instead of /var log files.
//
// Nothing calls Build yet — the per-user notification/digest layer that will
// schedule and deliver these reports is not built. See ROADMAP ("glockpeek
// notification layer"). This package exists so that work has a ready builder.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"glocker/internal/store"
)

// Report is the summary for one account over the window (End-24h, End].
type Report struct {
	Start            time.Time
	End              time.Time
	Violations       int
	Unblocks         int
	UnmanagedMinutes int
	// ByKeyword counts violations grouped by their keyword/label.
	ByKeyword map[string]int
	// UnblockLines and LifecycleLines are the per-event detail rows.
	UnblockLines   []UnblockLine
	LifecycleLines []LifecycleLine
	// Attention flags a heavy day (many violations or a long unmanaged gap).
	Attention bool
}

// UnblockLine is one temporary-unblock (or program-extension) detail row.
type UnblockLine struct {
	At      time.Time
	Domain  string
	Minutes int // 0 if the unblock was still open at report time
	Reason  string
}

// LifecycleLine is one install/uninstall detail row.
type LifecycleLine struct {
	At     time.Time
	Type   string
	Reason string
}

// Build assembles the report for userID over the 24 hours ending at end, i.e.
// the window (end-24h, end]. It reads only stored records, so it works purely
// from what the syncer has ingested.
func Build(db *store.DB, userID uint, end time.Time) (Report, error) {
	start := end.Add(-24 * time.Hour)
	startMS, endMS := start.UnixMilli(), end.UnixMilli()
	inWindow := func(ts int64) bool { return ts >= startMS && ts <= endMS }

	rep := Report{Start: start, End: end, ByKeyword: map[string]int{}}

	violations, err := db.Violations(userID)
	if err != nil {
		return Report{}, fmt.Errorf("report: violations: %w", err)
	}
	for _, v := range violations {
		if !inWindow(v.TS) {
			continue
		}
		rep.Violations++
		rep.ByKeyword[v.Keyword]++
	}

	unblocks, err := db.Unblocks(userID)
	if err != nil {
		return Report{}, fmt.Errorf("report: unblocks: %w", err)
	}
	for _, u := range unblocks {
		if !inWindow(u.TS) {
			continue
		}
		rep.Unblocks++
		line := UnblockLine{
			At:     time.UnixMilli(u.TS),
			Domain: u.Domain,
			Reason: u.Reason,
		}
		if u.RestoreTS != nil {
			line.Minutes = int(time.UnixMilli(*u.RestoreTS).Sub(line.At).Minutes())
		}
		rep.UnblockLines = append(rep.UnblockLines, line)
	}

	lifecycle, err := db.LifecycleEvents(userID)
	if err != nil {
		return Report{}, fmt.Errorf("report: lifecycle: %w", err)
	}
	for _, e := range lifecycle {
		if !inWindow(e.TS) {
			continue
		}
		rep.LifecycleLines = append(rep.LifecycleLines, LifecycleLine{
			At:     time.UnixMilli(e.TS),
			Type:   e.Type,
			Reason: e.Reason,
		})
	}

	// Unmanaged minutes: pair each uninstall with the next install (the daemon
	// was down in between) and clamp the overlap to the report window. Pairs
	// that straddle the window boundary still count their in-window portion, so
	// the full lifecycle history is scanned, not just the windowed slice.
	rep.UnmanagedMinutes = unmanagedMinutes(lifecycle, start, end)

	rep.Attention = rep.Violations > 10 || rep.UnmanagedMinutes > 30
	return rep, nil
}

// unmanagedMinutes sums the daemon-down time overlapping [start, end]. lifecycle
// must be TS-ascending (the store returns it that way). Uninstall/install pairs
// shorter than two minutes are treated as upgrades and skipped; an unclosed
// uninstall counts as down through end.
func unmanagedMinutes(lifecycle []store.LifecycleEvent, start, end time.Time) int {
	total := 0
	var down *time.Time
	clampAdd := func(s, e time.Time) {
		if s.Before(start) {
			s = start
		}
		if e.After(end) {
			e = end
		}
		if s.Before(e) {
			total += int(e.Sub(s).Minutes())
		}
	}
	for _, ev := range lifecycle {
		switch ev.Type {
		case "uninstall":
			t := time.UnixMilli(ev.TS)
			down = &t
		case "install":
			if down == nil {
				continue
			}
			up := time.UnixMilli(ev.TS)
			if up.Sub(*down) >= 2*time.Minute {
				clampAdd(*down, up)
			}
			down = nil
		}
	}
	if down != nil {
		clampAdd(*down, end)
	}
	return total
}

// Text renders the report as a plain-text summary (the body the notification
// layer would send). Kept deterministic: keyword counts are emitted in
// descending-count, then alphabetical, order.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Glocker activity, %s to %s\n",
		r.Start.Format("Jan 2 15:04"), r.End.Format("Jan 2 15:04"))
	b.WriteString(strings.Repeat("=", 51) + "\n\n")

	b.WriteString("SUMMARY\n")
	b.WriteString(strings.Repeat("-", 30) + "\n")
	fmt.Fprintf(&b, "Violations:     %d\n", r.Violations)
	fmt.Fprintf(&b, "Unblocks:       %d\n", r.Unblocks)
	if r.UnmanagedMinutes > 0 {
		fmt.Fprintf(&b, "Unmanaged time: %d minutes\n", r.UnmanagedMinutes)
	}
	b.WriteString("\n")

	if len(r.ByKeyword) > 0 {
		b.WriteString("VIOLATIONS\n")
		b.WriteString(strings.Repeat("-", 30) + "\n")
		type kv struct {
			k string
			n int
		}
		rows := make([]kv, 0, len(r.ByKeyword))
		for k, n := range r.ByKeyword {
			rows = append(rows, kv{k, n})
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].n != rows[j].n {
				return rows[i].n > rows[j].n
			}
			return rows[i].k < rows[j].k
		})
		for _, row := range rows {
			fmt.Fprintf(&b, "  %s: %d\n", row.k, row.n)
		}
		b.WriteString("\n")
	}

	if len(r.UnblockLines) > 0 {
		b.WriteString("UNBLOCKS\n")
		b.WriteString(strings.Repeat("-", 30) + "\n")
		for _, u := range r.UnblockLines {
			fmt.Fprintf(&b, "  %s - %s (%d min) - %q\n",
				u.At.Format("15:04"), u.Domain, u.Minutes, u.Reason)
		}
		b.WriteString("\n")
	}

	if len(r.LifecycleLines) > 0 {
		b.WriteString("LIFECYCLE EVENTS\n")
		b.WriteString(strings.Repeat("-", 30) + "\n")
		for _, e := range r.LifecycleLines {
			if e.Reason != "" {
				fmt.Fprintf(&b, "  %s - %s (%s)\n", e.At.Format("15:04"), e.Type, e.Reason)
			} else {
				fmt.Fprintf(&b, "  %s - %s\n", e.At.Format("15:04"), e.Type)
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}
