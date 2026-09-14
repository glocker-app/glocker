package enforcement

import (
	"fmt"
	"log"
	"log/slog"
	"slices"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
	"glocker/internal/utils"
)

// GetDomainsToBlock evaluates all configured domains against current time windows
// and returns a list of domain names that should be blocked right now.
func GetDomainsToBlock(cfg *config.Config, now time.Time) []string {
	var blocked []string
	var loggedBlocked []string
	currentDay := now.Weekday().String()[:3] // Mon, Tue, etc.
	currentTime := now.Format("15:04")

	alwaysBlockCount := 0
	timeBasedBlockCount := 0
	tempUnblockedCount := 0

	slog.Debug("Evaluating domains for blocking", "current_day", currentDay, "current_time", currentTime, "total_domains", len(cfg.Domains.List))

	for _, domain := range cfg.Domains.List {
		if domain.LogBlocking {
			slog.Debug("Evaluating domain", "domain", domain.Name, "unblockable", domain.Unblockable, "has_block_windows", len(domain.BlockWindows) > 0)
		}

		// NEW BEHAVIOR: Domains are permanent (non-unblockable) by default
		// Only check temp unblock for domains explicitly marked as unblockable
		if domain.Unblockable {
			// Check if domain is temporarily unblocked (only for unblockable domains)
			if IsTempUnblocked(domain.Name, now) {
				tempUnblockedCount++
				if domain.LogBlocking {
					slog.Debug("Domain is temporarily unblocked", "domain", domain.Name)
					log.Printf("DOMAIN STATUS: %s -> temporarily unblocked (expires soon)", domain.Name)
				}
				continue
			}
		}

		// NEW BEHAVIOR: If no block windows are specified, always block (default)
		// This makes block windows the primary control mechanism
		if len(domain.BlockWindows) == 0 {
			// No block windows means always block
			alwaysBlockCount++
			blocked = append(blocked, domain.Name)
			if domain.LogBlocking {
				blockType := "always blocked (permanent)"
				if domain.Unblockable {
					blockType = "always blocked (unblockable)"
				}
				slog.Debug("Domain marked for always block", "domain", domain.Name, "unblockable", domain.Unblockable)
				log.Printf("DOMAIN STATUS: %s -> %s", domain.Name, blockType)
				loggedBlocked = append(loggedBlocked, domain.Name)
			}
			continue
		}

		// Check block windows - only reach here if block windows are defined
		domainBlocked := false
		activeWindow := ""
		for _, window := range domain.BlockWindows {
			if domain.LogBlocking {
				slog.Debug("Checking block window", "domain", domain.Name, "window_days", window.Days, "window_start", window.Start, "window_end", window.End)
			}

			// For midnight-crossing windows, check previous day for early morning times
			dayToCheck := currentDay
			if window.Start > window.End && currentTime <= window.End {
				dayToCheck = now.AddDate(0, 0, -1).Weekday().String()[:3]
				if domain.LogBlocking {
					slog.Debug("Checking previous day for wraparound window", "domain", domain.Name, "current_day", currentDay, "checking_day", dayToCheck)
				}
			}

			if !slices.Contains(window.Days, dayToCheck) {
				if domain.LogBlocking {
					slog.Debug("Day not in window", "domain", domain.Name, "day_checked", dayToCheck)
				}
				continue
			}

			if utils.IsInTimeWindow(currentTime, window.Start, window.End) {
				timeBasedBlockCount++
				blocked = append(blocked, domain.Name)
				domainBlocked = true
				activeWindow = fmt.Sprintf("%s-%s on %s", window.Start, window.End, strings.Join(window.Days, ","))
				if domain.LogBlocking {
					slog.Debug("Domain blocked by time window", "domain", domain.Name, "window", fmt.Sprintf("%s-%s", window.Start, window.End))
					log.Printf("DOMAIN STATUS: %s -> blocked by time window (%s)", domain.Name, activeWindow)
					loggedBlocked = append(loggedBlocked, domain.Name)
				}
				break
			}
		}

		if !domainBlocked && len(domain.BlockWindows) > 0 && domain.LogBlocking {
			slog.Debug("Domain not blocked by any block window", "domain", domain.Name)
			log.Printf("DOMAIN STATUS: %s -> not blocked (outside block windows)", domain.Name)
		}
	}

	// Log summary with counts only
	slog.Debug("Domain blocking evaluation complete",
		"total_blocked", len(blocked),
		"always_block_count", alwaysBlockCount,
		"time_based_block_count", timeBasedBlockCount,
		"temp_unblocked_count", tempUnblockedCount,
		"logged_domains_count", len(loggedBlocked))

	return blocked
}

// IsTempUnblocked checks if a domain is currently temporarily unblocked.
// Returns true if the domain has an active temporary unblock that hasn't expired.
func IsTempUnblocked(domain string, now time.Time) bool {
	unblocks := state.GetTempUnblocks()
	for _, unblock := range unblocks {
		if unblock.Domain == domain {
			if now.Before(unblock.ExpiresAt) {
				// Always log temporary unblocks since they're manual actions
				slog.Debug("Domain is temporarily unblocked", "domain", domain, "expires_at", unblock.ExpiresAt.Format("2006-01-02 15:04:05"))
				return true
			} else {
				// Always log when temporary unblocks expire since they're manual actions
				slog.Debug("Domain temporary unblock has expired", "domain", domain, "expired_at", unblock.ExpiresAt.Format("2006-01-02 15:04:05"))
			}
		}
	}
	return false
}

// CleanupExpiredUnblocks removes expired temporary unblocks from the state.
func CleanupExpiredUnblocks(now time.Time) {
	unblocks := state.GetTempUnblocks()
	var activeUnblocks []state.TempUnblock
	expiredCount := 0

	for _, unblock := range unblocks {
		if now.Before(unblock.ExpiresAt) {
			activeUnblocks = append(activeUnblocks, unblock)
		} else {
			expiredCount++
			slog.Debug("Removing expired temporary unblock", "domain", unblock.Domain, "expired_at", unblock.ExpiresAt.Format("2006-01-02 15:04:05"))
		}
	}

	if expiredCount > 0 {
		state.SetTempUnblocks(activeUnblocks)
		slog.Debug("Cleaned up expired temporary unblocks", "removed_count", expiredCount, "remaining_count", len(activeUnblocks))
	}
}

// GetBlockingReason returns a human-readable string explaining why a domain is blocked.
func GetBlockingReason(cfg *config.Config, domain string, now time.Time) string {
	currentDay := now.Weekday().String()[:3]
	currentTime := now.Format("15:04")

	// Find the domain in the config
	for _, configDomain := range cfg.Domains.List {
		if configDomain.Name == domain {
			// NEW BEHAVIOR: Domains without block windows are always blocked by default
			// Domains are permanent (non-unblockable) by default
			if len(configDomain.BlockWindows) == 0 {
				if configDomain.Unblockable {
					return "always blocked (can be temporarily unblocked)"
				}
				return "always blocked (permanent)"
			}

			// Check which block window is active
			for _, window := range configDomain.BlockWindows {
				if slices.Contains(window.Days, currentDay) && utils.IsInTimeWindow(currentTime, window.Start, window.End) {
					return fmt.Sprintf("time-based block (active %s-%s on %s)", window.Start, window.End, strings.Join(window.Days, ","))
				}
			}
		}
	}

	return "unknown blocking rule"
}
