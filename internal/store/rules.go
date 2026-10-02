package store

import (
	"errors"
	"fmt"
	"strings"
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
// tag id must be one the account can use (a system tag or its own). Tags are
// attached via the rule_tags registry join.
func (db *DB) SetRules(userID uint, in []RuleInput) error {
	// Build the set of tags this account may reference, by namespace.
	visible, err := db.TagsForUser(userID)
	if err != nil {
		return err
	}
	ns := make(map[uint]string, len(visible))
	for _, t := range visible {
		ns[t.ID] = t.Namespace
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
			row := Rule{UserID: userID, Position: pos, Program: strings.TrimSpace(r.Program), Title: strings.TrimSpace(r.Title)}
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
