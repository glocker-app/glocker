package monitoring

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
)

func TestCaptureChecksum(t *testing.T) {
	// Create a temporary file
	tmpFile, err := os.CreateTemp("", "test-checksum-*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	content := []byte("test content\n")
	if _, err := tmpFile.Write(content); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	cfg := &config.Config{
		Domains: config.DomainsConfig{HostsPath: "/etc/hosts"}, // Not the test file
	}

	// Capture checksum
	checksum := CaptureChecksum(cfg, tmpFile.Name())

	if !checksum.Exists {
		t.Error("Expected file to exist")
	}
	if checksum.Checksum == "" {
		t.Error("Expected non-empty checksum")
	}
	if checksum.Path != tmpFile.Name() {
		t.Errorf("Expected path %s, got %s", tmpFile.Name(), checksum.Path)
	}
}

func TestCaptureChecksum_NonExistent(t *testing.T) {
	cfg := &config.Config{}
	checksum := CaptureChecksum(cfg, "/nonexistent/file.txt")

	if checksum.Exists {
		t.Error("Expected file to not exist")
	}
	if checksum.Checksum != "" {
		t.Error("Expected empty checksum for non-existent file")
	}
}

func TestExtractGlockerSection(t *testing.T) {
	content := `127.0.0.1 localhost
127.0.1.1 myhost

### GLOCKER START ###
127.0.0.1 blocked.com
127.0.0.1 www.blocked.com
`

	section := ExtractGlockerSection(content)

	if !strings.Contains(section, "GLOCKER START") {
		t.Error("Expected glocker section to contain marker")
	}
	if !strings.Contains(section, "blocked.com") {
		t.Error("Expected glocker section to contain blocked domains")
	}
	if strings.Contains(section, "localhost") {
		t.Error("Expected glocker section to NOT contain non-glocker content")
	}
}

func TestRecordViolation(t *testing.T) {
	// Clear violations
	state.ClearViolations()

	cfg := &config.Config{
		ViolationTracking: config.ViolationTrackingConfig{
			Enabled:           true,
			MaxViolations:     5,
			TimeWindowMinutes: 60,
			Command:           "",
		},
	}

	// Record a violation
	RecordViolation(cfg, "web_access", "example.com", "https://example.com")

	// Check that it was recorded
	violations := state.GetViolations()
	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	if violations[0].Host != "example.com" {
		t.Errorf("Expected host example.com, got %s", violations[0].Host)
	}
	if violations[0].Type != "web_access" {
		t.Errorf("Expected type web_access, got %s", violations[0].Type)
	}

	// Clean up
	state.ClearViolations()
}

func TestCountRecentViolations(t *testing.T) {
	// Clear violations
	state.ClearViolations()

	cfg := &config.Config{
		ViolationTracking: config.ViolationTrackingConfig{
			Enabled:           true,
			TimeWindowMinutes: 60,
		},
	}

	now := time.Now()

	// Add some violations - some recent, some old
	state.AddViolation(state.Violation{
		Timestamp: now.Add(-10 * time.Minute), // Recent
		Host:      "recent1.com",
		Type:      "web_access",
	})
	state.AddViolation(state.Violation{
		Timestamp: now.Add(-30 * time.Minute), // Recent
		Host:      "recent2.com",
		Type:      "web_access",
	})
	state.AddViolation(state.Violation{
		Timestamp: now.Add(-90 * time.Minute), // Old (outside 60 min window)
		Host:      "old.com",
		Type:      "web_access",
	})

	count := countRecentViolations(cfg, now)

	if count != 2 {
		t.Errorf("Expected 2 recent violations, got %d", count)
	}

	// Clean up
	state.ClearViolations()
}

func TestShouldKill_ActiveExtensionKeepsExtendibleProgramAlive(t *testing.T) {
	orig := state.ProgramExtensionsPath
	state.ProgramExtensionsPath = t.TempDir() + "/extensions.json"
	state.ClearProgramExtensions()
	t.Cleanup(func() {
		state.ProgramExtensionsPath = orig
		state.ClearProgramExtensions()
	})

	// Window covers the whole week, so isProgramForbidden returns true
	// regardless of when the test runs.
	prog := config.ForbiddenProgram{
		Name:       "firefox-esr",
		Extendible: true,
		KillWindows: []config.TimeWindow{
			{Start: "00:00", End: "23:59", Days: []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}},
		},
	}

	now := time.Now()
	currentDay := now.Weekday().String()[:3]
	currentTime := now.Format("15:04")

	if !shouldKill(prog, now, currentDay, currentTime) {
		t.Fatal("baseline: program should be killed without an active extension")
	}

	if err := state.AddProgramExtension(state.ProgramExtension{
		Program:   "firefox-esr",
		GrantedAt: now,
		ExpiresAt: now.Add(state.ExtensionDuration),
		Reason:    "client call",
	}); err != nil {
		t.Fatalf("AddProgramExtension: %v", err)
	}

	if shouldKill(prog, now, currentDay, currentTime) {
		t.Error("active extension should suppress the kill")
	}
}

