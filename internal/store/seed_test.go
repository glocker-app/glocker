package store

import (
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(Options{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// System Glocker tags are seeded on Migrate, are content-stable, and re-seeding
// (an upgrade) doesn't duplicate them.
func TestSeedSystemTagsIdempotent(t *testing.T) {
	db := openTestDB(t) // Open() ran Migrate() -> SeedSystemTags()

	var n int64
	db.Model(&Tag{}).Where("scope = ?", "system").Count(&n)
	if int(n) != len(glockerTags) {
		t.Fatalf("system tags = %d, want %d", n, len(glockerTags))
	}
	// A distraction default and the hide behavior are present.
	var ent Tag
	if err := db.Where("scope = ? AND namespace = ? AND name = ?", "system", "Glocker", "entertainment").First(&ent).Error; err != nil {
		t.Fatal(err)
	}
	if !ent.Distraction {
		t.Error("entertainment should default to a distraction")
	}
	var hide Tag
	db.Where("scope = ? AND name = ?", "system", "hide").First(&hide)
	if hide.Behavior != "hide" {
		t.Errorf("hide behavior = %q, want hide", hide.Behavior)
	}

	// Re-seed (as a Migrate on upgrade would): still exactly one set.
	if err := db.SeedSystemTags(); err != nil {
		t.Fatal(err)
	}
	db.Model(&Tag{}).Where("scope = ?", "system").Count(&n)
	if int(n) != len(glockerTags) {
		t.Fatalf("after re-seed system tags = %d, want %d", n, len(glockerTags))
	}
}

// Starter rules seed once per account, link to the system tags, and never
// re-seed (so a deleted starter stays gone).
func TestSeedAccountRulesOnce(t *testing.T) {
	db := openTestDB(t)
	u, err := db.CreateUser("a@x.com", "pw123456") // verified -> seeds in createUser
	if err != nil {
		t.Fatal(err)
	}

	var rules []Rule
	db.Where("user_id = ?", u.ID).Order("position").Find(&rules)
	if len(rules) != len(glockerStarterRules) {
		t.Fatalf("seeded rules = %d, want %d", len(rules), len(glockerStarterRules))
	}
	// Positions are 0..n-1 in order.
	for i, r := range rules {
		if r.Position != i {
			t.Errorf("rule %d position = %d, want %d", i, r.Position, i)
		}
	}
	// Each rule has a rule_tag pointing at a system Glocker tag.
	var links int64
	db.Model(&RuleTag{}).Where("rule_id IN (?)", db.Model(&Rule{}).Where("user_id = ?", u.ID).Select("id")).Count(&links)
	if int(links) != len(glockerStarterRules) {
		t.Fatalf("rule_tags = %d, want %d", links, len(glockerStarterRules))
	}

	// Delete a starter, then seed again — it must NOT come back.
	db.Delete(&Rule{}, rules[0].ID)
	if err := db.SeedAccountRules(u.ID); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&Rule{}).Where("user_id = ?", u.ID).Count(&n)
	if int(n) != len(glockerStarterRules)-1 {
		t.Fatalf("after re-seed rules = %d, want %d (deleted starter must stay gone)", n, len(glockerStarterRules)-1)
	}
}

// A self-serve account gets no starter rules until it verifies, then exactly one set.
func TestSeedOnVerification(t *testing.T) {
	db := openTestDB(t)
	u, tok, err := db.RegisterUser("b@x.com", "pw123456")
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&Rule{}).Where("user_id = ?", u.ID).Count(&n)
	if n != 0 {
		t.Fatalf("unverified account has %d rules, want 0", n)
	}
	if _, err := db.VerifyEmail(tok); err != nil {
		t.Fatal(err)
	}
	db.Model(&Rule{}).Where("user_id = ?", u.ID).Count(&n)
	if int(n) != len(glockerStarterRules) {
		t.Fatalf("after verify rules = %d, want %d", n, len(glockerStarterRules))
	}
}
