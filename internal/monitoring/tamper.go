package monitoring

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
)

// MonitorTampering continuously monitors file checksums and system state for tampering.
// It checks files and service status at regular intervals.
func MonitorTampering(cfg *config.Config, checksums []state.FileChecksum, filesToMonitor []string) {
	// Set default check interval if not specified
	checkInterval := cfg.TamperDetection.CheckInterval
	if checkInterval == 0 {
		checkInterval = 30 // Default: check every 30 seconds
	}

	ticker := time.NewTicker(time.Duration(checkInterval) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		log.Println("Tamper check")
		tampered := false
		var tamperReasons []string

		// Check file checksums
		var currentChecksums []state.FileChecksum
		for _, filePath := range filesToMonitor {
			checksum := CaptureChecksum(cfg, filePath)
			currentChecksums = append(currentChecksums, checksum)
		}
		for i, current := range currentChecksums {
			original := checksums[i]

			// File was deleted
			if original.Exists && !current.Exists {
				tampered = true
				tamperReasons = append(tamperReasons, fmt.Sprintf("File deleted: %s", current.Path))
			}

			// File was modified
			if original.Exists && current.Exists && original.Checksum != current.Checksum {
				tampered = true
				tamperReasons = append(tamperReasons, fmt.Sprintf("File modified: %s", current.Path))
			}
		}

		// Check if service is still running
		if !IsServiceRunning() {
			tampered = true
			tamperReasons = append(tamperReasons, "Glocker service was stopped")
		}

		// Trigger alarm if tampering detected
		if tampered {
			log.Println("Tamper check failed")
			log.Println(tamperReasons)

			RaiseAlarm(cfg, tamperReasons)
			// Update baseline checksums after alarm
			checksums = nil
			for _, filePath := range filesToMonitor {
				checksum := CaptureChecksum(cfg, filePath)
				checksums = append(checksums, checksum)
			}
			// Also update global checksums
			state.SetGlobalChecksums(checksums)
		}
	}
}

// CaptureChecksum calculates and returns the checksum for a file.
// For hosts files, it only checksums the glocker section.
func CaptureChecksum(cfg *config.Config, path string) state.FileChecksum {
	checksum := state.FileChecksum{Path: path}

	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		checksum.Exists = false
	} else {
		checksum.Exists = true

		// For hosts file, only checksum the GLOCKER section
		if path == cfg.Domains.HostsPath {
			if data, err := os.ReadFile(path); err == nil {
				glockerSection := ExtractGlockerSection(string(data))
				hash := sha256.Sum256([]byte(glockerSection))
				checksum.Checksum = fmt.Sprintf("%x", hash)
			}
		} else {
			// Calculate SHA256 checksum for other files
			if data, err := os.ReadFile(path); err == nil {
				hash := sha256.Sum256(data)
				checksum.Checksum = fmt.Sprintf("%x", hash)
			}
		}
	}

	return checksum
}

// UpdateChecksum updates the checksum for a specific file in the global state.
func UpdateChecksum(filePath string) {
	cfg := state.GetGlobalConfig()
	if cfg == nil {
		return // Tamper detection not initialized
	}

	filesToMonitor := state.GetGlobalFilesToMonitor()

	// Find the index of the file in our monitoring list
	fileIndex := -1
	for i, monitoredFile := range filesToMonitor {
		if monitoredFile == filePath {
			fileIndex = i
			break
		}
	}

	if fileIndex == -1 {
		return // File not being monitored
	}

	// Update the checksum
	newChecksum := CaptureChecksum(cfg, filePath)
	state.UpdateChecksum(filePath, newChecksum.Checksum, newChecksum.Exists)
	log.Printf("Updated checksum for %s: %s", filePath, newChecksum.Checksum)
}

// IsServiceRunning checks if the glocker systemd service is active.
func IsServiceRunning() bool {
	cmd := exec.Command("systemctl", "is-active", "glocker.service")
	output, _ := cmd.Output()
	return strings.TrimSpace(string(output)) == "active"
}

// RaiseAlarm sends notifications and executes the alarm command when tampering is detected.
func RaiseAlarm(cfg *config.Config, reasons []string) {
	if cfg.TamperDetection.AlarmCommand == "" {
		return
	}

	// Prepare alarm message
	message := "GLOCKER TAMPER DETECTED:\\n"
	for _, reason := range reasons {
		message += "  - " + reason + "\\n"
	}

	// Execute alarm command - split on spaces for proper argument handling
	parts := strings.Fields(cfg.TamperDetection.AlarmCommand)
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Env = append(os.Environ(),
		"GLOCKER_TAMPER_MESSAGE="+message,
		"GLOCKER_TAMPER_REASONS="+strings.Join(reasons, "; "),
	)

	cmd.Run()
}

// ExtractGlockerSection extracts only the glocker-managed portion of a hosts file.
func ExtractGlockerSection(content string) string {
	lines := strings.Split(content, "\n")
	var glockerLines []string
	inGlockerSection := false

	for _, line := range lines {
		if strings.Contains(line, config.HostsMarkerStart) {
			inGlockerSection = true
			glockerLines = append(glockerLines, line)
			continue
		}
		if inGlockerSection {
			glockerLines = append(glockerLines, line)
		}
	}

	return strings.Join(glockerLines, "\n")
}
