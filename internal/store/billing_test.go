package store

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestSetSubscription(t *testing.T) {
	db := openMem(t)
	u, err := db.CreateUser("sub@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}

	end := time.Now().Add(30 * 24 * time.Hour).Round(time.Second)

	// Grant: active subscription. The device cap stays the flat default — billing
	// no longer touches device_limit.
	if err := db.SetSubscription(u.ID, SubStatusActive, "sub_123", end, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _ := db.UserByEmail("sub@x.com")
	if got.SubStatus != SubStatusActive || got.SubID != "sub_123" {
		t.Errorf("status/id = %q/%q, want active/sub_123", got.SubStatus, got.SubID)
	}
	if EffectiveDeviceLimit(got) != DefaultDeviceCap {
		t.Errorf("device limit = %d, want %d (flat default)", EffectiveDeviceLimit(got), DefaultDeviceCap)
	}

	// An admin per-account override must survive subscription writes.
	if err := db.SetDeviceLimit("sub@x.com", 9); err != nil {
		t.Fatal(err)
	}

	// Idempotent: replaying the same grant leaves the same state.
	if err := db.SetSubscription(u.ID, SubStatusActive, "sub_123", end, time.Time{}); err != nil {
		t.Fatal(err)
	}

	// Revoke: status flips to expired; the admin override is left intact.
	if err := db.SetSubscription(u.ID, SubStatusExpired, "sub_123", end, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.UserByEmail("sub@x.com")
	if got.SubStatus != SubStatusExpired {
		t.Errorf("status = %q, want expired", got.SubStatus)
	}
	if EffectiveDeviceLimit(got) != 9 {
		t.Errorf("device limit after revoke = %d, want 9 (admin override preserved)", EffectiveDeviceLimit(got))
	}

	// Unknown user is an error, not a silent no-op.
	if err := db.SetSubscription(99999, SubStatusActive, "x", end, time.Time{}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("unknown user err = %v, want ErrRecordNotFound", err)
	}
}

func TestHasAccessTrial(t *testing.T) {
	future := time.Now().Add(7 * 24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	cases := []struct {
		name string
		u    User
		want bool
	}{
		{"no sub, no trial", User{}, false},
		{"trial in future", User{TrialEndsAt: &future}, true},
		{"trial lapsed", User{TrialEndsAt: &past}, false},
		{"subscribed, trial field ignored", User{SubStatus: SubStatusActive, TrialEndsAt: &past}, true},
		{"expired sub still in trial window is read-only", User{SubStatus: SubStatusExpired, TrialEndsAt: &future}, false},
	}
	for _, c := range cases {
		if got := c.u.HasAccess(); got != c.want {
			t.Errorf("%s: HasAccess()=%v, want %v", c.name, got, c.want)
		}
	}
}