func TestShouldKill_ActiveExtensionIgnoredWhenNotExtendible(t *testing.T) {
	orig := state.ProgramExtensionsPath
	state.ProgramExtensionsPath = t.TempDir() + "/extensions.json"
	state.ClearProgramExtensions()
	t.Cleanup(func() {
		state.ProgramExtensionsPath = orig
		state.ClearProgramExtensions()
	})

	prog := config.ForbiddenProgram{
		Name:       "chromium",
		Extendible: false,
		KillWindows: []config.TimeWindow{
			{Start: "00:00", End: "23:59", Days: []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}},
		},
	}

	now := time.Now()
	// Even with a grant on the books, a non-extendible program is still
	// killed — the safety belt against config drift.
	if err := state.AddProgramExtension(state.ProgramExtension{
		Program:   "chromium",
		GrantedAt: now,
		ExpiresAt: now.Add(state.ExtensionDuration),
	}); err != nil {
		t.Fatalf("AddProgramExtension: %v", err)
	}

	if !shouldKill(prog, now, now.Weekday().String()[:3], now.Format("15:04")) {
		t.Error("non-extendible program must be killed even with a stale grant")
	}
}

func TestRecordViolation_Disabled(t *testing.T) {
	// Clear violations
	state.ClearViolations()

	cfg := &config.Config{
		ViolationTracking: config.ViolationTrackingConfig{
			Enabled: false,
		},
	}

	// Record a violation
	RecordViolation(cfg, "web_access", "example.com", "https://example.com")

	// Check that it was NOT recorded (disabled)
	violations := state.GetViolations()
	if len(violations) != 0 {
		t.Errorf("Expected 0 violations when disabled, got %d", len(violations))
	}
}

// TestKillOnBlock_TerminatesMatchingProcess spawns a uniquely-named, killable
// process and verifies KillOnBlock terminates it. The unique name keeps the
// substring match from touching any unrelated process on the host.
func TestKillOnBlock_TerminatesMatchingProcess(t *testing.T) {
	src := "/bin/sleep"
	if _, err := os.Stat(src); err != nil {
		src = "/usr/bin/sleep"
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("cannot read sleep binary: %v", err)
	}

	// `comm` is truncated to 15 chars; keep the name short and distinctive so
	// it can't collide with "glocker"/"systemd"/etc. and only matches us.
	const name = "glkkilltest"
	dst := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("write test binary: %v", err)
	}

	cmd := exec.Command(dst, "300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start test process: %v", err)
	}
	// Safety net in case the kill path fails.
	defer func() { _ = cmd.Process.Kill() }()

	// Give it a moment to show up in `ps`.
	time.Sleep(300 * time.Millisecond)

	cfg := &config.Config{Domains: config.DomainsConfig{KillOnBlock: []string{name}}}
	KillOnBlock(cfg)

	// KillOnBlock sends TERM then KILL after a 2s grace period; wait for exit.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		// Process exited (killed) — success.
	case <-time.After(5 * time.Second):
		t.Fatalf("process %q was not terminated by KillOnBlock", name)
	}
}

// TestKillOnBlock_EmptyListNoop ensures an unset kill_on_block is a harmless no-op.
func TestKillOnBlock_EmptyListNoop(t *testing.T) {
	KillOnBlock(&config.Config{})
	KillOnBlock(&config.Config{Domains: config.DomainsConfig{KillOnBlock: []string{"", "   "}}})
}
