package store

import "testing"

// A batch with duplicate conflict keys must not error on upsert (Postgres rejects
// affecting a row twice) and must collapse to the last value.
func TestIngestDedupsDuplicateKeys(t *testing.T) {
	db := openMem(t)
	const uid = 1

	vios := []Violation{
		{TS: 7, RecordID: "dup", Keyword: "a", Domain: "first"},
		{TS: 7, RecordID: "dup", Keyword: "a", Domain: "second"},
	}
	if err := db.IngestViolations(uid, 0, vios); err != nil {
		t.Fatalf("duplicate record_id batch errored: %v", err)
	}
	if got, _ := db.Violations(uid); len(got) != 1 || got[0].Domain != "second" {
		t.Fatalf("violation dedup: want 1 row = second, got %+v", got)
	}

	usage := []UsageSample{
		{TS: 9, ActiveClass: "kitty"},
		{TS: 9, ActiveClass: "firefox"},
	}
	if err := db.IngestUsage(uid, 0, usage); err != nil {
		t.Fatalf("duplicate ts usage batch errored: %v", err)
	}
	if us, _ := db.UsageSamples(uid); len(us) != 1 || us[0].ActiveClass != "firefox" {
		t.Fatalf("usage dedup: want 1 row = firefox, got %+v", us)
	}
}
