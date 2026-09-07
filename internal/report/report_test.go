package report

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glocker/internal/store"
)

func openMem(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestBuildWindowsAndSummarizes(t *testing.T) {
	db := openMem(t)
	const uid = 1
	end := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return end.Add(d).UnixMilli() }

	// Two violations inside the 24h window, one well outside it.
	if err := db.IngestViolations(uid, []store.Violation{
		{TS: ms(-1 * time.Hour), RecordID: "v1", Keyword: "reddit", URL: "https://reddit.com/a", Type: "web_access", Domain: "reddit.com"},
		{TS: ms(-2 * time.Hour), RecordID: "v2", Keyword: "reddit", URL: "https://reddit.com/b", Type: "web_access", Domain: "reddit.com"},
		{TS: ms(-30 * time.Hour), RecordID: "v3", Keyword: "reddit", URL: "https://reddit.com/old", Type: "web_access", Domain: "reddit.com"},
	}); err != nil {
		t.Fatalf("ingest violations: %v", err)
	}

	restore := ms(-45 * time.Minute) // 15 min after the unblock
	if err := db.IngestUnblocks(uid, []store.Unblock{
		{TS: ms(-1 * time.Hour), RecordID: "u1", Domain: "news.com", RestoreTS: &restore, Reason: "reading"},
	}); err != nil {
		t.Fatalf("ingest unblocks: %v", err)
	}

	// Daemon down for 40 minutes inside the window (uninstall -> install pair).
	if err := db.IngestLifecycle(uid, []store.LifecycleEvent{
		{TS: ms(-3 * time.Hour), Type: "uninstall", Reason: "maintenance"},
		{TS: ms(-3*time.Hour + 40*time.Minute), Type: "install"},
	}); err != nil {
		t.Fatalf("ingest lifecycle: %v", err)
	}

	rep, err := Build(db, uid, end)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if rep.Violations != 2 {
		t.Errorf("Violations = %d, want 2 (old one should be windowed out)", rep.Violations)
	}
	if rep.ByKeyword["reddit"] != 2 {
		t.Errorf("ByKeyword[reddit] = %d, want 2", rep.ByKeyword["reddit"])
	}
	if rep.Unblocks != 1 || len(rep.UnblockLines) != 1 {
		t.Fatalf("Unblocks = %d / lines %d, want 1/1", rep.Unblocks, len(rep.UnblockLines))
	}
	if rep.UnblockLines[0].Minutes != 15 {
		t.Errorf("unblock minutes = %d, want 15", rep.UnblockLines[0].Minutes)
	}
	if rep.UnmanagedMinutes != 40 {
		t.Errorf("UnmanagedMinutes = %d, want 40", rep.UnmanagedMinutes)
	}
	if !rep.Attention {
		t.Error("Attention should be true (unmanaged > 30 min)")
	}

	text := rep.Text()
	for _, want := range []string{"Violations:     2", "reddit: 2", "news.com", "Unmanaged time: 40 minutes"} {
		if !strings.Contains(text, want) {
			t.Errorf("Text() missing %q\n---\n%s", want, text)
		}
	}
}

func TestBuildEmptyIsClean(t *testing.T) {
	db := openMem(t)
	end := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	rep, err := Build(db, 1, end)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rep.Violations != 0 || rep.Unblocks != 0 || rep.UnmanagedMinutes != 0 || rep.Attention {
		t.Errorf("empty report should be all-zero, got %+v", rep)
	}
	if rep.End != end || rep.Start != end.Add(-24*time.Hour) {
		t.Errorf("window = (%s, %s], want 24h ending at end", rep.Start, rep.End)
	}
}
