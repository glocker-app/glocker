package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glocker/internal/config"
	"glocker/internal/enforcement"
	"glocker/internal/reports"
	"glocker/internal/state"
)

func TestGetStatusResponse(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com"}, // No time windows = always blocked by default
		}},
	}

	response := GetStatusResponse(cfg)

	if !strings.Contains(response, "RUNTIME STATUS") {
		t.Error("Response should contain 'RUNTIME STATUS'")
	}

	if !strings.Contains(response, "Currently Blocked Domains") {
		t.Error("Response should contain 'Currently Blocked Domains'")
	}
}

func TestGetStatusResponse_WithExtensionKeywords(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com"}, // No time windows = always blocked by default
		}},
		Content: config.ContentConfig{Keywords: config.ExtensionKeywordsConfig{
			URLKeywords:     []string{"gambling", "casino", "poker"},
			ContentKeywords: []string{"bet", "jackpot"},
			Whitelist:       []string{"example.com", "safe.com"},
		}},
	}

	response := GetInfoResponse(cfg)

	// Should contain extension keywords section
	if !strings.Contains(response, "Extension Keywords:") {
		t.Error("Response should contain 'Extension Keywords:'")
	}

	// Should show URL keywords
	if !strings.Contains(response, "URL Keywords (3):") {
		t.Error("Response should show URL Keywords count")
	}
	if !strings.Contains(response, "gambling") || !strings.Contains(response, "casino") || !strings.Contains(response, "poker") {
		t.Error("Response should contain all URL keywords")
	}

	// Should show content keywords
	if !strings.Contains(response, "Content Keywords (2):") {
		t.Error("Response should show Content Keywords count")
	}
	if !strings.Contains(response, "bet") || !strings.Contains(response, "jackpot") {
		t.Error("Response should contain all content keywords")
	}

	// Should show whitelist count
	if !strings.Contains(response, "Whitelisted: 2 domains") {
		t.Error("Response should show whitelist count")
	}
}

func TestGetStatusResponse_WithManyKeywords(t *testing.T) {
	// Test with many keywords to verify all are shown
	manyKeywords := []string{"k1", "k2", "k3", "k4", "k5", "k6", "k7", "k8", "k9", "k10", "k11", "k12"}

	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com"}, // No time windows = always blocked by default
		}},
		Content: config.ContentConfig{Keywords: config.ExtensionKeywordsConfig{
			URLKeywords: manyKeywords,
		}},
	}

	response := GetInfoResponse(cfg)

	// Should show all keywords (no truncation)
	for _, keyword := range manyKeywords {
		if !strings.Contains(response, keyword) {
			t.Errorf("Response should contain keyword '%s'", keyword)
		}
	}

	// Should show correct count
	if !strings.Contains(response, "URL Keywords (12):") {
		t.Error("Response should show correct URL Keywords count")
	}
}

func TestProcessPanicRequest(t *testing.T) {
	cfg := &config.Config{Panic: config.PanicConfig{Enabled: true, Duration: 5}}

	// Clear panic state
	state.SetPanicUntil(time.Time{})

	// Process panic request
	ProcessPanicRequest(cfg)

	// Check that panic mode was set
	panicUntil := state.GetPanicUntil()
	if panicUntil.IsZero() {
		t.Error("Panic mode should be set")
	}

	// Verify it's approximately 5 minutes from now
	now := time.Now()
	expectedUntil := now.Add(5 * time.Minute)
	diff := panicUntil.Sub(expectedUntil).Abs()

	if diff > 5*time.Second {
		t.Errorf("Panic until time differs by %v, expected within 5 seconds", diff)
	}
}

