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

	// Grant: active subscription with an unlimited paid cap.
	if err := db.SetSubscription(u.ID, SubStatusActive, "sub_123", end, -1); err != nil {
		t.Fatal(err)
	}
	got, _ := db.UserByEmail("sub@x.com")
	if got.SubStatus != SubStatusActive || got.SubID != "sub_123" {
		t.Errorf("status/id = %q/%q, want active/sub_123", got.SubStatus, got.SubID)
	}
	if EffectiveDeviceLimit(got) != -1 {
		t.Errorf("device limit = %d, want -1 (unlimited)", EffectiveDeviceLimit(got))
	}

	// Idempotent: replaying the same grant leaves the same state.
	if err := db.SetSubscription(u.ID, SubStatusActive, "sub_123", end, -1); err != nil {
		t.Fatal(err)
	}
	got, _ = db.UserByEmail("sub@x.com")
	if EffectiveDeviceLimit(got) != -1 {
		t.Errorf("device limit after replay = %d, want -1", EffectiveDeviceLimit(got))
	}

	// Revoke: expired drops the cap back to the free tier (0 -> DefaultFreeDevices).
	if err := db.SetSubscription(u.ID, SubStatusExpired, "sub_123", end, 0); err != nil {
		t.Fatal(err)
	}
	got, _ = db.UserByEmail("sub@x.com")
	if got.SubStatus != SubStatusExpired {
		t.Errorf("status = %q, want expired", got.SubStatus)
	}
	if EffectiveDeviceLimit(got) != DefaultFreeDevices {
		t.Errorf("device limit after revoke = %d, want %d (free)", EffectiveDeviceLimit(got), DefaultFreeDevices)
	}

	// Unknown user is an error, not a silent no-op.
	if err := db.SetSubscription(99999, SubStatusActive, "x", end, -1); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("unknown user err = %v, want ErrRecordNotFound", err)
	}
}
