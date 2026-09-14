package enforcement

import (
	"log"
	"log/slog"
	"time"

	"glocker/internal/config"
)

// RunOnce performs a single enforcement cycle, applying all configured blocking mechanisms.
// It updates the hosts file and sudoers restrictions based on current time windows.
func RunOnce(cfg *config.Config, dryRun bool) {
	now := time.Now()
	slog.Debug("Starting enforcement run", "time", now.Format("2006-01-02 15:04:05"), "dry_run", dryRun)

	// Clean up expired temporary unblocks
	CleanupExpiredUnblocks(now)

	blockedDomains := GetDomainsToBlock(cfg, now)
	slog.Debug("Domains to block determined", "count", len(blockedDomains))

	if cfg.Domains.Enabled {
		slog.Debug("Updating hosts file", "enabled", true)
		if err := UpdateHosts(cfg, blockedDomains, dryRun); err != nil {
			log.Printf("ERROR updating hosts: %v", err)
		}
	} else {
		slog.Debug("Hosts file management disabled")
	}

	if cfg.Sudoers.Enabled {
		slog.Debug("Updating sudoers configuration", "enabled", true)
		if err := UpdateSudoers(cfg, now, dryRun, false); err != nil {
			log.Printf("ERROR updating sudoers: %v", err)
		}
	} else {
		slog.Debug("Sudoers management disabled")
	}

	if cfg.Programs.Enabled {
		slog.Debug("Forbidden programs monitoring", "enabled", true)
		// Note: Forbidden programs are monitored in a separate goroutine
		// This is just for status reporting
	} else {
		slog.Debug("Forbidden programs monitoring disabled")
	}

	slog.Debug("Enforcement run completed")
}

