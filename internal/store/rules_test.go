package store

import (
	"errors"
	"testing"
)

func sysTagID(t *testing.T, db *DB, name string) uint {
	t.Helper()
	var tg Tag
	if err := db.Where("scope = ? AND namespace = ? AND name = ?", "system", "Glocker", name).First(&tg).Error; err != nil {
		t.Fatalf("system tag %q: %v", name, err)
	}
	return tg.ID
}

func TestSetRulesRoundTrip(t *testing.T) {
	db := openTestDB(t)
	u, err := db.CreateUser("a@x.com", "pw123456")
	if err != nil {
		t.Fatal(err)
	}
	ent := sysTagID(t, db, "entertainment")
	work, err := db.CreateUserTag(u.ID, "Activity", "work", "#3aa0ff", false)
	if err != nil {
		t.Fatal(err)
	}

	// One rule carrying two namespaces (Glocker + Activity).
	if err := db.SetRules(u.ID, []RuleInput{
		{Program: "^Emacs$", TagIDs: []uint{ent, work.ID}},
	}); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	rules, err := db.RulesWithTags(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Program != "^Emacs$" || len(rules[0].TagIDs) != 2 {
		t.Fatalf("rules = %+v", rules)
	}
	// Legacy Tag column is dual-written from the first tag.
	var r Rule
	db.Where("user_id = ?", u.ID).First(&r)
	if r.Tag != "Glocker:entertainment" {
		t.Errorf("legacy Tag = %q, want Glocker:entertainment", r.Tag)
	}
}

func TestSetRulesRejectsInvalid(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("b@x.com", "pw123456")
	ent, games := sysTagID(t, db, "entertainment"), sysTagID(t, db, "games")

	// Two tags in the same namespace on one rule.
	err := db.SetRules(u.ID, []RuleInput{{Program: "x", TagIDs: []uint{ent, games}}})
	if !errors.Is(err, ErrTagNamespaceConflict) {
		t.Errorf("same-namespace should be ErrTagNamespaceConflict, got %v", err)
	}
	// A tag the account can't see.
	err = db.SetRules(u.ID, []RuleInput{{Program: "y", TagIDs: []uint{999999}}})
	if !errors.Is(err, ErrUnknownTag) {
		t.Errorf("unknown tag should be ErrUnknownTag, got %v", err)
	}
}

// A legacy single-tag rule (Rule.Tag, no link) is migrated into the registry:
// a plain tag becomes a user tag carrying its legacy colour; "hidden" maps to the
// system Glocker:hide tag; re-running the backfill is a no-op.
func TestBackfillLegacyRules(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.CreateUser("c@x.com", "pw123456") // seeds linked starter rules

	// Insert legacy, unlinked rules the way an old DB would have them.
	db.Create(&Rule{UserID: u.ID, Program: "^Emacs$", Tag: "work"})
	db.Create(&Rule{UserID: u.ID, Program: "^KeePass$", Tag: "hidden"})
	db.Create(&TagColor{UserID: u.ID, Tag: "work", Color: "#abcdef"})

	if err := db.backfillRulesToTags(); err != nil {
		t.Fatal(err)
	}

	// "work" -> a user tag Activity:work with the legacy colour, and a link.
	var work Tag
	if err := db.Where("scope = ? AND user_id = ? AND namespace = ? AND name = ?", "user", u.ID, "Activity", "work").First(&work).Error; err != nil {
		t.Fatalf("Activity:work not created: %v", err)
	}
	if work.Color != "#abcdef" {
		t.Errorf("colour = %q, want #abcdef", work.Color)
	}
	// "hidden" -> the system Glocker:hide tag (not a new user tag).
	var hideLinks int64
	db.Model(&RuleTag{}).Where("tag_id = ?", sysTagID(t, db, "hide")).Count(&hideLinks)
	if hideLinks != 1 {
		t.Errorf("expected the hidden rule linked to Glocker:hide, got %d links", hideLinks)
	}

	// Every legacy rule now has exactly one link; re-running changes nothing.
	countLinks := func() int64 {
		var n int64
		db.Model(&RuleTag{}).Count(&n)
		return n
	}
	before := countLinks()
	if err := db.backfillRulesToTags(); err != nil {
		t.Fatal(err)
	}
	if countLinks() != before {
		t.Errorf("re-running backfill changed link count %d -> %d", before, countLinks())
	}
}
