package store

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrSystemTag is returned when a caller tries to rename/delete a system tag.
var ErrSystemTag = errors.New("system tags cannot be renamed or deleted")

// TagsForUser returns the registry as the account sees it: the shared system
// tags (with this account's distraction overrides applied) followed by the
// account's own tags. System first, each group ordered by namespace then name.
func (db *DB) TagsForUser(userID uint) ([]Tag, error) {
	var sys, usr []Tag
	if err := db.Where("scope = ?", "system").Order("namespace, name").Find(&sys).Error; err != nil {
		return nil, err
	}
	if err := db.Where("scope = ? AND user_id = ?", "user", userID).Order("namespace, name").Find(&usr).Error; err != nil {
		return nil, err
	}
	var ov []TagOverride
	if err := db.Where("user_id = ?", userID).Find(&ov).Error; err != nil {
		return nil, err
	}
	om := make(map[uint]bool, len(ov))
	for _, o := range ov {
		om[o.TagID] = o.Distraction
	}
	for i := range sys {
		if d, ok := om[sys[i].ID]; ok {
			sys[i].Distraction = d
		}
	}
	return append(sys, usr...), nil
}

// CreateUserTag adds a tag in one of the account's own namespaces. The unique
// index rejects a duplicate (same namespace+name).
func (db *DB) CreateUserTag(userID uint, namespace, name, color string, distraction bool) (*Tag, error) {
	t := &Tag{Scope: "user", UserID: userID, Namespace: namespace, Name: name, Color: color, Distraction: distraction}
	if err := db.Create(t).Error; err != nil {
		return nil, err
	}
	return t, nil
}

// UpdateUserTag edits one of the account's own tags. It never touches a system
// tag (scope+user_id in the WHERE); RowsAffected 0 means "not yours / not found".
func (db *DB) UpdateUserTag(userID, id uint, namespace, name, color string, distraction bool) error {
	res := db.Model(&Tag{}).Where("id = ? AND scope = ? AND user_id = ?", id, "user", userID).
		Updates(map[string]any{"namespace": namespace, "name": name, "color": color, "distraction": distraction})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SetTagDistraction flips a tag's distraction flag for this account: in place for
// the account's own tag, or via an override row for a shared system tag (so the
// shared tag itself is untouched).
func (db *DB) SetTagDistraction(userID, id uint, distraction bool) error {
	var t Tag
	if err := db.First(&t, id).Error; err != nil {
		return err
	}
	if t.Scope == "user" {
		if t.UserID != userID {
			return gorm.ErrRecordNotFound
		}
		return db.Model(&Tag{}).Where("id = ?", id).Update("distraction", distraction).Error
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "tag_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"distraction"}),
	}).Create(&TagOverride{UserID: userID, TagID: id, Distraction: distraction}).Error
}

// DeleteUserTag removes one of the account's own tags and unlinks it from any of
// the account's rules. System tags can't be deleted.
func (db *DB) DeleteUserTag(userID, id uint) error {
	var t Tag
	if err := db.First(&t, id).Error; err != nil {
		return err
	}
	if t.Scope != "user" {
		return ErrSystemTag
	}
	if t.UserID != userID {
		return gorm.ErrRecordNotFound
	}
	return db.Transaction(func(tx *DB) error {
		if err := tx.Delete(&Tag{}, id).Error; err != nil {
			return err
		}
		return tx.Where("tag_id = ?", id).Delete(&RuleTag{}).Error
	})
}
