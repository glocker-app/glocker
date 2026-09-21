package accountability

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glocker/internal/store"
)

type sentMail struct{ to, subject, text, html string }

type fakeMailer struct{ sent []sentMail }

func (f *fakeMailer) Send(_ context.Context, to, subject, text, html string) error {
	f.sent = append(f.sent, sentMail{to, subject, text, html})
	return nil
}

func newDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(store.Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "n.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func fixedNotifier(db *store.DB, fm *fakeMailer, now time.Time) *Scheduler {
	return &Scheduler{db: db, mail: fm, now: func() time.Time { return now }}
}

func cfg(uid uint, mut func(*store.AccountabilityConfig)) store.AccountabilityConfig {
	c := store.AccountabilityConfig{
		UserID: uid, Enabled: true,
		BuddyEmails: store.EncodeBuddies([]string{"buddy@example.com"}),
	}
	if mut != nil {
		mut(&c)
	}
	return c
}

// The digest reports counts and never leaks the keyword/domain (content-free).
func TestDigestCountsAndContentFree(t *testing.T) {
	db := newDB(t)
	const uid = 1
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := db.IngestViolations(uid, 0, []store.Violation{
		{TS: now.Add(-time.Hour).UnixMilli(), RecordID: "r1", Keyword: "SECRETWORD", Domain: "secret.example"},
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.IngestUnblocks(uid, 0, []store.Unblock{{TS: now.Add(-2 * time.Hour).UnixMilli(), RecordID: "u1", Domain: "x.example", Reason: "work"}})
	if err := db.SaveAccountabilityConfig(uid, cfg(uid, func(c *store.AccountabilityConfig) { c.DigestEveryHours = 24 })); err != nil {
		t.Fatal(err)
	}

	fm := &fakeMailer{}
	fixedNotifier(db, fm, now).tick(now)

	if len(fm.sent) != 1 {
		t.Fatalf("want 1 email, got %d", len(fm.sent))
	}
	m := fm.sent[0]
	if m.to != "buddy@example.com" {
		t.Errorf("to = %q", m.to)
	}
	if !strings.Contains(m.text, "hits: 1") {
		t.Errorf("digest missing violation count:\n%s", m.text)
	}
	for _, leak := range []string{"SECRETWORD", "secret.example", "x.example"} {
		if strings.Contains(m.text, leak) || strings.Contains(m.html, leak) {
			t.Errorf("digest leaked content %q", leak)
		}
	}
	if c, _ := db.EnabledAccountabilityConfigs(); c[0].LastDigestAt == 0 {
		t.Error("LastDigestAt not stamped")
	}
}

// A quiet window sends nothing but still advances the cadence clock.
func TestDigestSkipsQuietButStamps(t *testing.T) {
	db := newDB(t)
	const uid = 1
	now := time.Now().UTC()
	_ = db.SaveAccountabilityConfig(uid, cfg(uid, func(c *store.AccountabilityConfig) { c.DigestEveryHours = 24 }))

	fm := &fakeMailer{}
	fixedNotifier(db, fm, now).tick(now)

	if len(fm.sent) != 0 {
		t.Fatalf("quiet window should send nothing, got %d", len(fm.sent))
	}
	if c, _ := db.EnabledAccountabilityConfigs(); c[0].LastDigestAt == 0 {
		t.Error("cadence should advance even on a quiet skip")
	}
}

// digest_when_quiet sends an all-clear even with no activity.
func TestDigestWhenQuietSends(t *testing.T) {
	db := newDB(t)
	const uid = 1
	now := time.Now().UTC()
	_ = db.SaveAccountabilityConfig(uid, cfg(uid, func(c *store.AccountabilityConfig) { c.DigestEveryHours = 24; c.DigestWhenQuiet = true }))

	fm := &fakeMailer{}
	fixedNotifier(db, fm, now).tick(now)
	if len(fm.sent) != 1 {
		t.Fatalf("digest_when_quiet should send an all-clear, got %d", len(fm.sent))
	}
}

// Crossing the high-violation threshold flags the subject.
func TestDigestHighViolationsFlag(t *testing.T) {
	db := newDB(t)
	const uid = 1
	now := time.Now().UTC()
	for i := 0; i < 25; i++ {
		_ = db.IngestViolations(uid, 0, []store.Violation{{TS: now.Add(-time.Duration(i) * time.Minute).UnixMilli(), RecordID: fmt.Sprintf("r%d", i), Keyword: "k"}})
	}
	_ = db.SaveAccountabilityConfig(uid, cfg(uid, func(c *store.AccountabilityConfig) { c.DigestEveryHours = 24; c.HighViolations = 20 }))

	fm := &fakeMailer{}
	fixedNotifier(db, fm, now).tick(now)
	if len(fm.sent) != 1 || !strings.Contains(fm.sent[0].subject, "high activity") {
		t.Fatalf("expected a high-activity subject, got %+v", fm.sent)
	}
}

// went_dark fires once per dark episode and re-arms after a fresh heartbeat.
func TestWentDarkOncePerEpisode(t *testing.T) {
	db := newDB(t)
	const uid = 1
	now := time.Now().UTC()
	_ = db.IngestHeartbeats(uid, 0, []store.Heartbeat{{TS: now.Add(-30 * time.Minute).UnixMilli(), Alive: true}})
	_ = db.SaveAccountabilityConfig(uid, cfg(uid, func(c *store.AccountabilityConfig) { c.WentDark = true }))

	fm := &fakeMailer{}
	n := fixedNotifier(db, fm, now)
	n.tick(now)
	if len(fm.sent) != 1 || !strings.Contains(fm.sent[0].subject, "dark") {
		t.Fatalf("want 1 went-dark alert, got %+v", fm.sent)
	}

	// Still dark, no new heartbeat → no duplicate.
	n.now = func() time.Time { return now.Add(time.Minute) }
	n.tick(now.Add(time.Minute))
	if len(fm.sent) != 1 {
		t.Fatalf("should not re-alert the same episode, got %d", len(fm.sent))
	}

	// Fresh heartbeat re-arms; going dark again alerts.
	beat := now.Add(2 * time.Minute)
	_ = db.IngestHeartbeats(uid, 0, []store.Heartbeat{{TS: beat.UnixMilli(), Alive: true}})
	later := beat.Add(30 * time.Minute)
	n.now = func() time.Time { return later }
	n.tick(later)
	if len(fm.sent) != 2 {
		t.Fatalf("a new dark episode should alert again, got %d", len(fm.sent))
	}
}

// Not-yet-due digests don't send.
func TestDigestNotDue(t *testing.T) {
	db := newDB(t)
	const uid = 1
	now := time.Now().UTC()
	c := cfg(uid, func(c *store.AccountabilityConfig) { c.DigestEveryHours = 24 })
	_ = db.SaveAccountabilityConfig(uid, c)
	_ = db.MarkDigestSent(uid, now.Add(-2*time.Hour).UnixMilli()) // last sent 2h ago, window 24h

	fm := &fakeMailer{}
	fixedNotifier(db, fm, now).tick(now)
	if len(fm.sent) != 0 {
		t.Fatalf("digest not due (2h < 24h) should send nothing, got %d", len(fm.sent))
	}
}
