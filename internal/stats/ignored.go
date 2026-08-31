package stats

import (
	"fmt"

	"glocker/internal/store"
)

// IgnoredEntry marks a single recorded violation as a false positive so the
// dashboard stops counting it. Matching is by ts + keyword + url (the fields
// that uniquely identify one report line); domain is kept only for display.
// The raw reports log is never touched — this is a separate, reversible overlay.
type IgnoredEntry struct {
	TS      int64  `json:"ts"` // event time, epoch milliseconds
	Keyword string `json:"keyword"`
	URL     string `json:"url"`
	Domain  string `json:"domain,omitempty"`
}

// ignoreKey is the identity used to match a violation against the ignore list.
func ignoreKey(ts int64, keyword, url string) string {
	return fmt.Sprintf("%d\x00%s\x00%s", ts, keyword, url)
}

// loadIgnored reads the account's ignore list from the DB.
func loadIgnored(db *store.DB, userID uint) ([]IgnoredEntry, error) {
	rows, err := db.IgnoredViolations(userID)
	if err != nil {
		return []IgnoredEntry{}, err
	}
	out := make([]IgnoredEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, IgnoredEntry{TS: r.TS, Keyword: r.Keyword, URL: r.URL, Domain: r.Domain})
	}
	return out, nil
}

// dedupeIgnored drops entries with no timestamp and collapses duplicates by key,
// preserving the stored field values exactly so matching stays reliable.
func dedupeIgnored(in []IgnoredEntry) []IgnoredEntry {
	out := []IgnoredEntry{}
	seen := map[string]bool{}
	for _, e := range in {
		if e.TS == 0 {
			continue
		}
		k := ignoreKey(e.TS, e.Keyword, e.URL)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// saveIgnored replaces the account's ignore list in the DB and returns the
// cleaned set.
func saveIgnored(in []IgnoredEntry, db *store.DB, userID uint) ([]IgnoredEntry, error) {
	clean := dedupeIgnored(in)
	rows := make([]store.IgnoredViolation, 0, len(clean))
	for _, e := range clean {
		rows = append(rows, store.IgnoredViolation{TS: e.TS, Keyword: e.Keyword, URL: e.URL, Domain: e.Domain})
	}
	if err := db.SetIgnored(userID, rows); err != nil {
		return clean, err
	}
	return clean, nil
}
