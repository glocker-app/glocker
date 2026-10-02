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
