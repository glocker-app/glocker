package store

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ErrTagNamespaceConflict is returned when a rule would carry two tags in the
// same namespace (the one-tag-per-namespace invariant).
var ErrTagNamespaceConflict = errors.New("a rule can carry at most one tag per namespace")

// ErrUnknownTag is returned when a rule references a tag the account can't use.
var ErrUnknownTag = errors.New("unknown or inaccessible tag")

// RuleWithTags is a rule plus the ids of the registry tags it attaches.
type RuleWithTags struct {
	ID       uint   `json:"id"`
	Position int    `json:"position"`
	Program  string `json:"program"`
	Title    string `json:"title"`
	TagIDs   []uint `json:"tagIds"`
}

// RuleInput is one rule as the client submits it (order + matcher + tag ids).
type RuleInput struct {
	Position int    `json:"position"`
	Program  string `json:"program"`
	Title    string `json:"title"`
	TagIDs   []uint `json:"tagIds"`
}

// RulesWithTags returns the account's rules in precedence order (position, then
// id) with their tag ids.
func (db *DB) RulesWithTags(userID uint) ([]RuleWithTags, error) {
	var rules []Rule
	if err := db.Where("user_id = ?", userID).Order("position, id").Find(&rules).Error; err != nil {
		return nil, err
	}
	ids := make([]uint, len(rules))
	for i, r := range rules {
		ids[i] = r.ID
	}
	byRule := map[uint][]uint{}
	if len(ids) > 0 {
		var links []RuleTag
		if err := db.Where("rule_id IN ?", ids).Find(&links).Error; err != nil {
			return nil, err
		}
		for _, l := range links {
			byRule[l.RuleID] = append(byRule[l.RuleID], l.TagID)
		}
	}
	out := make([]RuleWithTags, len(rules))
	for i, r := range rules {
		out[i] = RuleWithTags{ID: r.ID, Position: r.Position, Program: r.Program, Title: r.Title, TagIDs: byRule[r.ID]}
	}
	return out, nil
}

// SetRules replaces the account's rules with the given set (replace-all, like the
// old rules config). Each rule may carry at most one tag per namespace, and every
// tag id must be one the account can use (a system tag or its own). The legacy
// Tag column is written from the first tag so the pre-registry read path keeps
// working during the transition.
func (db *DB) SetRules(userID uint, in []RuleInput) error {
	// Build the set of tags this account may reference: namespace + "ns:name".
	visible, err := db.TagsForUser(userID)
	if err != nil {
		return err
	}
	ns := make(map[uint]string, len(visible))
	label := make(map[uint]string, len(visible))
	for _, t := range visible {
		ns[t.ID] = t.Namespace
		label[t.ID] = t.Namespace + ":" + t.Name
	}

	// Validate before touching anything.
	for ri, r := range in {
		seen := map[string]bool{}
		for _, id := range r.TagIDs {
			n, ok := ns[id]
			if !ok {
				return fmt.Errorf("rule %d: %w (tag %d)", ri, ErrUnknownTag, id)
			}
			if seen[n] {
				return fmt.Errorf("rule %d: %w (namespace %q)", ri, ErrTagNamespaceConflict, n)
			}
			seen[n] = true
		}
	}

	return db.Transaction(func(tx *DB) error {
		// Drop this account's rule_tags, then its rules.
		if err := tx.Where("rule_id IN (?)", tx.Model(&Rule{}).Where("user_id = ?", userID).Select("id")).
			Delete(&RuleTag{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&Rule{}).Error; err != nil {
			return err
		}
		for i, r := range in {
			pos := r.Position
			if pos == 0 {
				pos = i
			}
			legacy := ""
			if len(r.TagIDs) > 0 {
				legacy = label[r.TagIDs[0]]
			}
			row := Rule{UserID: userID, Position: pos, Program: strings.TrimSpace(r.Program), Title: strings.TrimSpace(r.Title), Tag: legacy}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			for _, id := range r.TagIDs {
				if err := tx.Create(&RuleTag{RuleID: row.ID, TagID: id}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// backfillRulesToTags migrates legacy single-tag rules (Rule.Tag, tag_colors)
// into the registry model: each un-linked rule with a Tag gets a matching tag
// (a system Glocker:* tag, or a created user tag carrying its legacy colour) and
// a rule_tags link. Idempotent — a rule that already has links is skipped, so
// re-running (a later Migrate) is a no-op. Non-destructive: the Tag column stays.
func (db *DB) backfillRulesToTags() error {
	// rule ids that already have a link
	var linked []RuleTag
	if err := db.Select("rule_id").Find(&linked).Error; err != nil {
		return err
	}
	hasLink := map[uint]bool{}
	for _, l := range linked {
		hasLink[l.RuleID] = true
	}

	var rules []Rule
	if err := db.Where("tag <> ''").Order("user_id, position, id").Find(&rules).Error; err != nil {
		return err
	}

	// legacy per-user tag colours
	var tcs []TagColor
	if err := db.Find(&tcs).Error; err != nil {
		return err
	}
	colorOf := map[string]string{} // "user_id\x00tag" -> color
	for _, tc := range tcs {
		colorOf[key(tc.UserID, tc.Tag)] = tc.Color
	}

	// cache system Glocker tag ids by name
	var sys []Tag
	if err := db.Where("scope = ? AND namespace = ?", "system", "Glocker").Find(&sys).Error; err != nil {
		return err
	}
	sysID := map[string]uint{}
	for _, t := range sys {
		sysID[t.Name] = t.ID
	}

	pos := map[uint]int{} // per-user running position
	return db.Transaction(func(tx *DB) error {
		for _, r := range rules {
			if hasLink[r.ID] {
				pos[r.UserID] = pos[r.UserID] + 1
				continue
			}
			namespace, name := splitLegacyTag(r.Tag)

			var tagID uint
			if namespace == "Glocker" {
				tagID = sysID[name]
			}
			if tagID == 0 { // user tag (or an unknown Glocker name -> keep it as the user's)
				color := colorOf[key(r.UserID, r.Tag)]
				if color == "" {
					color = "#7c9cff"
				}
				var ut Tag
				err := tx.Where("scope = ? AND user_id = ? AND namespace = ? AND name = ?", "user", r.UserID, namespace, name).First(&ut).Error
				if errors.Is(err, gorm.ErrRecordNotFound) {
					ut = Tag{Scope: "user", UserID: r.UserID, Namespace: namespace, Name: name, Color: color}
					if err := tx.Create(&ut).Error; err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
				tagID = ut.ID
			}

			if err := tx.Create(&RuleTag{RuleID: r.ID, TagID: tagID}).Error; err != nil {
				return err
			}
			if r.Position == 0 { // give unordered legacy rules a stable order
				if err := tx.Model(&Rule{}).Where("id = ?", r.ID).Update("position", pos[r.UserID]).Error; err != nil {
					return err
				}
			}
			pos[r.UserID] = pos[r.UserID] + 1
		}
		return nil
	})
}

func key(userID uint, tag string) string { return fmt.Sprintf("%d\x00%s", userID, tag) }

// splitLegacyTag maps a legacy tag string to (namespace, name): "ns:name" splits
// on the first colon; the magic "hidden" becomes Glocker:hide; anything else goes
// under the default user "Activity" namespace.
func splitLegacyTag(tag string) (namespace, name string) {
	tag = strings.TrimSpace(tag)
	if strings.EqualFold(tag, "hidden") {
		return "Glocker", "hide"
	}
	if i := strings.IndexByte(tag, ':'); i >= 0 {
		return tag[:i], tag[i+1:]
	}
	return "Activity", tag
}
