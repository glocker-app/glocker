package enforcement

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
)

func TestUpdateHostsWritesGlockerLocalAlias(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Domains: config.DomainsConfig{HostsPath: hostsPath}}
	if err := UpdateHosts(cfg, []string{"example.com"}, false); err != nil {
		t.Fatalf("UpdateHosts: %v", err)
	}
	got, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	// The special-cased dashboard alias is always present in the managed block.
	if !strings.Contains(s, "127.0.0.1 glocker.localhost") || !strings.Contains(s, "::1 glocker.localhost") {
		t.Errorf("missing glocker.localhost alias:\n%s", s)
	}
	// Original content and the blocked domain survive alongside it.
	if !strings.Contains(s, "127.0.0.1 localhost") || !strings.Contains(s, "127.0.0.1 example.com") {
		t.Errorf("missing expected host entries:\n%s", s)
	}
}

func TestGetDomainsToBlock_AlwaysBlock(t *testing.T) {
	// NEW BEHAVIOR: Domains without time windows are always blocked by default
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com"}, // No time windows = always blocked
			{Name: "test.com", BlockWindows: []config.TimeWindow{
				{Start: "09:00", End: "17:00", Days: []string{"Sun"}}, // Wrong day, won't block
			}},
		}},
	}

	now := time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC) // Monday 10:00
	blocked := GetDomainsToBlock(cfg, now)

	if len(blocked) != 1 {
		t.Fatalf("Expected 1 blocked domain, got %d", len(blocked))
	}
	if blocked[0] != "example.com" {
		t.Errorf("Expected example.com to be blocked, got %s", blocked[0])
	}
}

func TestGetDomainsToBlock_TimeWindows(t *testing.T) {
	// Test with a time window that is currently active
	now := time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC) // Monday 10:00
	currentDay := now.Weekday().String()[:3]            // "Mon"

	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{
				Name: "worksite.com",
				BlockWindows: []config.TimeWindow{
					{
						Start: "09:00",
						End:   "17:00",
						Days:  []string{currentDay},
					},
				},
			},
		}},
	}

	blocked := GetDomainsToBlock(cfg, now)

	if len(blocked) != 1 {
		t.Fatalf("Expected 1 blocked domain, got %d", len(blocked))
	}
	if blocked[0] != "worksite.com" {
		t.Errorf("Expected worksite.com to be blocked, got %s", blocked[0])
	}
}

func TestGetDomainsToBlock_OutsideTimeWindow(t *testing.T) {
	// Test with a time that's outside the window
	now := time.Date(2026, 1, 6, 18, 0, 0, 0, time.UTC) // Monday 18:00 (6 PM)
	currentDay := now.Weekday().String()[:3]

	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{
				Name: "worksite.com",
				BlockWindows: []config.TimeWindow{
					{
						Start: "09:00",
						End:   "17:00",
						Days:  []string{currentDay},
					},
				},
			},
		}},
	}

	blocked := GetDomainsToBlock(cfg, now)

	if len(blocked) != 0 {
		t.Errorf("Expected 0 blocked domains outside time window, got %d", len(blocked))
	}
}

func TestGetDomainsToBlock_WrongDay(t *testing.T) {
	now := time.Date(2026, 1, 7, 10, 0, 0, 0, time.UTC) // Tuesday 10:00
	currentDay := now.Weekday().String()[:3]            // "Tue"

	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{
				Name: "worksite.com",
				BlockWindows: []config.TimeWindow{
					{
						Start: "09:00",
						End:   "17:00",
						Days:  []string{"Mon"}, // Only Monday
					},
				},
			},
		}},
	}

	blocked := GetDomainsToBlock(cfg, now)

	if len(blocked) != 0 {
		t.Errorf("Expected 0 blocked domains on wrong day, got %d (day=%s)", len(blocked), currentDay)
	}
}

