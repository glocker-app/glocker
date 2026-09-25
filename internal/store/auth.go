package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"gorm.io/gorm"
)

// SessionTTL is how long a browser login stays valid.
const SessionTTL = 30 * 24 * time.Hour

// DefaultUsername / DefaultEmail identify the implicit account used when auth is
// disabled (the single-user self-hosted case).
const (
	DefaultUsername = "local"
	DefaultEmail    = "local@localhost"
)

// EnsureDefaultUser finds or creates the implicit single-user account. Used when
// auth is off so all data has an owner without anyone logging in. The password
// is random and unused (there's no login in this mode); if auth is later enabled,
// reset it with `glockpeek -passwd`. Located by the legacy username so an
// existing pre-email database is reused rather than duplicated.
func (db *DB) EnsureDefaultUser() (*User, error) {
	var u User
	if err := db.Where("username = ?", DefaultUsername).First(&u).Error; err == nil {
		_ = db.SeedAccountRules(u.ID) // once (guarded); catches an existing local account on upgrade
		return &u, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	pw, err := randToken(24)
	if err != nil {
		return nil, err
	}
	hash, err := argon2id.CreateHash(pw, argon2id.DefaultParams)
	if err != nil {
		return nil, err
	}
	nu := &User{Email: DefaultEmail, Username: DefaultUsername, PasswordHash: hash, Verified: true}
	if err := db.Create(nu).Error; err != nil {
		return nil, err
	}
	_ = db.SeedAccountRules(nu.ID)
	return nu, nil
}

// ErrInvalidCredentials is returned by Authenticate on unknown account or bad
// password (same error for both, to avoid leaking which emails exist).
var ErrInvalidCredentials = errors.New("invalid email or password")

// randToken returns n bytes of crypto-random data as a URL-safe base64 string.
func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is the deterministic hash stored for API tokens (they're
// high-entropy random strings, so a fast hash is fine — unlike passwords).
func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// ── Users ───────────────────────────────────────────────

// normalizeEmail lower-cases and trims an email for consistent lookups.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// CreateUser creates an admin/CLI account identified by email, with an
// argon2id-hashed password. Such accounts are trusted, so they are created
// already verified. Username is set to the email to satisfy the legacy column.
// (Self-service signups use RegisterUser, which creates unverified accounts.)
func (db *DB) CreateUser(email, password string) (*User, error) {
	return db.createUser(email, password, true)
}

func (db *DB) createUser(email, password string, verified bool) (*User, error) {
	email = normalizeEmail(email)
	if email == "" || password == "" {
		return nil, errors.New("email and password are required")
	}
	hash, err := argon2id.CreateHash(password, argon2id.DefaultParams)
	if err != nil {
		return nil, err
	}
	u := &User{Email: email, Username: email, PasswordHash: hash, Verified: verified}
	if err := db.Create(u).Error; err != nil {
		return nil, err
	}
	// Accounts that are active on creation (admin-added, verified) get their
	// starter rules now; self-serve accounts get them at verification instead.
	if verified {
		_ = db.SeedAccountRules(u.ID)
	}
	return u, nil
}

// Authenticate verifies an email/password and returns the user.
func (db *DB) Authenticate(email, password string) (*User, error) {
	var u User
	if err := db.Where("email = ?", normalizeEmail(email)).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Do the work anyway to keep timing similar whether or not the account
			// exists, then fail uniformly.
			_, _ = argon2id.ComparePasswordAndHash(password, "$argon2id$v=19$m=65536,t=1,p=2$YWJjZGVmZ2g$aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaA")
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	ok, err := argon2id.ComparePasswordAndHash(password, u.PasswordHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	return &u, nil
}

// SetPassword updates an account's password (by email).
func (db *DB) SetPassword(email, password string) error {
	if password == "" {
		return errors.New("password is required")
	}
	hash, err := argon2id.CreateHash(password, argon2id.DefaultParams)
	if err != nil {
		return err
	}
	res := db.Model(&User{}).Where("email = ?", normalizeEmail(email)).Update("password_hash", hash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ── Browser sessions ────────────────────────────────────

// CreateSession issues a new opaque session token for a user.
func (db *DB) CreateSession(userID uint) (string, error) {
	tok, err := randToken(32)
	if err != nil {
		return "", err
	}
	s := Session{Token: tok, UserID: userID, ExpiresAt: time.Now().Add(SessionTTL)}
	if err := db.Create(&s).Error; err != nil {
		return "", err
	}
	return tok, nil
}

// UserBySession returns the user for a valid, unexpired session token.
func (db *DB) UserBySession(token string) (*User, error) {
	if token == "" {
		return nil, ErrInvalidCredentials
	}
	var s Session
	if err := db.Where("token = ?", token).First(&s).Error; err != nil {
		return nil, ErrInvalidCredentials
	}
	if time.Now().After(s.ExpiresAt) {
		db.Delete(&s)
		return nil, ErrInvalidCredentials
	}
	var u User
	if err := db.First(&u, s.UserID).Error; err != nil {
		return nil, ErrInvalidCredentials
	}
	return &u, nil
}

// DeleteSession removes a session (logout).
func (db *DB) DeleteSession(token string) error {
	return db.Where("token = ?", token).Delete(&Session{}).Error
}

// ── API tokens (syncer / ingest) ────────────────────────

// CreateAPIToken mints a bearer token for a user and returns the plaintext
// (shown once — only the hash is stored).
func (db *DB) CreateAPIToken(userID uint, name string) (string, error) {
	tok, err := randToken(32)
	if err != nil {
		return "", err
	}
	row := APIToken{Name: name, TokenHash: hashToken(tok), UserID: userID, CreatedAt: time.Now()}
	if err := db.Create(&row).Error; err != nil {
		return "", err
	}
	return tok, nil
}

// UserByAPIToken returns the user a bearer token belongs to, and stamps
// LastUsedAt.
func (db *DB) UserByAPIToken(token string) (*User, error) {
	u, _, err := db.UserAndDeviceByAPIToken(token)
	return u, err
}

// UserAndDeviceByAPIToken resolves a bearer token to its user AND the token's own
// id, which serves as the device id (one API token = one connected device). The
// ingest path stamps this onto every row so an account's devices don't collide.
func (db *DB) UserAndDeviceByAPIToken(token string) (*User, uint, error) {
	if token == "" {
		return nil, 0, ErrInvalidCredentials
	}
	var row APIToken
	if err := db.Where("token_hash = ?", hashToken(token)).First(&row).Error; err != nil {
		return nil, 0, ErrInvalidCredentials
	}
	if row.RevokedAt != nil {
		// Revoked device: reject ingest. History under its device_id is kept.
		return nil, 0, ErrInvalidCredentials
	}
	now := time.Now()
	db.Model(&row).Update("last_used_at", &now)
	var u User
	if err := db.First(&u, row.UserID).Error; err != nil {
		return nil, 0, ErrInvalidCredentials
	}
	return &u, row.ID, nil
}

// UserByEmail looks up a user by email (for the admin CLI).
func (db *DB) UserByEmail(email string) (*User, error) {
	var u User
	if err := db.Where("email = ?", normalizeEmail(email)).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// UserByID looks up a user by primary key.
func (db *DB) UserByID(id uint) (*User, error) {
	var u User
	if err := db.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}
