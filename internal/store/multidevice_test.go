package store

import "testing"

// TestMultiDeviceNoCollision is the regression guard for the multi-device data-
// loss bug: before rows carried a device id, two agents on one account sampling
// usage/heartbeats at the same timestamp collided on (user_id, ts) and the
// second was silently dropped (OnConflict DoNothing). With device_id in the key,
// both devices' time-aligned rows must survive.
func TestMultiDeviceNoCollision(t *testing.T) {
	db := openMem(t)
	u, err := db.CreateUser("multi@x.com", "longenough1")
	if err != nil {
		t.Fatal(err)
	}
	const devA, devB uint = 1, 2

	// Both devices report usage AND a heartbeat at the exact same timestamps.
	for _, dev := range []uint{devA, devB} {
		if err := db.IngestUsage(u.ID, dev, []UsageSample{
			{TS: 1000, ActiveClass: "firefox"}, {TS: 1060, ActiveClass: "code"},
		}); err != nil {
			t.Fatalf("ingest usage dev %d: %v", dev, err)
		}
		if err := db.IngestHeartbeats(u.ID, dev, []Heartbeat{{TS: 1000, Alive: true}}); err != nil {
			t.Fatalf("ingest heartbeat dev %d: %v", dev, err)
		}
	}

	var usage, hb int64
	db.Model(&UsageSample{}).Where("user_id = ?", u.ID).Count(&usage)
	db.Model(&Heartbeat{}).Where("user_id = ?", u.ID).Count(&hb)
	if usage != 4 {
		t.Errorf("usage rows = %d, want 4 (2 devices × 2 timestamps, none dropped)", usage)
	}
	if hb != 2 {
		t.Errorf("heartbeat rows = %d, want 2 (one per device at the same ts)", hb)
	}

	// A device re-sending its own batch still dedups (idempotent backfill).
	if err := db.IngestUsage(u.ID, devA, []UsageSample{{TS: 1000, ActiveClass: "firefox"}}); err != nil {
		t.Fatal(err)
	}
	db.Model(&UsageSample{}).Where("user_id = ?", u.ID).Count(&usage)
	if usage != 4 {
		t.Errorf("usage rows after re-send = %d, want 4 (dedup within a device)", usage)
	}

	// High-water marks are per device, so a device seeds its cursor from its own
	// progress — not another device's.
	if hw := db.HighWaterMarks(u.ID, devA)["usage"]; hw != 1060 {
		t.Errorf("devA usage high-water = %d, want 1060", hw)
	}
	// A device with no rows yet starts at 0 (backfills its whole history).
	if hw := db.HighWaterMarks(u.ID, 999)["usage"]; hw != 0 {
		t.Errorf("unknown device usage high-water = %d, want 0", hw)
	}
}
