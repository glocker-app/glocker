package monitoring

import (
	"fmt"
	"log"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/notify"
	"glocker/internal/state"
)

// RecordViolation adds a violation to the tracking system and checks thresholds.
func RecordViolation(cfg *config.Config, violationType, host, url string) {
	if !cfg.ViolationTracking.Enabled {
		return
	}

	violation := state.Violation{
		Timestamp: time.Now(),
		Host:      host,
		URL:       url,
		Type:      violationType,
	}

	state.AddViolation(violation)

	slog.Debug("Recorded violation", "type", violationType, "host", host, "url", url)
	log.Printf("VIOLATION RECORDED: %s - %s (%s)", violationType, host, url)

	// Check if we've exceeded the threshold
	go checkViolationThreshold(cfg)
}

// checkViolationThreshold checks if violation threshold has been exceeded.
func checkViolationThreshold(cfg *config.Config) {
	if !cfg.ViolationTracking.Enabled {
		return
	}

	now := time.Now()
	recentCount := countRecentViolations(cfg, now)

	slog.Debug("Checking violation threshold", "recent_count", recentCount, "max_violations", cfg.ViolationTracking.MaxViolations)

	if recentCount >= cfg.ViolationTracking.MaxViolations {
		log.Printf("VIOLATION THRESHOLD EXCEEDED: %d/%d violations in last %d minutes",
			recentCount, cfg.ViolationTracking.MaxViolations, cfg.ViolationTracking.TimeWindowMinutes)

		// Send desktop notification
		notify.SendNotification(cfg, "Glocker Alert",
			fmt.Sprintf("Violation threshold exceeded: %d/%d", recentCount, cfg.ViolationTracking.MaxViolations),
			"critical", "dialog-warning")

		// Execute the configured command
		if cfg.ViolationTracking.Command != "" {
			executeViolationCommand(cfg, recentCount)
		}

		log.Printf("Violation command executed - violations will continue to trigger until daily reset")
	}
}

// countRecentViolations counts violations within the configured time window.
func countRecentViolations(cfg *config.Config, now time.Time) int {
	if !cfg.ViolationTracking.Enabled {
		return 0
	}

	cutoff := now.Add(-time.Duration(cfg.ViolationTracking.TimeWindowMinutes) * time.Minute)
	count := 0

	violations := state.GetViolations()
	for _, v := range violations {
		if v.Timestamp.After(cutoff) {
			count++
		}
	}

	return count
}

// executeViolationCommand executes the configured command when threshold is exceeded.
func executeViolationCommand(cfg *config.Config, count int) {
	parts := strings.Fields(cfg.ViolationTracking.Command)
	if len(parts) == 0 {
		return
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	if err := cmd.Run(); err != nil {
		log.Printf("Failed to execute violation command: %v", err)
	} else {
		log.Printf("Violation command executed successfully")
	}
}

// MonitorViolations monitors and automatically resets violations daily.
func MonitorViolations(cfg *config.Config) {
	if !cfg.ViolationTracking.Enabled {
		return
	}

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		lastReset := state.GetLastViolationReset()

		// Reset violations at midnight
		if lastReset.IsZero() || (now.Day() != lastReset.Day() && now.Hour() == 0) {
			state.ClearViolations()
			log.Printf("Violations reset at daily boundary")
		}
	}
}
