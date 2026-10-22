package store

import (
	"errors"
	"testing"
	"time"
)

func TestPasswordReset(t *testing.T) {
	db := openTestDB(t)
	u, err := db.createUser("u@x.com", "oldpass12", true)
	if err != nil {
		t.Fatal(err)
	}
	// A live session that must not survive the reset.
	sess, err := db.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}

	tok, err := db.CreatePasswordResetToken(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !db.HasActivePasswordReset(u.ID) {
		t.Fatal("expected an active reset token")
	}

	// Too-short password rejected; token stays valid.
	if err := db.ResetPassword(tok, "short"); err == nil {
		t.Fatal("expected short-password error")
	}

	if err := db.ResetPassword(tok, "newpass34"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := db.Authenticate("u@x.com", "newpass34"); err != nil {
		t.Fatalf("new password should authenticate: %v", err)
	}
	if _, err := db.Authenticate("u@x.com", "oldpass12"); err == nil {
		t.Fatal("old password should no longer work")
	}
	if _, err := db.UserBySession(sess); err == nil {
		t.Fatal("session should be invalidated after reset")
	}
	// Single-use.
	if err := db.ResetPassword(tok, "another78"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("token should be consumed: %v", err)
	}
	if db.HasActivePasswordReset(u.ID) {
		t.Fatal("no active token expected after use")
	}
}

func TestPasswordResetExpired(t *testing.T) {
	db := openTestDB(t)
	u, _ := db.createUser("e@x.com", "oldpass12", true)
	tok, err := db.CreatePasswordResetToken(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PasswordResetToken{}).Where("token = ?", tok).
		Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.ResetPassword(tok, "newpass34"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token should be invalid: %v", err)
	}
	if db.HasActivePasswordReset(u.ID) {
		t.Fatal("expired token should not count as active")
	}
}

func TestPasswordResetInvalidToken(t *testing.T) {
	db := openTestDB(t)
	for _, tok := range []string{"nope", ""} {
		if err := db.ResetPassword(tok, "newpass34"); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("token %q should be invalid: %v", tok, err)
		}
	}
}
