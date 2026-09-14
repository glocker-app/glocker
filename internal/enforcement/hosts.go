package enforcement

import (
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"strings"

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

	// Remove immutable flag temporarily
	slog.Debug("Removing immutable flag from hosts file", "command", "chattr -i "+hostsPath)
	if err := exec.Command("chattr", "-i", hostsPath).Run(); err != nil {
		slog.Debug("Failed to remove immutable flag (may not be set)", "error", err)
	} else {
		slog.Debug("Successfully removed immutable flag")
	}

	// Open file for writing
	file, err := os.OpenFile(hostsPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		slog.Debug("Failed to open hosts file for writing", "error", err, "path", hostsPath)
		return fmt.Errorf("opening hosts file for writing: %w", err)
	}
	defer file.Close()

	slog.Debug("Writing hosts file in chunks", "path", hostsPath)

	// Write original content first
	if len(originalLines) > 0 {
		// Remove any trailing empty lines from original content
		for len(originalLines) > 0 && strings.TrimSpace(originalLines[len(originalLines)-1]) == "" {
			originalLines = originalLines[:len(originalLines)-1]
		}

		originalContent := strings.Join(originalLines, "\n")
		if originalContent != "" {
			originalContent += "\n"
		}

		if _, err := file.WriteString(originalContent); err != nil {
			slog.Debug("Failed to write original content", "error", err)
			return fmt.Errorf("writing original content: %w", err)
		}
		slog.Debug("Wrote original hosts content", "lines", len(originalLines))
	}

	// Add empty line and start marker
	if _, err := file.WriteString("\n" + config.HostsMarkerStart + "\n"); err != nil {
		slog.Debug("Failed to write start marker", "error", err)
		return fmt.Errorf("writing start marker: %w", err)
	}
	slog.Debug("Wrote glocker start marker")

	// Special-cased local alias for the /stats dashboard: reach it at
	// http://glocker.localhost instead of typing 127.0.0.1. It lives inside the
	// glocker-managed block (protected by the immutable flag, removed on
	// uninstall), and resolving to loopback keeps the dashboard's
	// localhost-only guard satisfied.
	if _, err := file.WriteString("127.0.0.1 glocker.localhost\n::1 glocker.localhost\n"); err != nil {
		slog.Debug("Failed to write glocker.localhost alias", "error", err)
		return fmt.Errorf("writing glocker.localhost alias: %w", err)
	}

	// Write domains in chunks
	const chunkSize = 1000
	totalDomains := len(domains)
	chunksWritten := 0

	for i := 0; i < totalDomains; i += chunkSize {
		end := i + chunkSize
		if end > totalDomains {
			end = totalDomains
		}

		// Build chunk content
		var chunkBuilder strings.Builder
		for j := i; j < end; j++ {
			domain := domains[j]
			chunkBuilder.WriteString(fmt.Sprintf("127.0.0.1 %s\n", domain))
			chunkBuilder.WriteString(fmt.Sprintf("127.0.0.1 www.%s\n", domain))
			chunkBuilder.WriteString(fmt.Sprintf("::1 %s\n", domain))
			chunkBuilder.WriteString(fmt.Sprintf("::1 www.%s\n", domain))
		}

		// Write chunk to file
		if _, err := file.WriteString(chunkBuilder.String()); err != nil {
			slog.Debug("Failed to write domain chunk", "error", err, "chunk", chunksWritten+1)
			return fmt.Errorf("writing domain chunk %d: %w", chunksWritten+1, err)
		}

		chunksWritten++

		// Flush and log progress every 1000 chunks or at the end
		if chunksWritten%1000 == 0 || end == totalDomains {
			if err := file.Sync(); err != nil {
				slog.Debug("Failed to sync file", "error", err)
			}

			domainsProcessed := end
			log.Printf("Hosts file progress: %d/%d domains written (%d chunks, %.1f%%)",
				domainsProcessed, totalDomains, chunksWritten,
				float64(domainsProcessed)/float64(totalDomains)*100)

			slog.Debug("Hosts file chunk progress",
				"chunks_written", chunksWritten,
				"domains_processed", domainsProcessed,
				"total_domains", totalDomains,
				"latest_domain", domains[end-1])
		}
	}

	// Final sync
	if err := file.Sync(); err != nil {
		slog.Debug("Failed to final sync file", "error", err)
	}

	slog.Debug("Successfully wrote hosts file in chunks", "total_chunks", chunksWritten, "total_domains", totalDomains)
	log.Printf("Hosts file update completed: %d domains written in %d chunks", totalDomains, chunksWritten)

	// Set immutable flag
	slog.Debug("Setting immutable flag on hosts file", "command", "chattr +i "+hostsPath)
	if err := exec.Command("chattr", "+i", hostsPath).Run(); err != nil {
		slog.Debug("Failed to set immutable flag", "error", err)
	} else {
		slog.Debug("Successfully set immutable flag")
	}

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

	// Remove everything after the start marker
	for _, line := range lines {
		if strings.Contains(line, config.HostsMarkerStart) {
			break
		}
		originalLines = append(originalLines, line)
	}

	// Remove immutable flag
	exec.Command("chattr", "-i", hostsPath).Run()

	// Write back the cleaned content
	newContent := strings.Join(originalLines, "\n")
	if err := os.WriteFile(hostsPath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("writing cleaned hosts file: %w", err)
	}

	log.Printf("Cleaned up hosts file: removed glocker entries")
	return nil
}
