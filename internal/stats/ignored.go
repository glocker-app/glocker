package stats

import "glocker/internal/store"

// IgnoredEntry marks a recorded violation as a false positive so the dashboard
// stops counting it. Identity is the opaque RecordID — the same content-blind
// fingerprint the store dedups on — so the ignore overlay never carries plaintext
// keyword/url to the server. TS is kept for display/sorting only. The raw reports
// log is never touched; this is a separate, reversible overlay.
type IgnoredEntry struct {
	RecordID string `json:"recordId"`
	TS       int64  `json:"ts,omitempty"` // event time (ms), display only
}

// loadIgnored reads the account's ignore list from the DB.
func loadIgnored(db *store.DB, userID uint) ([]IgnoredEntry, error) {
	rows, err := db.IgnoredViolations(userID)
	if err != nil {
		return []IgnoredEntry{}, err
	}
	out := make([]IgnoredEntry, 0, len(rows))
	for _, r := range rows {
		if r.RecordID == "" {
			continue // legacy pre-recordId entries: they match nothing, so drop them
		}
		out = append(out, IgnoredEntry{RecordID: r.RecordID, TS: r.TS})
	}
	return out, nil
}

// dedupeIgnored drops entries with no RecordID and collapses duplicates.
func dedupeIgnored(in []IgnoredEntry) []IgnoredEntry {
	out := []IgnoredEntry{}
	seen := map[string]bool{}
	for _, e := range in {
		if e.RecordID == "" || seen[e.RecordID] {
			continue
		}
		seen[e.RecordID] = true
		out = append(out, e)
	}
	return out
}

// saveIgnored replaces the account's ignore list in the DB and returns the
// cleaned set. Only the opaque RecordID (+ ts for display) is persisted.
func saveIgnored(in []IgnoredEntry, db *store.DB, userID uint) ([]IgnoredEntry, error) {
	clean := dedupeIgnored(in)
	rows := make([]store.IgnoredViolation, 0, len(clean))
	for _, e := range clean {
		rows = append(rows, store.IgnoredViolation{RecordID: e.RecordID, TS: e.TS})
	}
	if err := db.SetIgnored(userID, rows); err != nil {
		return clean, err
	}
	return clean, nil
}