func TestProcessUnblockRequest_RejectsPermanentDomains(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "permanent.com"},                      // Permanent by default
			{Name: "unblockable.com", Unblockable: true}, // Can be unblocked
		}, Unblocking: config.UnblockingConfig{
			TempUnblockTime: 30,
			Reasons:         []string{"work", "research"},
		}},
	}

	// Initialize enforcement state cache with test config
	// This simulates what happens during daemon startup
	enforcement.InitializeTestCache(cfg.Domains.List)

	// Clear temp unblocks
	state.SetTempUnblocks([]state.TempUnblock{})

	// Try to unblock both domains - should succeed for unblockable.com, reject permanent.com
	err := ProcessUnblockRequest(cfg, "permanent.com,unblockable.com", "work")
	if err != nil {
		t.Errorf("Should not error when at least one domain is unblockable, got: %v", err)
	}

	// Check temp unblocks
	unblocks := state.GetTempUnblocks()

	// Should only have unblockable.com
	if len(unblocks) != 1 {
		t.Errorf("Expected 1 temp unblock, got %d", len(unblocks))
	}

	if len(unblocks) > 0 && unblocks[0].Domain != "unblockable.com" {
		t.Errorf("Expected unblockable.com to be unblocked, got %s", unblocks[0].Domain)
	}

	// Verify permanent.com is NOT in temp unblocks
	for _, unblock := range unblocks {
		if unblock.Domain == "permanent.com" {
			t.Error("permanent.com should not be in temp unblocks (not marked as unblockable)")
		}
	}
}

func TestProcessUnblockRequest_AllPermanentDomainsError(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "permanent1.com"}, // Permanent by default
			{Name: "permanent2.com"}, // Permanent by default
		}, Unblocking: config.UnblockingConfig{
			TempUnblockTime: 30,
			Reasons:         []string{"work", "research"},
		}},
	}

	// Initialize enforcement state cache with test config
	enforcement.InitializeTestCache(cfg.Domains.List)

	// Clear temp unblocks
	state.SetTempUnblocks([]state.TempUnblock{})

	// Try to unblock only permanent domains - should return error
	err := ProcessUnblockRequest(cfg, "permanent1.com,permanent2.com", "work")
	if err == nil {
		t.Error("Expected error when all domains are permanently blocked")
	}

	// Verify error message mentions the domains
	if err != nil && !strings.Contains(err.Error(), "permanent1.com") {
		t.Errorf("Error should mention rejected domain, got: %v", err)
	}

	// Verify no domains were unblocked
	unblocks := state.GetTempUnblocks()
	if len(unblocks) != 0 {
		t.Errorf("Expected 0 temp unblocks, got %d", len(unblocks))
	}
}

func TestProcessUnblockRequest_EnforcesDailyCap(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "unblocks.log")
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{{Name: "example.com", Unblockable: true}}, Unblocking: config.UnblockingConfig{
			TempUnblockTime: 30,
			LogFile:         logFile,
			MaxPerDay:       2,
		}},
	}
	enforcement.InitializeTestCache(cfg.Domains.List)
	state.SetTempUnblocks([]state.TempUnblock{})

	// First two unblocks are within the cap.
	for i := 1; i <= 2; i++ {
		if err := ProcessUnblockRequest(cfg, "example.com", "work"); err != nil {
			t.Fatalf("unblock #%d should be allowed under the cap, got: %v", i, err)
		}
	}

	// Third crosses the daily cap and must be refused.
	err := ProcessUnblockRequest(cfg, "example.com", "work")
	if err == nil {
		t.Fatal("expected the third unblock to be rejected by the daily cap")
	}
	if !strings.Contains(err.Error(), "daily unblock limit") {
		t.Errorf("error should mention the daily limit, got: %v", err)
	}

	// Only two grants should have been recorded in the log.
	entries, perr := reports.ParseUnblocksLog(logFile)
	if perr != nil {
		t.Fatalf("reading unblock log: %v", perr)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 logged unblocks, got %d", len(entries))
	}
}

func TestProcessUnblockRequest_CapAppliesWithinSingleRequest(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "unblocks.log")
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "a.com", Unblockable: true},
			{Name: "b.com", Unblockable: true},
			{Name: "c.com", Unblockable: true},
		}, Unblocking: config.UnblockingConfig{
			TempUnblockTime: 30,
			LogFile:         logFile,
			MaxPerDay:       2,
		}},
	}
	enforcement.InitializeTestCache(cfg.Domains.List)
	state.SetTempUnblocks([]state.TempUnblock{})

	// A single request for three domains: two granted, the third capped. A partial
	// grant is not an error.
	if err := ProcessUnblockRequest(cfg, "a.com,b.com,c.com", "work"); err != nil {
		t.Fatalf("partial grant should not error, got: %v", err)
	}
	if got := len(state.GetTempUnblocks()); got != 2 {
		t.Errorf("expected 2 domains unblocked, got %d", got)
	}
}

