package enforcement

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/utils"
)

// UpdateSudoers updates the /etc/sudoers file to restrict or allow sudo access
// based on the current time and configured time windows.
func UpdateSudoers(cfg *config.Config, now time.Time, dryRun bool, forceBlock bool) error {
	if !cfg.Sudoers.Enabled && !forceBlock {
		return nil
	}

	// Determine if sudo should be allowed at this time
	sudoAllowed := IsSudoAllowed(cfg, now)

	// Override with force block if requested
	if forceBlock {
		sudoAllowed = false
	}

	targetLine := cfg.Sudoers.BlockedSudoersLine
	if sudoAllowed {
		targetLine = cfg.Sudoers.AllowedSudoersLine
	}

	if dryRun {
		return nil
	}

	// Read current sudoers file
	content, err := os.ReadFile(config.SudoersPath)
	if err != nil {
		return fmt.Errorf("reading sudoers file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	found := false

	// Look for our managed line or the user's sudo line
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check if this is our managed line
		if strings.Contains(line, config.SudoersMarker) {
			// Replace with the target line
			newLines = append(newLines, targetLine+" "+config.SudoersMarker)
			found = true
			continue
		}

		// Check if this is an unmanaged line for our user
		if strings.HasPrefix(trimmed, cfg.User+" ") ||
			strings.HasPrefix(trimmed, "# "+cfg.User+" ") {
			// Replace with our managed version
			newLines = append(newLines, targetLine+" "+config.SudoersMarker)
			found = true
			continue
		}

		newLines = append(newLines, line)
	}

	// If we didn't find any line, add it at the end
	if !found {
		newLines = append(newLines, targetLine+" "+config.SudoersMarker)
	}

	newContent := strings.Join(newLines, "\n")

	// Write to a temporary file
	tmpFile := config.SudoersPath + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(newContent), 0440); err != nil {
		return fmt.Errorf("writing temporary sudoers file: %w", err)
	}
	defer os.Remove(tmpFile)

	// Validate the file with visudo
	cmd := exec.Command("visudo", "-c", "-f", tmpFile)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudoers validation failed: %w", err)
	}

	// Validation passed, now replace the real file
	if err := os.Rename(tmpFile, config.SudoersPath); err != nil {
		return fmt.Errorf("replacing sudoers file: %w", err)
	}

	// Ensure correct permissions
	os.Chmod(config.SudoersPath, 0440)

	// Update checksum after legitimate change

	return nil
}

// IsSudoAllowed determines if sudo access should be allowed at the given time
// based on the configured time windows.
func IsSudoAllowed(cfg *config.Config, now time.Time) bool {
	if !cfg.Sudoers.Enabled {
		return true // If not enabled, don't restrict
	}

	currentDay := now.Weekday().String()[:3]
	currentTime := now.Format("15:04")

	// Check if current time falls within any allow window
	for _, window := range cfg.Sudoers.AllowWindows {
		// For midnight-crossing windows, check previous day for early morning times
		dayToCheck := currentDay
		if window.Start > window.End && currentTime <= window.End {
			dayToCheck = now.AddDate(0, 0, -1).Weekday().String()[:3]
		}

		if !slices.Contains(window.Days, dayToCheck) {
			continue
		}

		if utils.IsInTimeWindow(currentTime, window.Start, window.End) {
			return true
		}
	}

	return false
}

// CreateSudoersBackup creates a backup of the sudoers file before modification.
// It only creates the backup if one doesn't already exist.
func CreateSudoersBackup() error {
	// Check if backup already exists
	if _, err := os.Stat(config.SudoersBackup); err == nil {
		// Backup already exists, don't overwrite
		return nil
	}

	// Read current sudoers
	content, err := os.ReadFile(config.SudoersPath)
	if err != nil {
		return err
	}

	// Write backup
	return os.WriteFile(config.SudoersBackup, content, 0440)
}
