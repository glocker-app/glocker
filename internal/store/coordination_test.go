package store

import (
	"testing"
	"time"
)

func TestSkipMigrate(t *testing.T) {
	dsn := t.TempDir() + "/t.db"
	// Opened with SkipMigrate: tables aren't created, so a query fails.
	db, err := Open(Options{Driver: "sqlite", DSN: dsn, SkipMigrate: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.RateAllow("k", time.Hour, 1, time.Now()); err == nil {
		t.Fatal("expected a query to fail before Migrate() creates the tables")
	}
	// Explicit Migrate() then makes it work.
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.RateAllow("k", time.Hour, 1, time.Now()); err != nil {
		t.Errorf("after Migrate: %v", err)
	}
	_ = db.Close()
}

func TestRateAllow(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	// limit 2 per window: two allowed, third denied.
	for i, want := range []bool{true, true, false} {
		got, err := db.RateAllow("login:1.2.3.4", time.Hour, 2, now)
		if err != nil {
			t.Fatalf("RateAllow %d: %v", i, err)
		}
		if got != want {
			t.Errorf("RateAllow call %d = %v, want %v", i+1, got, want)
		}
	}
	// A different key has an independent counter.
	if ok, _ := db.RateAllow("login:9.9.9.9", time.Hour, 2, now); !ok {
		t.Error("different key should have its own counter")
	}
}

func TestCaptchaUseOnce(t *testing.T) {
	db := openTestDB(t)
	exp := time.Now().Add(time.Minute)
	if first, err := db.CaptchaUseOnce("salt-abc", exp); err != nil || !first {
		t.Fatalf("first use = %v (err %v), want true", first, err)
	}
	if replay, err := db.CaptchaUseOnce("salt-abc", exp); err != nil || replay {
		t.Errorf("replay = %v (err %v), want false", replay, err)
	}
}

func TestSchedulerLease(t *testing.T) {
	db := openTestDB(t)
	// A takes it; B is refused while A holds a live lease; A can renew.
	if ok, _ := db.TryAcquireSchedulerLease("A", time.Hour); !ok {
		t.Fatal("A should acquire the lease")
	}
	if ok, _ := db.TryAcquireSchedulerLease("B", time.Hour); ok {
		t.Fatal("B must not acquire while A holds a live lease")
	}
	if ok, _ := db.TryAcquireSchedulerLease("A", time.Hour); !ok {
		t.Fatal("A should renew its own lease")
	}
	// A's lease lapses → B takes over.
	if ok, _ := db.TryAcquireSchedulerLease("A", time.Millisecond); !ok {
		t.Fatal("A renew with short lease")
	}
	time.Sleep(5 * time.Millisecond)
	if ok, _ := db.TryAcquireSchedulerLease("B", time.Hour); !ok {
		t.Error("B should take over an expired lease")
	}
}
