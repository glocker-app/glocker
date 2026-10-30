package enforcement

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glocker/internal/config"
)

func hostsCfg(path string) *config.Config {
	c := &config.Config{}
	c.Domains.HostsPath = path
	return c
}

// UpdateHosts writes an uncorrupted file (no NUL bytes), captures a one-time
// backup of the original, and CleanupHostsFile restores the original both via
// the marker and, when the marker is lost, via the backup.
func TestHostsAtomicWriteBackupAndRestore(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts")
	original := "127.0.0.1 localhost\n::1 localhost\n# my own entry\n10.0.0.5 nas\n"
	if err := os.WriteFile(hostsPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// Redirect the backup into the temp dir.
	old := config.HostsBackupPath
	config.HostsBackupPath = filepath.Join(dir, "hosts.orig")
	t.Cleanup(func() { config.HostsBackupPath = old })

	cfg := hostsCfg(hostsPath)
	if err := UpdateHosts(cfg, []string{"example.com", "foo.test"}, false); err != nil {
		t.Fatalf("UpdateHosts: %v", err)
	}

	got, _ := os.ReadFile(hostsPath)
	if bytes.IndexByte(got, 0) != -1 {
		t.Fatal("hosts file contains NUL bytes — not written cleanly")
	}
	for _, want := range []string{"127.0.0.1 localhost", "10.0.0.5 nas", config.HostsMarkerStart,
		"127.0.0.1 example.com", "::1 www.foo.test"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("hosts missing %q", want)
		}
	}
	// Backup captured the pre-glocker original (no marker, no blocked domains).
	bak, err := os.ReadFile(config.HostsBackupPath)
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if strings.Contains(string(bak), config.HostsMarkerStart) || strings.Contains(string(bak), "example.com") {
		t.Fatalf("backup should be the clean original, got:\n%s", bak)
	}

	// Normal cleanup (marker present) → original restored, block gone.
	if err := CleanupHostsFile(cfg); err != nil {
		t.Fatalf("CleanupHostsFile: %v", err)
	}
	after, _ := os.ReadFile(hostsPath)
	if strings.Contains(string(after), config.HostsMarkerStart) || strings.Contains(string(after), "example.com") {
		t.Fatalf("cleanup left glocker entries:\n%s", after)
	}
	if !strings.Contains(string(after), "10.0.0.5 nas") {
		t.Fatal("cleanup dropped the user's own entry")
	}

	// Corruption case: a blocklist with the marker lost. Cleanup must fall back to
	// the backup rather than leaving the blocklist behind.
	corrupt := "127.0.0.1 localhost\n127.0.0.1 example.com\n127.0.0.1 spam.test\n"
	if err := os.WriteFile(hostsPath, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CleanupHostsFile(cfg); err != nil {
		t.Fatalf("CleanupHostsFile (corrupt): %v", err)
	}
	restored, _ := os.ReadFile(hostsPath)
	if string(restored) != string(bak) {
		t.Fatalf("corrupt cleanup should restore the backup.\n got: %q\nwant: %q", restored, bak)
	}
}
