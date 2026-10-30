package enforcement

import (
	"bytes"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/renameio/v2"

	"glocker/internal/config"
)

// UpdateHosts updates the /etc/hosts file with blocked domains.
// It removes old glocker entries and adds new ones based on the provided domains list.
// Uses chunked writing for performance with large domain lists.
func UpdateHosts(cfg *config.Config, domains []string, dryRun bool) error {
	hostsPath := cfg.Domains.HostsPath
	slog.Debug("Starting hosts file update", "hosts_path", hostsPath, "domains_count", len(domains), "dry_run", dryRun)

	// Log the domains being processed
	if len(domains) > 0 {
		slog.Debug("Number of domains to block", "domains", len(domains))
	} else {
		slog.Debug("No domains to block - will clear existing blocks")
	}

	// Check if hosts file exists and get current permissions
	fileInfo, err := os.Stat(hostsPath)
	if err != nil && !os.IsNotExist(err) {
		slog.Debug("Failed to stat hosts file", "error", err)
		return fmt.Errorf("checking hosts file: %w", err)
	}
	if fileInfo != nil {
		slog.Debug("Hosts file info", "size", fileInfo.Size(), "mode", fileInfo.Mode(), "mod_time", fileInfo.ModTime())
	}

	// Read current hosts file
	content, err := os.ReadFile(hostsPath)
	if err != nil && !os.IsNotExist(err) {
		slog.Debug("Failed to read hosts file", "error", err)
		return fmt.Errorf("reading hosts file: %w", err)
	}
	slog.Debug("Read hosts file", "size_bytes", len(content), "exists", err == nil)

	lines := strings.Split(string(content), "\n")
	var originalLines []string
	inBlockSection := false
	removedLines := 0
	originalLineCount := len(lines)

	slog.Debug("Processing hosts file content", "original_lines", originalLineCount)

	// Remove old glocker block section (everything after start marker)
	for i, line := range lines {
		if strings.Contains(line, config.HostsMarkerStart) {
			slog.Debug("Found glocker block start marker", "line_number", i+1)
			inBlockSection = true
			// Don't include the start marker line or anything after it
			break
		}
		originalLines = append(originalLines, line)
	}

	if inBlockSection {
		removedLines = originalLineCount - len(originalLines)
		slog.Debug("Removed old glocker block", "removed_lines", removedLines, "remaining_lines", len(originalLines))
	}

	if dryRun {
		slog.Debug("Dry run mode - would write hosts file with chunked approach", "original_lines", len(originalLines), "domains_to_add", len(domains))
		return nil
	}

	// Drop trailing blank lines from the preserved original content.
	for len(originalLines) > 0 && strings.TrimSpace(originalLines[len(originalLines)-1]) == "" {
		originalLines = originalLines[:len(originalLines)-1]
	}

	// One-time backup of the original (pre-glocker) hosts, so uninstall can restore
	// it even if our markers are later lost. On a clean first run originalLines IS
	// the real original (no marker found); on later cycles it's the same content
	// above the marker, and the backup already exists so this is a no-op.
	backupOriginalHosts(originalLines)

	// Build the whole new file in memory, then write it atomically (temp file +
	// rename). The old path truncated /etc/hosts in place and streamed ~50 MB in
	// chunks; a daemon killed mid-write (e.g. during an install/uninstall cycle)
	// left a truncated, NUL-holed hosts file. An atomic replace can't tear.
	var buf bytes.Buffer
	if s := strings.Join(originalLines, "\n"); s != "" {
		buf.WriteString(s)
		buf.WriteByte('\n')
	}
	buf.WriteString("\n" + config.HostsMarkerStart + "\n")
	// Local alias for the /stats dashboard (http://glocker.localhost), inside the
	// managed block so it's removed on uninstall.
	buf.WriteString("127.0.0.1 glocker.localhost\n::1 glocker.localhost\n")
	for _, domain := range domains {
		fmt.Fprintf(&buf, "127.0.0.1 %s\n127.0.0.1 www.%s\n::1 %s\n::1 www.%s\n", domain, domain, domain, domain)
	}

	if err := renameio.WriteFile(hostsPath, buf.Bytes(), 0o644); err != nil {
		slog.Debug("Failed to write hosts file", "error", err, "path", hostsPath)
		return fmt.Errorf("writing hosts file: %w", err)
	}
	log.Printf("Hosts file update completed: %d domains written", len(domains))

	// Update checksum after legitimate change
	// Note: updateChecksum is in monitoring package, will be integrated later
	slog.Debug("Updating checksum for hosts file")
	slog.Debug("Hosts file update completed successfully")

	return nil
}

// CleanupHostsFile removes all glocker entries from the hosts file.
// This is used during uninstallation to restore the original hosts file.
func CleanupHostsFile(cfg *config.Config) error {
	hostsPath := cfg.Domains.HostsPath

	// Read current hosts file
	content, err := os.ReadFile(hostsPath)
	if err != nil {
		return fmt.Errorf("reading hosts file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var originalLines []string
	markerFound := false
	for _, line := range lines {
		if strings.Contains(line, config.HostsMarkerStart) {
			markerFound = true
			break
		}
		originalLines = append(originalLines, line)
	}

	var out []byte
	switch {
	case markerFound:
		// Normal path: drop our appended block, keep everything above it (preserves
		// any edits the user made to their own hosts entries).
		s := strings.Join(originalLines, "\n")
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		out = []byte(s)
	default:
		// No marker — e.g. the file was corrupted and lost it. Rather than leave the
		// whole blocklist behind, restore the original we backed up at install time.
		backup, berr := os.ReadFile(config.HostsBackupPath)
		if berr != nil {
			log.Printf("hosts cleanup: no marker and no backup (%v); leaving %s unchanged", berr, hostsPath)
			return nil
		}
		log.Printf("hosts cleanup: no marker found; restoring original from %s", config.HostsBackupPath)
		out = backup
	}

	// Atomic replace (temp file + rename) so an interrupted cleanup can't corrupt it.
	if err := renameio.WriteFile(hostsPath, out, 0o644); err != nil {
		return fmt.Errorf("writing cleaned hosts file: %w", err)
	}
	log.Printf("Cleaned up hosts file: restored original entries")
	return nil
}

// backupOriginalHosts saves the pre-glocker hosts content once, so uninstall can
// recover the user's original even if our in-file markers are later lost. No-op
// if the backup already exists.
func backupOriginalHosts(originalLines []string) {
	if _, err := os.Stat(config.HostsBackupPath); err == nil {
		return // already captured
	}
	if err := os.MkdirAll(filepath.Dir(config.HostsBackupPath), 0o755); err != nil {
		slog.Debug("hosts backup: mkdir failed", "error", err)
		return
	}
	content := strings.Join(originalLines, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := renameio.WriteFile(config.HostsBackupPath, []byte(content), 0o644); err != nil {
		slog.Debug("hosts backup: write failed", "error", err)
		return
	}
	slog.Debug("hosts backup: saved original", "path", config.HostsBackupPath, "lines", len(originalLines))
}
