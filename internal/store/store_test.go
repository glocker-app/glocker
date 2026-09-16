package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openMem opens a fresh temp-file sqlite store for a test.
func openMem(t *testing.T) *DB {
	t.Helper()
	db, err := Open(Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenMigrateAllTables(t *testing.T) {
	db := openMem(t)
	for _, m := range AllModels() {
		if !db.Migrator().HasTable(m) {
			t.Errorf("missing table for %T", m)
		}
	}
}

func TestDefaultsAndBadDriver(t *testing.T) {
	if _, err := Open(Options{Driver: "sqlite"}); err == nil {
		t.Error("empty DSN should error")
	}
	if _, err := Open(Options{Driver: "mysql", DSN: "x"}); err == nil {
		t.Error("unsupported driver should error")
	}
}

func TestIngestIdempotent(t *testing.T) {
	db := openMem(t)
	const uid = 1
	v := []Violation{{TS: 1000, Keyword: "porn", URL: "https://x/?q=porn", Type: "url-keyword", Domain: "x"}}
	if err := db.IngestViolations(uid, 0, v); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// Re-ingesting the same (user,ts,keyword,url) must not duplicate.
	if err := db.IngestViolations(uid, 0, []Violation{{TS: 1000, Keyword: "porn", URL: "https://x/?q=porn"}}); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	got, _ := db.Violations(uid)
	if len(got) != 1 {
		t.Fatalf("want 1 violation after dup ingest, got %d", len(got))
	}

	// Usage + heartbeat unique on (user, ts).
	if err := db.IngestUsage(uid, 0, []UsageSample{{TS: 5, ActiveClass: "kitty"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.IngestUsage(uid, 0, []UsageSample{{TS: 5, ActiveClass: "firefox"}}); err != nil {
		t.Fatal(err)
	}
	us, _ := db.UsageSamples(uid)
	if len(us) != 1 {
		t.Errorf("want 1 usage sample, got %d", len(us))
	}
}

// TestTenantIsolation is the crux of the multi-tenant model: one account never
// sees another's rows, even with identical natural keys.
func TestTenantIsolation(t *testing.T) {
	db := openMem(t)
	const alice, bob = 1, 2

	same := Violation{TS: 1000, Keyword: "porn", URL: "https://x/?q=porn", Domain: "x"}
	if err := db.IngestViolations(alice, 0, []Violation{same}); err != nil {
		t.Fatal(err)
	}
	// Bob ingests the identical record — must NOT collide with Alice's (the
	// unique key is per-user), and must land in Bob's data only.
	if err := db.IngestViolations(bob, 0, []Violation{same}); err != nil {
		t.Fatalf("bob ingest identical record: %v", err)
	}

	av, _ := db.Violations(alice)
	bv, _ := db.Violations(bob)
	if len(av) != 1 || len(bv) != 1 {
		t.Fatalf("each account should see exactly its own: alice=%d bob=%d", len(av), len(bv))
	}

	// Bob's ignore overlay must not affect Alice.
	if err := db.SetIgnored(bob, []IgnoredViolation{{TS: 1000, Keyword: "porn", URL: "https://x/?q=porn"}}); err != nil {
		t.Fatal(err)
	}
	ai, _ := db.IgnoredViolations(alice)
	bi, _ := db.IgnoredViolations(bob)
	if len(ai) != 0 || len(bi) != 1 {
		t.Errorf("ignore overlay leaked across tenants: alice=%d bob=%d", len(ai), len(bi))
	}

	// Rules are per-account too.
	if err := db.SetRulesConfig(alice, []Rule{{Program: "Emacs", Tag: "work"}}, map[string]string{"work": "#4c9f70"}); err != nil {
		t.Fatal(err)
	}
	ar, _ := db.Rules(alice)
	br, _ := db.Rules(bob)
	if len(ar) != 1 || len(br) != 0 {
		t.Errorf("rules leaked across tenants: alice=%d bob=%d", len(ar), len(br))
	}
}

// TestEnsureDefaultUserReusesPreEmailRow guards the migration path: an existing
// database has a `local` user created before the email column existed (so its
// email is NULL). EnsureDefaultUser must find and reuse it by username, not
// create a duplicate.
func TestEnsureDefaultUserReusesPreEmailRow(t *testing.T) {
	db := openMem(t)
	// Simulate a pre-email row: username set, email NULL.
	if err := db.Exec("INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)",
		DefaultUsername, "x", time.Now()).Error; err != nil {
		t.Fatalf("seed old row: %v", err)
	}
	u1, err := db.EnsureDefaultUser()
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	u2, _ := db.EnsureDefaultUser()
	if u1.ID != u2.ID {
		t.Errorf("EnsureDefaultUser returned different ids (%d vs %d) — duplicated", u1.ID, u2.ID)
	}
	var n int64
	db.Model(&User{}).Where("username = ?", DefaultUsername).Count(&n)
	if n != 1 {
		t.Errorf("want exactly 1 local user, got %d", n)
	}
}

func TestUsersSessionsTokens(t *testing.T) {
	db := openMem(t)

	u, err := db.CreateUser("alice@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Good + bad password (email is case-insensitive).
	if _, err := db.Authenticate("Alice@Example.com", "correct horse battery staple"); err != nil {
		t.Errorf("valid login rejected: %v", err)
	}
	if _, err := db.Authenticate("alice@example.com", "wrong"); err != ErrInvalidCredentials {
		t.Errorf("bad password: want ErrInvalidCredentials, got %v", err)
	}
	if _, err := db.Authenticate("ghost@example.com", "x"); err != ErrInvalidCredentials {
		t.Errorf("unknown user: want ErrInvalidCredentials, got %v", err)
	}

	// Session round trip + logout.
	tok, err := db.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.UserBySession(tok); err != nil || got.ID != u.ID {
		t.Errorf("session lookup: %v (user %+v)", err, got)
	}
	if err := db.DeleteSession(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UserBySession(tok); err != ErrInvalidCredentials {
		t.Errorf("deleted session should be invalid, got %v", err)
	}

	// API token round trip; wrong token rejected.
	api, err := db.CreateAPIToken(u.ID, "syncer")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.UserByAPIToken(api); err != nil || got.ID != u.ID {
		t.Errorf("api token lookup: %v (user %+v)", err, got)
	}
	if _, err := db.UserByAPIToken("nope"); err != ErrInvalidCredentials {
		t.Errorf("bad api token: want ErrInvalidCredentials, got %v", err)
	}
}

// TestIngestViolationsRecordID verifies dedup is keyed on RecordID (the
// agent-computed fingerprint), not the content columns: the same RecordID no-ops,
// distinct RecordIDs are distinct rows, a very long URL still ingests, and —
// crucially for E2E — the same RecordID de-dups even when every content column
// arrives as different bytes (fresh ciphertext on each sync).
func TestIngestViolationsRecordID(t *testing.T) {
	db := openMem(t)
	const uid = uint(1)

	huge := "https://www.google.com/search?q=x&sca_esv=" + strings.Repeat("a", 5000)
	v := Violation{TS: 1000, RecordID: "rid-a", Keyword: "porn", URL: huge, Type: "url-keyword"}

	if err := db.IngestViolations(uid, 0, []Violation{v}); err != nil {
		t.Fatalf("ingest long url: %v", err)
	}
	// Re-sending the identical row is idempotent.
	if err := db.IngestViolations(uid, 0, []Violation{v}); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	got, _ := db.Violations(uid)
	if len(got) != 1 {
		t.Fatalf("idempotent re-ingest should keep 1 row, got %d", len(got))
	}
	if got[0].URLHash != hashURL(huge) || got[0].URL != huge {
		t.Errorf("stored row lost url/hash: hash=%q urlLen=%d", got[0].URLHash, len(got[0].URL))
	}

	// A different RecordID (distinct content) is a distinct violation, even at the
	// same (user, ts).
	v2 := Violation{TS: 1000, RecordID: "rid-b", Keyword: "porn", URL: "https://other.example/z", Type: "url-keyword"}
	if err := db.IngestViolations(uid, 0, []Violation{v2}); err != nil {
		t.Fatalf("ingest distinct url: %v", err)
	}
	if got, _ := db.Violations(uid); len(got) != 2 {
		t.Errorf("distinct RecordIDs should yield 2 rows, got %d", len(got))
	}

	// The E2E guarantee: the same RecordID re-sent with entirely different content
	// bytes (as ciphertext would be every sync) must NOT create a new row.
	v3 := Violation{TS: 1000, RecordID: "rid-a", Keyword: "ciphertext-blob-2", URL: "different-ciphertext", Type: "url-keyword"}
	if err := db.IngestViolations(uid, 0, []Violation{v3}); err != nil {
		t.Fatalf("re-ingest ciphertext variant: %v", err)
	}
	if got, _ := db.Violations(uid); len(got) != 2 {
		t.Errorf("same RecordID with changed content must stay deduped, got %d rows", len(got))
	}
}

// TestBackfillURLHash covers the migration core: rows missing url_hash get it
// computed from their url, without touching other data.
func TestBackfillURLHash(t *testing.T) {
	db := openMem(t)
	// Insert bypassing IngestViolations so url_hash starts empty (mimics an old row).
	v := Violation{UserID: 1, TS: 1, Keyword: "k", URL: "http://example.com/x"}
	if err := db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.backfillURLHash(&Violation{}); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var got Violation
	if err := db.First(&got, v.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.URLHash != hashURL("http://example.com/x") {
		t.Errorf("url_hash not backfilled: %q", got.URLHash)
	}
}

func TestListUsersAndDeleteUserData(t *testing.T) {
	db := openMem(t)
	if _, err := db.CreateUser("admin@x.com", "longenough1"); err != nil {
		t.Fatal(err)
	}
	victim, err := db.CreateUser("victim@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}

	// Give the victim data across several tables.
	if err := db.IngestViolations(victim.ID, 0, []Violation{{TS: 1, Keyword: "k", URL: "http://x"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRulesConfig(victim.ID, []Rule{{Program: "p", Title: "t", Tag: "Activity:x"}}, map[string]string{"Activity:x": "#fff"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAPIToken(victim.ID, "dev"); err != nil {
		t.Fatal(err)
	}

	if us, err := db.ListUsers(); err != nil || len(us) != 2 {
		t.Fatalf("ListUsers = %v (len %d, err %v), want 2", us, len(us), err)
	}

	if err := db.DeleteUserData(victim.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Victim and all their data are gone; the admin is untouched.
	if _, err := db.UserByEmail("victim@x.com"); err == nil {
		t.Error("victim user should be deleted")
	}
	if n, _ := db.CountAPITokens(victim.ID); n != 0 {
		t.Errorf("victim tokens after delete = %d, want 0", n)
	}
	for src, c := range db.Counts(victim.ID) {
		if c != 0 {
			t.Errorf("victim %s count after delete = %d, want 0", src, c)
		}
	}
	if rules, _ := db.Rules(victim.ID); len(rules) != 0 {
		t.Errorf("victim rules after delete = %d, want 0", len(rules))
	}
	if _, err := db.UserByEmail("admin@x.com"); err != nil {
		t.Errorf("admin should be untouched: %v", err)
	}
}

func TestSetDeviceLimitByID(t *testing.T) {
	db := openMem(t)
	u, err := db.CreateUser("lim@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetDeviceLimitByID(u.ID, 4); err != nil {
		t.Fatal(err)
	}
	got, _ := db.UserByEmail("lim@x.com")
	if EffectiveDeviceLimit(got) != 4 {
		t.Errorf("device limit = %d, want 4", EffectiveDeviceLimit(got))
	}
	if err := db.SetDeviceLimitByID(99999, 3); err == nil {
		t.Error("setting limit on unknown id should error")
	}
}
