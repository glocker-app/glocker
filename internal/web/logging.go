package web

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
)

// LogContentReport logs a content monitoring report from the browser extension.
func LogContentReport(cfg *config.Config, report *state.ContentReport) error {
	logFile := cfg.Content.Monitoring.LogFile
	if logFile == "" {
		logFile = "/var/log/glocker-reports.log"
	}

	// Create log entry
	timestamp := time.Unix(report.Timestamp/1000, 0).Format("2006-01-02 15:04:05")
	logEntry := fmt.Sprintf("[%s] | %s | %s", timestamp, report.Trigger, report.URL)
	if report.Domain != "" {
		logEntry += fmt.Sprintf(" | %s", report.Domain)
	}
	logEntry += "\n"

	// Append to log file
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	_, err = file.WriteString(logEntry)
	if err != nil {
		return fmt.Errorf("failed to write to log file: %w", err)
	}

	return nil
}

// LogUnblockEntry logs a temporary unblock request with details.
func LogUnblockEntry(cfg *config.Config, domain, reason string, unblockTime, restoreTime time.Time) error {
	if cfg.Domains.Unblocking.LogFile == "" {
		return nil // No log file configured
	}

	entry := state.UnblockLogEntry{
		UnblockTime: unblockTime,
		RestoreTime: restoreTime,
		Reason:      reason,
		Domain:      domain,
	}

	// Convert to JSON
	jsonData, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal unblock entry: %w", err)
	}

	// Append to log file
	file, err := os.OpenFile(cfg.Domains.Unblocking.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open unblock log file: %w", err)
	}
	defer file.Close()

	// Write JSON entry with newline
	_, err = file.WriteString(string(jsonData) + "\n")
	if err != nil {
		return fmt.Errorf("failed to write to unblock log file: %w", err)
	}

	slog.Debug("Logged unblock entry", "domain", domain, "reason", reason, "log_file", cfg.Domains.Unblocking.LogFile)
	return nil
}

// ParseUnblockLog reads and parses the unblock log file to generate statistics.
func ParseUnblockLog(cfg *config.Config) (*state.UnblockStats, error) {
	if cfg.Domains.Unblocking.LogFile == "" {
		return &state.UnblockStats{
			ReasonCounts: make(map[string]int),
			DomainCounts: make(map[string]int),
		}, nil
	}

	file, err := os.Open(cfg.Domains.Unblocking.LogFile)
	if err != nil {
		if os.IsNotExist(err) {
			// Log file doesn't exist yet, return empty stats
			return &state.UnblockStats{
				ReasonCounts: make(map[string]int),
				DomainCounts: make(map[string]int),
			}, nil
		}
		return nil, fmt.Errorf("failed to open unblock log file: %w", err)
	}
	defer file.Close()

	stats := &state.UnblockStats{
		ReasonCounts: make(map[string]int),
		DomainCounts: make(map[string]int),
	}

	today := time.Now().Format("2006-01-02")
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry state.UnblockLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			slog.Debug("Failed to parse unblock log entry", "line", line, "error", err)
			continue
		}

		stats.TotalCount++
		stats.ReasonCounts[entry.Reason]++
		stats.DomainCounts[entry.Domain]++

		// Check if this entry is from today
		if entry.UnblockTime.Format("2006-01-02") == today {
			stats.TodayCount++
			stats.TodayEntries = append(stats.TodayEntries, entry)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading unblock log file: %w", err)
	}

	return stats, nil
}

// IsValidUnblockReason checks if a provided reason is in the list of valid unblock reasons.
func IsValidUnblockReason(cfg *config.Config, reason string) bool {
	// If no reasons are configured, allow any reason
	if len(cfg.Domains.Unblocking.Reasons) == 0 {
		return true
	}

	// Check if the provided reason matches any of the configured valid reasons
	for _, validReason := range cfg.Domains.Unblocking.Reasons {
		if strings.EqualFold(reason, validReason) {
			return true
		}
	}

	return false
}

// DefaultLifecycleLogFile is the default path for install/uninstall logs.
const DefaultLifecycleLogFile = "/var/log/glocker-lifecycle.log"

// logLifecycleEntry logs an install or uninstall event.
func logLifecycleEntry(cfg *config.Config, entryType, reason, note string) error {
	logFile := cfg.Lifecycle.LogFile
	if logFile == "" {
		logFile = DefaultLifecycleLogFile
	}

	entry := state.LifecycleLogEntry{
		Timestamp: time.Now(),
		Type:      entryType,
		Reason:    reason,
		Note:      note,
	}

	// Convert to JSON
	jsonData, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal lifecycle entry: %w", err)
	}

	// Append to log file
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open lifecycle log file: %w", err)
	}
	defer file.Close()

	// Write JSON entry with newline
	_, err = file.WriteString(string(jsonData) + "\n")
	if err != nil {
		return fmt.Errorf("failed to write to lifecycle log file: %w", err)
	}

	slog.Debug("Logged lifecycle entry", "type", entryType, "reason", reason, "note", note, "log_file", logFile)
	return nil
}

// LogUninstallEntry logs an uninstall request with details.
func LogUninstallEntry(cfg *config.Config, reason, note string) error {
	return logLifecycleEntry(cfg, "uninstall", reason, note)
}

// LogInstallEntry logs an installation event.
func LogInstallEntry() error {
	// Use default log file since config may not be loaded yet during install
	entry := state.LifecycleLogEntry{
		Timestamp: time.Now(),
		Type:      "install",
	}

	jsonData, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal install entry: %w", err)
	}

	file, err := os.OpenFile(DefaultLifecycleLogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open lifecycle log file: %w", err)
	}
	defer file.Close()

	_, err = file.WriteString(string(jsonData) + "\n")
	if err != nil {
		return fmt.Errorf("failed to write to lifecycle log file: %w", err)
	}

	slog.Debug("Logged install entry", "log_file", DefaultLifecycleLogFile)
	return nil
}
