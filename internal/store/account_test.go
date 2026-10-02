package store

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// A stale unverified registration can be reclaimed by the real address owner; a
// fresh pending one and a verified one cannot (defeats account pre-hijacking).
func TestRegisterUser_PreHijack(t *testing.T) {
	db := openTestDB(t)

	u1, tok1, err := db.RegisterUser("v@x.com", "pw123456")
	if err != nil {
		t.Fatal(err)
	}
	if u1.Verified {
		t.Fatal("a new registration must be unverified")
	}

	// A still-fresh pending registration is held — not overwritten.
	if _, _, err := db.RegisterUser("v@x.com", "attacker9"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("re-register within TTL: got %v, want ErrEmailTaken", err)
	}
	// The original token still activates the original credentials.
	if _, err := db.VerifyEmail(tok1); err != nil {
		t.Fatalf("original token should still verify: %v", err)
	}
	// A verified account owns the address for good.
	if _, _, err := db.RegisterUser("v@x.com", "pw123456"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("re-register verified: got %v, want ErrEmailTaken", err)
	}

	// A stale unverified account (past the link lifetime) is reclaimable.
	u2, _, err := db.RegisterUser("s@x.com", "pw123456")
	if err != nil {
		t.Fatal(err)
	}
	db.DB.Model(&User{}).Where("id = ?", u2.ID).Update("created_at", time.Now().Add(-VerificationTTL-time.Hour))
	u3, tok3, err := db.RegisterUser("s@x.com", "newpass1")
	if err != nil {
		t.Fatalf("reclaim stale unverified: %v", err)
	}
	if u3.ID == u2.ID {
		t.Error("the stale account should have been replaced, not reused")
	}
	if tok3 == "" {
		t.Error("expected a fresh verification token")
	}
}

// DeleteUserData must remove every user-scoped table and leave system-owned rows
// (Glocker:* tags, user_id 0) intact.
func TestDeleteUserData_RemovesAllUserScoped(t *testing.T) {
	db := openTestDB(t)
	u, err := db.CreateUser("del@x.com", "pw123456") // verified; seeds rules
	if err != nil {
		t.Fatal(err)
	}
	uid := u.ID

	if err := db.SetVault(uid, `{"v":1}`); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveDeviceConfig(uid, 1, DeviceConfig{UserID: uid, DeviceID: 1, Config: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.Create(&AccountabilityConfig{UserID: uid, Enabled: true, BuddyEmails: `["b@x.com"]`}).Error; err != nil {
		t.Fatal(err)
	}
	utag, err := db.CreateUserTag(uid, "Activity", "work", "#3aa0ff", false)
	if err != nil {
		t.Fatal(err)
	}
	sys := Tag{Scope: "system", UserID: 0, Namespace: "Glocker", Name: "distraction-test"}
	if err := db.DB.Create(&sys).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.DB.Create(&TagOverride{UserID: uid, TagID: sys.ID, Distraction: true}).Error; err != nil {
		t.Fatal(err)
	}
	rule := Rule{UserID: uid, Position: 99, Program: "z", Title: "Z"}
	if err := db.DB.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.DB.Create(&RuleTag{RuleID: rule.ID, TagID: utag.ID}).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteUserData(uid); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := db.UserByID(uid); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("user row should be gone, got err=%v", err)
	}
	for _, c := range []struct {
		name  string
		model any
	}{
		{"keyvault", &KeyVault{}}, {"deviceconfig", &DeviceConfig{}},
		{"accountability", &AccountabilityConfig{}}, {"tag", &Tag{}}, {"tagoverride", &TagOverride{}},
		{"rule", &Rule{}}, {"apitoken", &APIToken{}}, {"session", &Session{}},
	} {
		var n int64
		db.DB.Model(c.model).Where("user_id = ?", uid).Count(&n)
		if n != 0 {
			t.Errorf("%s: %d rows left for the deleted user", c.name, n)
		}
	}
	var rt int64
	db.DB.Model(&RuleTag{}).Where("rule_id = ?", rule.ID).Count(&rt)
	if rt != 0 {
		t.Errorf("ruletag: %d rows left for the deleted user's rule", rt)
	}
	var sysN int64
	db.DB.Model(&Tag{}).Where("id = ?", sys.ID).Count(&sysN)
	if sysN != 1 {
		t.Errorf("system tag should survive, found %d", sysN)
	}
}