func TestIsTempUnblocked(t *testing.T) {
	// Clear any existing temp unblocks
	state.SetTempUnblocks([]state.TempUnblock{})

	now := time.Now()
	domain := "example.com"

	// Initially not unblocked
	if IsTempUnblocked(domain, now) {
		t.Error("Domain should not be temporarily unblocked initially")
	}

	// Add temporary unblock
	state.AddTempUnblock(domain, now.Add(30*time.Minute))

	// Should be unblocked now
	if !IsTempUnblocked(domain, now) {
		t.Error("Domain should be temporarily unblocked after adding")
	}

	// Should not be unblocked if time has passed
	future := now.Add(35 * time.Minute)
	if IsTempUnblocked(domain, future) {
		t.Error("Domain should not be unblocked after expiration")
	}

	// Clean up
	state.SetTempUnblocks([]state.TempUnblock{})
}

func TestPermanentDomainIgnoresTempUnblock(t *testing.T) {
	// Clear any existing temp unblocks
	state.SetTempUnblocks([]state.TempUnblock{})

	now := time.Now()

	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "permanent.com"},                      // No time windows, permanent (default)
			{Name: "unblockable.com", Unblockable: true}, // No time windows, but unblockable
		}},
	}

	// Add temp unblocks for both domains
	state.AddTempUnblock("permanent.com", now.Add(30*time.Minute))
	state.AddTempUnblock("unblockable.com", now.Add(30*time.Minute))

	// Get domains to block
	blocked := GetDomainsToBlock(cfg, now)

	// permanent.com should still be blocked (ignores temp unblock - not unblockable)
	foundPermanent := false
	foundUnblockable := false
	for _, domain := range blocked {
		if domain == "permanent.com" {
			foundPermanent = true
		}
		if domain == "unblockable.com" {
			foundUnblockable = true
		}
	}

	if !foundPermanent {
		t.Error("permanent.com should be blocked even with temp unblock (not marked as unblockable)")
	}

	if foundUnblockable {
		t.Error("unblockable.com should not be blocked with temp unblock (marked as unblockable)")
	}

	// Clean up
	state.SetTempUnblocks([]state.TempUnblock{})
}

func TestCleanupExpiredUnblocks(t *testing.T) {
	// Clear any existing temp unblocks
	state.SetTempUnblocks([]state.TempUnblock{})

	now := time.Now()

	// Add some temp unblocks - one expired, one active
	state.AddTempUnblock("expired.com", now.Add(-10*time.Minute))
	state.AddTempUnblock("active.com", now.Add(30*time.Minute))

	// Cleanup
	CleanupExpiredUnblocks(now)

	// Check remaining unblocks
	unblocks := state.GetTempUnblocks()
	if len(unblocks) != 1 {
		t.Fatalf("Expected 1 active unblock after cleanup, got %d", len(unblocks))
	}
	if unblocks[0].Domain != "active.com" {
		t.Errorf("Expected active.com to remain, got %s", unblocks[0].Domain)
	}

	// Clean up
	state.SetTempUnblocks([]state.TempUnblock{})
}

func TestGetBlockingReason_AlwaysBlock(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com"}, // No time windows = always blocked (permanent by default)
		}},
	}

	now := time.Now()
	reason := GetBlockingReason(cfg, "example.com", now)

	if reason != "always blocked (permanent)" {
		t.Errorf("Expected 'always blocked (permanent)', got '%s'", reason)
	}
}

func TestGetBlockingReason_Permanent(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com"}, // No time windows, not unblockable = permanent
		}},
	}

	now := time.Now()
	reason := GetBlockingReason(cfg, "example.com", now)

	expected := "always blocked (permanent)"
	if reason != expected {
		t.Errorf("Expected '%s', got '%s'", expected, reason)
	}
}

func TestGetBlockingReason_Unblockable(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com", Unblockable: true}, // No time windows, but unblockable
		}},
	}

	now := time.Now()
	reason := GetBlockingReason(cfg, "example.com", now)

	expected := "always blocked (can be temporarily unblocked)"
	if reason != expected {
		t.Errorf("Expected '%s', got '%s'", expected, reason)
	}
}

