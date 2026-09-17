package store

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// VerificationTTL is how long an email-verification link stays valid.
const VerificationTTL = 24 * time.Hour

var (
	// ErrEmailTaken is returned by RegisterUser for an already-registered email.
	ErrEmailTaken = errors.New("email already registered")
	// ErrInvalidToken is returned by VerifyEmail for unknown/expired tokens.
	ErrInvalidToken = errors.New("invalid or expired verification link")
)

// RegisterUser creates an unverified self-service account plus a verification
// token to email. The account cannot log in until VerifyEmail is called.
func (db *DB) RegisterUser(email, password string) (*User, string, error) {
	email = normalizeEmail(email)
	if _, err := db.UserByEmail(email); err == nil {
		return nil, "", ErrEmailTaken
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", err
	}
	u, err := db.createUser(email, password, false)
	if err != nil {
		return nil, "", err
	}
	tok, err := db.CreateVerificationToken(u.ID)
	if err != nil {
		return nil, "", err
	}
	return u, tok, nil
}

// DeleteUser removes an account and its verification tokens — used to roll back
// a registration whose verification email couldn't be sent.
func (db *DB) DeleteUser(userID uint) error {
	return db.Transaction(func(tx *DB) error {
		tx.Where("user_id = ?", userID).Delete(&VerificationToken{})
		return tx.Delete(&User{}, userID).Error
	})
}

// CreateVerificationToken issues a single-use, expiring token for an account.
func (db *DB) CreateVerificationToken(userID uint) (string, error) {
	tok, err := randToken(32)
	if err != nil {
		return "", err
	}
	row := VerificationToken{Token: tok, UserID: userID, ExpiresAt: time.Now().Add(VerificationTTL)}
	if err := db.Create(&row).Error; err != nil {
		return "", err
	}
	return tok, nil
}

// VerifyEmail consumes a verification token and marks its account verified,
// returning the account. Unknown or expired tokens yield ErrInvalidToken.
func (db *DB) VerifyEmail(token string) (*User, error) {
	if token == "" {
		return nil, ErrInvalidToken
	}
	var vt VerificationToken
	if err := db.Where("token = ?", token).First(&vt).Error; err != nil {
		return nil, ErrInvalidToken
	}
	if time.Now().After(vt.ExpiresAt) {
		db.Where("token = ?", token).Delete(&VerificationToken{})
		return nil, ErrInvalidToken
	}
	var u User
	if err := db.First(&u, vt.UserID).Error; err != nil {
		return nil, err
	}
	if err := db.Transaction(func(tx *DB) error {
		if err := tx.Model(&User{}).Where("id = ?", u.ID).Update("verified", true).Error; err != nil {
			return err
		}
		return tx.Where("token = ?", token).Delete(&VerificationToken{}).Error
	}); err != nil {
		return nil, err
	}
	u.Verified = true
	_ = db.SeedAccountRules(u.ID) // seed starter rules once, now that the account is active
	return &u, nil
}
