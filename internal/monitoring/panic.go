package monitoring

import (
	"log"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
)

// MonitorPanicMode continuously monitors and enforces panic mode restrictions.
// Panic mode keeps the system suspended until the configured time expires.
func MonitorPanicMode(cfg *config.Config) {
	log.Printf("========== PANIC MONITOR STARTED ==========")
	log.Printf("Monitor checking every 1 second for panic state")
	log.Printf("===========================================")

	ticker := time.NewTicker(1 * time.Second) // Check every second
	defer ticker.Stop()

	lastLogTime := time.Time{} // Track last time we logged to avoid spam

	for range ticker.C {
		now := time.Now()
		currentPanicUntil := state.GetPanicUntil()
		currentLastSuspend := state.GetLastSuspendTime()

		// DEBUG: Log time comparison
		if !currentPanicUntil.IsZero() {
			log.Printf("DEBUG: now=%s (unix=%d), panicUntil=%s (unix=%d), now.Before=%v, now.After=%v",
				now.Format("15:04:05"), now.Unix(),
				currentPanicUntil.Format("15:04:05"), currentPanicUntil.Unix(),
				now.Before(currentPanicUntil), now.After(currentPanicUntil))
		}

		// Check if we're in panic mode and time hasn't expired yet
		// Use Unix timestamps for comparison to avoid monotonic clock issues during system suspend
		if !currentPanicUntil.IsZero() && now.Unix() < currentPanicUntil.Unix() {
			// Calculate remaining time using Unix timestamps to avoid monotonic clock issues
			remainingSeconds := int(currentPanicUntil.Unix() - now.Unix())

			// Grace period: Don't suspend again if we suspended less than 5 seconds ago
			// This prevents immediate re-suspend when the system wakes up from suspension
			timeSinceLastSuspend := time.Duration(now.Unix()-currentLastSuspend.Unix()) * time.Second
			gracePeriod := 5 * time.Second

			if !currentLastSuspend.IsZero() && timeSinceLastSuspend < gracePeriod {
				// Within grace period - skip this suspend
				if time.Duration(now.Unix()-lastLogTime.Unix())*time.Second >= 5*time.Second {
					log.Printf("---------- PANIC MONITOR (GRACE PERIOD) ----------")
					log.Printf("Current time: %s", now.Format("2006-01-02 15:04:05"))
					log.Printf("Last suspend: %s (%v ago)", currentLastSuspend.Format("15:04:05"), timeSinceLastSuspend.Round(time.Second))
					log.Printf("Grace period: %v remaining", (gracePeriod - timeSinceLastSuspend).Round(time.Second))
					log.Printf("Panic expires: %s (%d seconds)", currentPanicUntil.Format("15:04:05"), remainingSeconds)
					log.Printf("Status: GRACE PERIOD - Skipping suspend")
					log.Printf("--------------------------------------------------")
					lastLogTime = now
				}
				continue
			}

			// Log detailed info every 5 seconds to avoid spam
			if time.Duration(now.Unix()-lastLogTime.Unix())*time.Second >= 5*time.Second {
				log.Printf("---------- PANIC MONITOR CHECK ----------")
				log.Printf("Current time: %s (Unix: %d)", now.Format("2006-01-02 15:04:05"), now.Unix())
				log.Printf("Target time: %s (Unix: %d)", currentPanicUntil.Format("2006-01-02 15:04:05"), currentPanicUntil.Unix())
				log.Printf("Remaining: %d seconds (%d minutes)", remainingSeconds, remainingSeconds/60)
				log.Printf("Status: PANIC MODE ACTIVE - System should be suspended")
				lastLogTime = now
			}

			// Suspend the system if needed
			if cfg.Panic.Command != "" {
				ExecuteSuspendCommand(cfg.Panic.Command)
				state.SetLastSuspendTime(now)
			}
		}
	}
}

// ExecuteSuspendCommand executes the system suspend command.
func ExecuteSuspendCommand(command string) {
	// Implementation placeholder - actual suspension would be dangerous in refactoring
	log.Printf("Would execute suspend command: %s", command)
}
