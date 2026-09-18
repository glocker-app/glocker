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

// TestImportDefaultRulesAndSeed covers the hosted-only starter-rule template:
// ImportDefaultRules validates + replaces the table, and a new account seeds from
// it (not the built-in set) when it holds rows.
func TestImportDefaultRulesAndSeed(t *testing.T) {
	db := openTestDB(t)
	imported, skipped, err := db.ImportDefaultRules([]DefaultRule{
		{Tag: "entertainment", Title: "youtube|netflix"},
		{Tag: "dev", Program: "kitty|emacs"},
		{Tag: "bogus", Title: "x"}, // unknown tag -> skipped
		{Tag: "work"},              // no matcher -> skipped
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported != 2 || skipped != 2 {
		t.Fatalf("import imported=%d skipped=%d, want 2/2", imported, skipped)
	}

	// A new account seeds from the template (2 rules), not the built-in 7.
	u, err := db.CreateUser("seedtmpl@x.com", "pw123456")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := db.RulesWithTags(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("seeded %d rules, want 2 from the template", len(rules))
	}

	// Re-importing replaces the table wholesale.
	if n, _, err := db.ImportDefaultRules([]DefaultRule{{Tag: "work", Program: "obsidian"}}); err != nil || n != 1 {
		t.Fatalf("re-import n=%d err=%v", n, err)
	}
	var cnt int64
	db.Model(&DefaultRule{}).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("default_rules count=%d, want 1 after replace", cnt)
	}
}

// TestSeedFallsBackToBuiltin: with no template imported, a new account still
// seeds from the built-in glockerStarterRules.
func TestSeedFallsBackToBuiltin(t *testing.T) {
	db := openTestDB(t)
	u, err := db.CreateUser("builtin@x.com", "pw123456")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := db.RulesWithTags(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != len(glockerStarterRules) {
		t.Fatalf("seeded %d rules, want built-in %d", len(rules), len(glockerStarterRules))
	}
}
