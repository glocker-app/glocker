package store

import (
	"errors"
	"time"

	"github.com/alexedwards/argon2id"
)

// PasswordResetTTL is how long a password-reset link stays valid — short, since
// it grants a login-password change.
const PasswordResetTTL = time.Hour

// PasswordResetToken is a single-use, expiring token emailed so an account can set
// a new login password. Consumed (deleted) on use. Separate from
// VerificationToken so an email-confirm link can never reset a password.
//
// Resetting the login password is independent of end-to-end encryption: the vault
// master key is wrapped under the user's encryption *passphrase*, not their login
// password, so a reset restores dashboard access only and never touches (or can
// recover) the encrypted data.
type PasswordResetToken struct {
	Token     string    `gorm:"primaryKey" json:"-"`
	UserID    uint      `gorm:"index;not null" json:"-"`
	ExpiresAt time.Time `gorm:"index" json:"-"`
	CreatedAt time.Time `json:"-"`
}

// CreatePasswordResetToken issues one active reset token for an account, replacing
// any existing one (so an older link stops working and valid links can't pile up).
func (db *DB) CreatePasswordResetToken(userID uint) (string, error) {
	tok, err := randToken(32)
	if err != nil {
		return "", err
	}
	if err := db.Transaction(func(tx *DB) error {
		tx.Where("user_id = ?", userID).Delete(&PasswordResetToken{})
		return tx.Create(&PasswordResetToken{Token: tok, UserID: userID, ExpiresAt: time.Now().Add(PasswordResetTTL)}).Error
	}); err != nil {
		return "", err
	}
	return tok, nil
}

// HasActivePasswordReset reports whether the account already has an unexpired reset
// token, so a repeat "forgot" request doesn't email a second link — bounding
// reset-email volume to one per TTL per account (anti-bombing).
func (db *DB) HasActivePasswordReset(userID uint) bool {
	var n int64
	db.Model(&PasswordResetToken{}).Where("user_id = ? AND expires_at > ?", userID, time.Now()).Count(&n)
	return n > 0
}

// ResetPassword consumes a reset token, sets the account's new login password, and
// invalidates all of its browser sessions (so a stolen cookie can't outlive a
// reset). Unknown/expired tokens yield ErrInvalidToken.
func (db *DB) ResetPassword(token, newPassword string) error {
	if token == "" {
		return ErrInvalidToken
	}
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	var prt PasswordResetToken
	if err := db.Where("token = ?", token).First(&prt).Error; err != nil {
		return ErrInvalidToken
	}
	if time.Now().After(prt.ExpiresAt) {
		db.Where("token = ?", token).Delete(&PasswordResetToken{})
		return ErrInvalidToken
	}
	hash, err := argon2id.CreateHash(newPassword, argon2id.DefaultParams)
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *DB) error {
		if err := tx.Model(&User{}).Where("id = ?", prt.UserID).Update("password_hash", hash).Error; err != nil {
			return err
		}
		tx.Where("user_id = ?", prt.UserID).Delete(&Session{}) // sign out everywhere
		return tx.Where("token = ?", token).Delete(&PasswordResetToken{}).Error
	})
}