func TestProcessUnblockRequest_ZeroCapIsUnlimited(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "unblocks.log")
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{{Name: "example.com", Unblockable: true}}, Unblocking: config.UnblockingConfig{
			TempUnblockTime: 30,
			LogFile:         logFile,
			MaxPerDay:       0, // unlimited
		}},
	}
	enforcement.InitializeTestCache(cfg.Domains.List)
	state.SetTempUnblocks([]state.TempUnblock{})

	for i := 1; i <= 5; i++ {
		if err := ProcessUnblockRequest(cfg, "example.com", "work"); err != nil {
			t.Fatalf("unblock #%d should be allowed when cap is 0, got: %v", i, err)
		}
	}
}

func TestGetStatusResponse_UnblocksLeft(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "unblocks.log")
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{{Name: "example.com", Unblockable: true}}, Unblocking: config.UnblockingConfig{
			TempUnblockTime: 30,
			LogFile:         logFile,
			MaxPerDay:       3,
		}},
	}
	enforcement.InitializeTestCache(cfg.Domains.List)
	state.SetTempUnblocks([]state.TempUnblock{})

	// Two unblocks today → status should read 2/3 used, 1 left.
	for i := range 2 {
		if err := ProcessUnblockRequest(cfg, "example.com", "work"); err != nil {
			t.Fatalf("unblock #%d: %v", i, err)
		}
	}

	out := GetStatusResponse(cfg)
	if !strings.Contains(out, "Unblocks Today: 2/3 used (1 left)") {
		t.Errorf("status should show 2/3 used, 1 left; got:\n%s", out)
	}
}

func TestGetStatusResponse_NoDailyCap(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{{Name: "example.com"}}, Unblocking: config.UnblockingConfig{
			// Empty (nonexistent) log so the count is a clean zero; cap 0 = unlimited.
			LogFile:   filepath.Join(t.TempDir(), "unblocks.log"),
			MaxPerDay: 0,
		}},
	}
	enforcement.InitializeTestCache(cfg.Domains.List)
	state.SetTempUnblocks([]state.TempUnblock{})

	out := GetStatusResponse(cfg)
	if !strings.Contains(out, "Unblocks Today: 0 (no daily cap)") {
		t.Errorf("status should show no daily cap; got:\n%s", out)
	}
}

func TestProcessUnblockRequest_ValidatesReasons(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com", Unblockable: true}, // Must be unblockable to test reason validation
		}, Unblocking: config.UnblockingConfig{
			TempUnblockTime: 30,
			Reasons:         []string{"work", "research", "emergency"},
		}},
	}

	// Initialize enforcement state cache with test config
	enforcement.InitializeTestCache(cfg.Domains.List)

	tests := []struct {
		name        string
		reason      string
		shouldError bool
	}{
		{"valid reason - work", "work", false},
		{"valid reason - research", "research", false},
		{"valid reason - emergency", "emergency", false},
		{"valid reason - case insensitive", "WORK", false},
		{"invalid reason", "fun", true},
		{"invalid reason - empty", "", true},
		{"invalid reason - random", "just because", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear temp unblocks before each test
			state.SetTempUnblocks([]state.TempUnblock{})

			err := ProcessUnblockRequest(cfg, "example.com", tt.reason)

			if tt.shouldError {
				if err == nil {
					t.Errorf("Expected error for reason '%s', but got nil", tt.reason)
				}
				// Verify domain was NOT unblocked
				unblocks := state.GetTempUnblocks()
				if len(unblocks) > 0 {
					t.Errorf("Domain should not be unblocked with invalid reason, but got %d unblocks", len(unblocks))
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error for reason '%s', but got: %v", tt.reason, err)
				}
				// Verify domain WAS unblocked
				unblocks := state.GetTempUnblocks()
				if len(unblocks) != 1 {
					t.Errorf("Expected 1 unblock for valid reason '%s', got %d", tt.reason, len(unblocks))
				}
			}
		})
	}
}

func TestProcessUnblockRequest_NoReasonValidationWhenListEmpty(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{Name: "example.com", Unblockable: true}, // Must be unblockable
		}, Unblocking: config.UnblockingConfig{
			TempUnblockTime: 30,
			Reasons:         []string{}, // Empty list - any reason should be accepted
		}},
	}

	// Initialize enforcement state cache with test config
	enforcement.InitializeTestCache(cfg.Domains.List)

	// Clear temp unblocks
	state.SetTempUnblocks([]state.TempUnblock{})

	// Try to unblock with any reason (should work when list is empty)
	err := ProcessUnblockRequest(cfg, "example.com", "any reason at all")
	if err != nil {
		t.Errorf("Expected no error when reasons list is empty, got: %v", err)
	}

	// Verify domain was unblocked
	unblocks := state.GetTempUnblocks()
	if len(unblocks) != 1 {
		t.Errorf("Expected 1 unblock, got %d", len(unblocks))
	}
}

