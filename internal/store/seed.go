package store

import "gorm.io/gorm/clause"

// This file holds the canonical Glocker taxonomy: the shared system tags and the
// starter rules seeded into each account. See docs/tags-and-rules.md. The tags
// are seeded globally (once per DB, refreshed on upgrade); the rules are copied
// into an account once, when it becomes active, and are the user's from then on.

// glockerTags is the fixed Glocker:* vocabulary. Distraction defaults marked;
// "hide" is a behavior tag (its windows drop out of usage totals).
var glockerTags = []Tag{
	// Curated palette: even hue spread, medium saturation, ~65% lightness so the
	// categories stay distinct yet calm on the near-black background.
	{Namespace: "Glocker", Name: "entertainment", Color: "#b07398", Distraction: true},
	{Namespace: "Glocker", Name: "social", Color: "#6b8cb0", Distraction: true},
	{Namespace: "Glocker", Name: "games", Color: "#c9a04e", Distraction: true},
	{Namespace: "Glocker", Name: "adult", Color: "#bd6b66", Distraction: true},
	{Namespace: "Glocker", Name: "news", Color: "#5f9ea0"},
	{Namespace: "Glocker", Name: "shopping", Color: "#9b7aa0"},
	{Namespace: "Glocker", Name: "messaging", Color: "#6fa6b0"},
	{Namespace: "Glocker", Name: "work", Color: "#6e7f99"},
	{Namespace: "Glocker", Name: "dev", Color: "#7a9b76"},
	{Namespace: "Glocker", Name: "hide", Color: "#9a927f", Behavior: "hide"},
}

// glockerStarterRules seed a fresh account so it classifies out of the box. Each
// references a Glocker tag by name. Matching (the regexes) runs in the browser.
type starterRule struct{ Program, Title, Tag string }

var glockerStarterRules = []starterRule{
	{Title: `youtube|netflix|twitch|hulu|disney\+|primevideo`, Tag: "entertainment"},
	{Program: `mpv|vlc|smplayer|celluloid|kodi|stremio`, Tag: "entertainment"},
	{Program: `steam|lutris|heroic|gamescope`, Tag: "games"},
	{Title: `reddit|twitter|x\.com|instagram|facebook|tiktok|mastodon`, Tag: "social"},
	{Title: `bbc\.|cnn\.|theguardian|nytimes|news\.`, Tag: "news"},
	{Program: `code|codium|nvim|jetbrains|goland|pycharm|kitty|alacritty|wezterm`, Tag: "dev"},
	{Program: `slack|discord|telegram|signal-desktop|thunderbird|zoom`, Tag: "messaging"},
}

// SeedSystemTags upserts the shared Glocker:* taxonomy (scope "system", UserID 0).
// Idempotent — safe on every Migrate, so a binary upgrade refreshes colours,
// distraction defaults, and any new categories.
func (db *DB) SeedSystemTags() error {
	rows := make([]Tag, len(glockerTags))
	for i, t := range glockerTags {
		t.Scope = "system"
		t.UserID = 0
		rows[i] = t
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "user_id"}, {Name: "namespace"}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"color", "distraction", "behavior"}),
	}).Create(&rows).Error
}

// SeedAccountRules seeds the Glocker starter rules into an account exactly once,
// guarded by users.rules_seeded. Called when an account becomes active. A no-op
// if already seeded, so deleted starters never come back. Each starter rule is
// written both as a rule_tag link (the new model) and with the legacy Tag string
// (so the current Usage tab keeps working until the UI moves over).
// accountStarterRules is the set a new account is seeded with: the imported
// default_rules template if it holds any (hosted, loaded via
// `glockpeek -import-rules`), else the built-in minimal set that ships with the
// open-source build.
func (db *DB) accountStarterRules() []starterRule {
	var rows []DefaultRule
	if err := db.Order("position, id").Find(&rows).Error; err == nil && len(rows) > 0 {
		out := make([]starterRule, len(rows))
		for i, r := range rows {
			out[i] = starterRule{Program: r.Program, Title: r.Title, Tag: r.Tag}
		}
		return out
	}
	return glockerStarterRules
}

// ImportDefaultRules replaces the default_rules template with rules (position =
// slice order). Only rules whose Tag is a known Glocker tag name are kept;
// returns how many were imported and how many were skipped. Hosted-only.
func (db *DB) ImportDefaultRules(rules []DefaultRule) (imported, skipped int, err error) {
	valid := map[string]bool{}
	for _, t := range glockerTags {
		valid[t.Name] = true
	}
	var keep []DefaultRule
	for _, r := range rules {
		if r.Tag == "" || (r.Program == "" && r.Title == "") || !valid[r.Tag] {
			skipped++
			continue
		}
		keep = append(keep, r)
	}
	err = db.Transaction(func(tx *DB) error {
		if err := tx.Where("1 = 1").Delete(&DefaultRule{}).Error; err != nil {
			return err
		}
		for i := range keep {
			keep[i].ID = 0
			keep[i].Position = i
			if err := tx.Create(&keep[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return len(keep), skipped, err
}

func (db *DB) SeedAccountRules(userID uint) error {
	var u User
	if err := db.First(&u, userID).Error; err != nil {
		return err
	}
	if u.RulesSeeded {
		return nil
	}

	// name -> system Glocker tag id
	var sys []Tag
	if err := db.Where("scope = ? AND namespace = ?", "system", "Glocker").Find(&sys).Error; err != nil {
		return err
	}
	tagID := map[string]uint{}
	for _, t := range sys {
		tagID[t.Name] = t.ID
	}

	rules := db.accountStarterRules()
	return db.Transaction(func(tx *DB) error {
		for i, sr := range rules {
			r := Rule{UserID: userID, Position: i, Program: sr.Program, Title: sr.Title}
			if err := tx.Create(&r).Error; err != nil {
				return err
			}
			if id := tagID[sr.Tag]; id != 0 {
				if err := tx.Create(&RuleTag{RuleID: r.ID, TagID: id}).Error; err != nil {
					return err
				}
			}
		}
		return tx.Model(&User{}).Where("id = ?", userID).Update("rules_seeded", true).Error
	})
}
