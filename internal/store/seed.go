package store

import "gorm.io/gorm/clause"

// This file holds the canonical Glocker taxonomy: the shared system tags and the
// starter rules seeded into each account. See docs/tags-and-rules.md. The tags
// are seeded globally (once per DB, refreshed on upgrade); the rules are copied
// into an account once, when it becomes active, and are the user's from then on.

// glockerTags is the fixed Glocker:* vocabulary. Distraction defaults marked;
// "hide" is a behavior tag (its windows drop out of usage totals).
var glockerTags = []Tag{
	{Namespace: "Glocker", Name: "entertainment", Color: "#c0468f", Distraction: true},
	{Namespace: "Glocker", Name: "social", Color: "#6aa6ff", Distraction: true},
	{Namespace: "Glocker", Name: "games", Color: "#f0a020", Distraction: true},
	{Namespace: "Glocker", Name: "adult", Color: "#ff4d4d", Distraction: true},
	{Namespace: "Glocker", Name: "news", Color: "#2f9e8f"},
	{Namespace: "Glocker", Name: "shopping", Color: "#b083f0"},
	{Namespace: "Glocker", Name: "messaging", Color: "#4fd1c5"},
	{Namespace: "Glocker", Name: "work", Color: "#7c9cff"},
	{Namespace: "Glocker", Name: "dev", Color: "#9fce6a"},
	{Namespace: "Glocker", Name: "hide", Color: "#5b6675", Behavior: "hide"},
}

// glockerStarterRules seed a fresh account so it classifies out of the box. Each
// references a Glocker tag by name. Matching (the regexes) runs in the browser.
var glockerStarterRules = []struct{ Program, Title, Tag string }{
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

	return db.Transaction(func(tx *DB) error {
		for i, sr := range glockerStarterRules {
			r := Rule{UserID: userID, Position: i, Program: sr.Program, Title: sr.Title, Tag: "Glocker:" + sr.Tag}
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