func TestGetBlockingReason_TimeWindow(t *testing.T) {
	now := time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC) // Monday 10:00
	currentDay := now.Weekday().String()[:3]

	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{
				Name: "worksite.com",
				BlockWindows: []config.TimeWindow{
					{
						Start: "09:00",
						End:   "17:00",
						Days:  []string{currentDay},
					},
				},
			},
		}},
	}

	reason := GetBlockingReason(cfg, "worksite.com", now)

	if !strings.Contains(reason, "time-based block") {
		t.Errorf("Expected time-based block reason, got '%s'", reason)
	}
	if !strings.Contains(reason, "09:00-17:00") {
		t.Errorf("Expected time window in reason, got '%s'", reason)
	}
}

func TestGetBlockingReason_Unknown(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com"}, // Some existing domain
		}},
	}

	now := time.Now()
	reason := GetBlockingReason(cfg, "nonexistent.com", now)

	if reason != "unknown blocking rule" {
		t.Errorf("Expected 'unknown blocking rule', got '%s'", reason)
	}
}

func TestIsSudoAllowed_Enabled(t *testing.T) {
	now := time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC) // Monday 10:00
	currentDay := now.Weekday().String()[:3]

	cfg := &config.Config{
		Sudoers: config.SudoersConfig{
			Enabled: true,
			AllowWindows: []config.TimeWindow{
				{
					Start: "09:00",
					End:   "17:00",
					Days:  []string{currentDay},
				},
			},
		},
	}

	if !IsSudoAllowed(cfg, now) {
		t.Error("Sudo should be allowed during configured time window")
	}

	// Test outside window
	laterTime := time.Date(2026, 1, 6, 18, 0, 0, 0, time.UTC) // Monday 18:00
	if IsSudoAllowed(cfg, laterTime) {
		t.Error("Sudo should not be allowed outside configured time window")
	}
}

func TestIsSudoAllowed_Disabled(t *testing.T) {
	cfg := &config.Config{
		Sudoers: config.SudoersConfig{
			Enabled: false,
		},
	}

	now := time.Now()
	if !IsSudoAllowed(cfg, now) {
		t.Error("Sudo should always be allowed when sudoers management is disabled")
	}
}

func TestGetDomainsToBlock_MultipleConditions(t *testing.T) {
	now := time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC) // Monday 10:00
	currentDay := now.Weekday().String()[:3]

	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "always.com"}, // No time windows = always blocked
			{
				Name: "timewindow.com",
				BlockWindows: []config.TimeWindow{
					{Start: "09:00", End: "17:00", Days: []string{currentDay}},
				},
			},
			{
				Name: "outside.com",
				BlockWindows: []config.TimeWindow{
					{Start: "18:00", End: "22:00", Days: []string{currentDay}}, // Outside current time
				},
			},
			{
				Name: "wrongday.com",
				BlockWindows: []config.TimeWindow{
					{Start: "09:00", End: "17:00", Days: []string{"Sun"}},
				},
			},
		}},
	}

	blocked := GetDomainsToBlock(cfg, now)

	// Should block: always.com (no time windows) and timewindow.com (in window)
	// Should NOT block: outside.com (outside time window) and wrongday.com (wrong day)
	if len(blocked) != 2 {
		t.Fatalf("Expected 2 blocked domains, got %d", len(blocked))
	}

	blockedMap := make(map[string]bool)
	for _, d := range blocked {
		blockedMap[d] = true
	}

	if !blockedMap["always.com"] {
		t.Error("always.com should be blocked (no time windows)")
	}
	if !blockedMap["timewindow.com"] {
		t.Error("timewindow.com should be blocked (in time window)")
	}
	if blockedMap["outside.com"] {
		t.Error("outside.com should not be blocked (outside time window)")
	}
	if blockedMap["wrongday.com"] {
		t.Error("wrongday.com should not be blocked (wrong day)")
	}
}