// extendTestConfig returns a Config with one extendible program and one
// non-extendible one, plus a temp extensions persistence path.
func extendTestConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	orig := state.ProgramExtensionsPath
	state.ProgramExtensionsPath = dir + "/extensions.json"
	state.ClearProgramExtensions()
	t.Cleanup(func() {
		state.ProgramExtensionsPath = orig
		state.ClearProgramExtensions()
	})

	return &config.Config{
		Programs: config.ProgramsConfig{
			Enabled: true,
			List: []config.ForbiddenProgram{
				{Name: "firefox-esr", Extendible: true},
				{Name: "chromium"}, // not extendible
			},
		},
	}
}

func TestProcessExtendRequest_GrantsActiveExtension(t *testing.T) {
	cfg := extendTestConfig(t)

	if err := ProcessExtendRequest(cfg, "firefox-esr", "client call"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := state.GetActiveExtension("firefox-esr")
	if !ok {
		t.Fatal("expected active extension after grant")
	}
	if got.Reason != "client call" {
		t.Errorf("got reason %q, want %q", got.Reason, "client call")
	}
}

func TestProcessExtendRequest_RejectsUnknownProgram(t *testing.T) {
	cfg := extendTestConfig(t)

	err := ProcessExtendRequest(cfg, "vim", "work")
	if err == nil {
		t.Fatal("expected error for unknown program")
	}
	if !strings.Contains(err.Error(), "no forbidden program") {
		t.Errorf("error should mention unknown program, got: %v", err)
	}
}

func TestProcessExtendRequest_RejectsNonExtendibleProgram(t *testing.T) {
	cfg := extendTestConfig(t)

	err := ProcessExtendRequest(cfg, "chromium", "client call")
	if err == nil {
		t.Fatal("expected error for non-extendible program")
	}
	if !strings.Contains(err.Error(), "extendible") {
		t.Errorf("error should mention extendible, got: %v", err)
	}
}

func TestProcessExtendRequest_RequiresExactNameMatch(t *testing.T) {
	cfg := extendTestConfig(t)

	// Case mismatch — should not match the `name:` field.
	err := ProcessExtendRequest(cfg, "FIREFOX-ESR", "client call")
	if err == nil {
		t.Fatal("expected error for case-mismatched program name")
	}
}

func TestProcessExtendRequest_RequiresReason(t *testing.T) {
	cfg := extendTestConfig(t)

	err := ProcessExtendRequest(cfg, "firefox-esr", "   ")
	if err == nil {
		t.Fatal("expected error for empty reason")
	}
}

func TestProcessExtendRequest_EnforcesCooldown(t *testing.T) {
	cfg := extendTestConfig(t)

	if err := ProcessExtendRequest(cfg, "firefox-esr", "first call"); err != nil {
		t.Fatalf("first grant failed: %v", err)
	}

	err := ProcessExtendRequest(cfg, "firefox-esr", "second call")
	if err == nil {
		t.Fatal("expected cooldown error on second grant within 24h")
	}
	if !strings.Contains(err.Error(), "cooldown") {
		t.Errorf("error should mention cooldown, got: %v", err)
	}
}

func TestGetStatusResponse_ShowsActiveProgramExtension(t *testing.T) {
	cfg := extendTestConfig(t)

	if err := ProcessExtendRequest(cfg, "firefox-esr", "client call"); err != nil {
		t.Fatalf("ProcessExtendRequest: %v", err)
	}

	resp := GetStatusResponse(cfg)
	if !strings.Contains(resp, "Program Extensions: 1 active") {
		t.Errorf("status missing extension count line:\n%s", resp)
	}
	if !strings.Contains(resp, "firefox-esr") {
		t.Errorf("status missing extended program name:\n%s", resp)
	}
	if !strings.Contains(resp, "client call") {
		t.Errorf("status missing extension reason:\n%s", resp)
	}
}

func TestGetStatusResponse_ZeroExtensionsLineAlwaysPresent(t *testing.T) {
	extendTestConfig(t) // resets state to zero extensions
	cfg := &config.Config{}

	resp := GetStatusResponse(cfg)
	if !strings.Contains(resp, "Program Extensions: 0 active") {
		t.Errorf("expected zero-count line in status, got:\n%s", resp)
	}
	if strings.Contains(resp, "Active program extensions:") {
		t.Errorf("did not expect active-extensions subsection when none active:\n%s", resp)
	}
}

func TestGetStatusResponse_ShowsRecentViolationDetails(t *testing.T) {
	state.ClearViolations()
	t.Cleanup(state.ClearViolations)

	cfg := &config.Config{
		ViolationTracking: config.ViolationTrackingConfig{
			Enabled:           true,
			MaxViolations:     5,
			TimeWindowMinutes: 60,
		},
	}

	state.AddViolation(state.Violation{
		Timestamp: time.Now().Add(-5 * time.Minute),
		Host:      "youtube.com",
		URL:       "https://youtube.com/watch?v=abc",
		Type:      "web_access",
	})
	state.AddViolation(state.Violation{
		Timestamp: time.Now().Add(-1 * time.Minute),
		Host:      "reddit.com",
		Type:      "content_report",
	})

	resp := GetStatusResponse(cfg)
	if !strings.Contains(resp, "Recent details:") {
		t.Errorf("status missing Recent details section:\n%s", resp)
	}
	if !strings.Contains(resp, "youtube.com") || !strings.Contains(resp, "reddit.com") {
		t.Errorf("status missing violation hosts:\n%s", resp)
	}
	if !strings.Contains(resp, "web_access") || !strings.Contains(resp, "content_report") {
		t.Errorf("status missing violation types:\n%s", resp)
	}
}

func TestGetStatusResponse_NoViolationDetailsWhenNoneRecent(t *testing.T) {
	state.ClearViolations()
	t.Cleanup(state.ClearViolations)

	cfg := &config.Config{
		ViolationTracking: config.ViolationTrackingConfig{
			Enabled:           true,
			MaxViolations:     5,
			TimeWindowMinutes: 60,
		},
	}

	// Stale violation outside the recent window should not appear in
	// details, even though it inflates Total Violations.
	state.AddViolation(state.Violation{
		Timestamp: time.Now().Add(-2 * time.Hour),
		Host:      "old.com",
		Type:      "web_access",
	})

	resp := GetStatusResponse(cfg)
	if strings.Contains(resp, "Recent details:") {
		t.Errorf("status should not include Recent details when no recent violations:\n%s", resp)
	}
	if strings.Contains(resp, "old.com") {
		t.Errorf("status should not list stale violation host:\n%s", resp)
	}
}

func TestGetStatusResponse_TruncatesViolationDetails(t *testing.T) {
	state.ClearViolations()
	t.Cleanup(state.ClearViolations)

	cfg := &config.Config{
		ViolationTracking: config.ViolationTrackingConfig{
			Enabled:           true,
			MaxViolations:     20,
			TimeWindowMinutes: 60,
		},
	}

	// Add newest-first (site0 = 1 min ago, site6 = 7 min ago). This deliberately
	// scrambles insertion order vs. timestamp order — the way concurrent
	// RecordViolation callers can — so the test fails if the display trusts slice
	// order instead of sorting by time.
	for i := range 7 {
		state.AddViolation(state.Violation{
			Timestamp: time.Now().Add(-time.Duration(i+1) * time.Minute),
			Host:      fmt.Sprintf("site%d.com", i),
			Type:      "web_access",
		})
	}

	resp := GetStatusResponse(cfg)
	if !strings.Contains(resp, "... and 2 more") {
		t.Errorf("expected truncation line for 7 violations (cap 5):\n%s", resp)
	}
	// The 5 most recent (site0..site4) must be shown; the 2 oldest (site5, site6)
	// must be the ones truncated.
	for i := 0; i < 5; i++ {
		if !strings.Contains(resp, fmt.Sprintf("site%d.com", i)) {
			t.Errorf("expected recent violation site%d.com to be shown:\n%s", i, resp)
		}
	}
	for _, old := range []string{"site5.com", "site6.com"} {
		if strings.Contains(resp, old) {
			t.Errorf("oldest violation %s should have been truncated, not shown:\n%s", old, resp)
		}
	}
}
