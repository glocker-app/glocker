package cli

import (
	"fmt"
	"log"
	"log/slog"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/enforcement"
	"glocker/internal/monitoring"
	"glocker/internal/reports"
	"glocker/internal/state"
	"glocker/internal/syncer"
	"glocker/internal/web"
)

// GetStatusResponse returns a formatted runtime status report.
func GetStatusResponse(cfg *config.Config) string {
	var response strings.Builder
	now := time.Now()

	response.WriteString("╔════════════════════════════════════════════════╗\n")
	response.WriteString("║              RUNTIME STATUS                    ║\n")
	response.WriteString("╚════════════════════════════════════════════════╝\n\n")

	// Current time and service status
	response.WriteString(fmt.Sprintf("Current Time: %s\n", now.Format("2006-01-02 15:04:05")))
	response.WriteString(fmt.Sprintf("Service Status: Running\n\n"))

	// Get blocked domain count from enforcement state
	_, blockedCount, _ := enforcement.GetEnforcementState()

	// Show temporary unblocks
	unblocks := state.GetTempUnblocks()
	activeUnblocks := 0
	for _, unblock := range unblocks {
		if now.Before(unblock.ExpiresAt) {
			activeUnblocks++
		}
	}

	// Adjust blocked count for active temp unblocks
	effectiveBlocked := blockedCount - activeUnblocks
	if effectiveBlocked < 0 {
		effectiveBlocked = 0
	}
	response.WriteString(fmt.Sprintf("Currently Blocked Domains: %d\n", effectiveBlocked))
	response.WriteString(fmt.Sprintf("Temporary Unblocks: %d active\n", activeUnblocks))

	if activeUnblocks > 0 {
		response.WriteString("  Active temporary unblocks:\n")
		for _, unblock := range unblocks {
			if now.Before(unblock.ExpiresAt) {
				remaining := unblock.ExpiresAt.Sub(now)
				response.WriteString(fmt.Sprintf("    - %s (expires in %v)\n", unblock.Domain, remaining.Round(time.Minute)))
			}
		}
	}

	// Daily unblock budget (unblocking.max_per_day). Counted off the unblock log,
	// so it resets at local midnight and survives a restart.
	usedToday := countUnblocksToday(cfg)
	if cfg.Unblocking.MaxPerDay > 0 {
		left := max(0, cfg.Unblocking.MaxPerDay-usedToday)
		response.WriteString(fmt.Sprintf("Unblocks Today: %d/%d used (%d left)\n", usedToday, cfg.Unblocking.MaxPerDay, left))
	} else {
		response.WriteString(fmt.Sprintf("Unblocks Today: %d (no daily cap)\n", usedToday))
	}

	// Show program extensions (one-hour runtime grants for extendible
	// forbidden programs). Mirror the temp-unblock section's shape.
	extensions := state.GetProgramExtensions()
	activeExtensions := 0
	for _, ext := range extensions {
		if now.Before(ext.ExpiresAt) {
			activeExtensions++
		}
	}
	response.WriteString(fmt.Sprintf("Program Extensions: %d active\n", activeExtensions))
	if activeExtensions > 0 {
		response.WriteString("  Active program extensions:\n")
		for _, ext := range extensions {
			if now.Before(ext.ExpiresAt) {
				remaining := ext.ExpiresAt.Sub(now)
				response.WriteString(fmt.Sprintf("    - %s (expires in %v, reason: %s)\n",
					ext.Program, remaining.Round(time.Minute), ext.Reason))
			}
		}
	}

	// Show violation tracking status
	if cfg.ViolationTracking.Enabled {
		violations := state.GetViolations()
		recentViolations := 0
		cutoff := now.Add(-time.Duration(cfg.ViolationTracking.TimeWindowMinutes) * time.Minute)
		for _, v := range violations {
			if v.Timestamp.After(cutoff) {
				recentViolations++
			}
		}

		response.WriteString("\n")
		response.WriteString("Violation Tracking:\n")
		response.WriteString(fmt.Sprintf("  Recent Violations: %d/%d (in last %d minutes)\n",
			recentViolations, cfg.ViolationTracking.MaxViolations, cfg.ViolationTracking.TimeWindowMinutes))
		response.WriteString(fmt.Sprintf("  Total Violations: %d\n", len(violations)))

		// Show the most recent violations in the active window so the
		// user can see *what* tripped the counter, not just *how many*.
		if recentViolations > 0 {
			const maxShown = 5
			response.WriteString("  Recent details:\n")
			shown := 0
			for i := len(violations) - 1; i >= 0 && shown < maxShown; i-- {
				v := violations[i]
				if !v.Timestamp.After(cutoff) {
					continue
				}
				target := v.Host
				if target == "" {
					target = v.URL
				}
				response.WriteString(fmt.Sprintf("    - [%s] %s: %s\n",
					v.Timestamp.Format("15:04:05"), v.Type, target))
				shown++
			}
			if recentViolations > maxShown {
				response.WriteString(fmt.Sprintf("    ... and %d more\n", recentViolations-maxShown))
			}
		}
	}

	// Show panic mode status
	panicUntil := state.GetPanicUntil()
	if !panicUntil.IsZero() && now.Before(panicUntil) {
		remaining := panicUntil.Sub(now)
		response.WriteString("\n")
		response.WriteString("⚠️  PANIC MODE ACTIVE ⚠️\n")
		response.WriteString(fmt.Sprintf("Time Remaining: %v\n", remaining.Round(time.Second)))
	}

	// Show glockpeek sync status.
	response.WriteString("\n")
	if !cfg.Sync.Enabled {
		response.WriteString("Sync (glockpeek): disabled\n")
	} else {
		ss := state.GetSyncSummary()
		// Records sitting in the local logs that the next push will carry.
		pending := formatSyncCounts(syncer.PendingCounts(cfg, ss.Cursors))
		interval := time.Duration(cfg.Sync.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = time.Duration(config.DefaultSyncIntervalSeconds) * time.Second
		}
		if ss.LastSyncAt.IsZero() {
			response.WriteString("Sync (glockpeek): enabled — nothing pushed yet\n")
			response.WriteString(fmt.Sprintf("  Pending: %s\n", pending))
		} else {
			response.WriteString(fmt.Sprintf("Sync (glockpeek): last push %s ago\n",
				now.Sub(ss.LastSyncAt).Round(time.Second)))
			response.WriteString(fmt.Sprintf("  This session: %s\n", formatSyncCounts(ss.Total)))
			nextIn := ss.LastSyncAt.Add(interval).Sub(now).Round(time.Second)
			if nextIn > 0 {
				response.WriteString(fmt.Sprintf("  Pending: %s; next push in %s\n", pending, nextIn))
			} else {
				response.WriteString(fmt.Sprintf("  Pending: %s; next push imminent\n", pending))
			}
		}
		// Surface the most recent push failure (e.g. a require_encryption 422) so a
		// stuck sync is visible without digging through the journal.
		if ss.LastError != "" {
			response.WriteString(fmt.Sprintf("  ⚠ last push FAILED %s ago: %s\n",
				now.Sub(ss.LastErrorAt).Round(time.Second), ss.LastError))
		}
	}

	response.WriteString("\nEND\n")
	return response.String()
}

// formatSyncCounts renders per-source sync counts in a stable order, skipping
// zeros, with a total, e.g. "2 violations, 2 usage (4 records)".
func formatSyncCounts(m map[string]int) string {
	order := []string{"violations", "unblocks", "lifecycle", "usage", "heartbeat"}
	var parts []string
	total := 0
	for _, k := range order {
		if n := m[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
			total += n
		}
	}
	if total == 0 {
		return "0 records"
	}
	return fmt.Sprintf("%s (%d records)", strings.Join(parts, ", "), total)
}

// GetInfoResponse returns a formatted configuration information report.
func GetInfoResponse(cfg *config.Config) string {
	var response strings.Builder

	response.WriteString("╔════════════════════════════════════════════════╗\n")
	response.WriteString("║            CONFIGURATION INFO                  ║\n")
	response.WriteString("╚════════════════════════════════════════════════╝\n\n")

	response.WriteString(fmt.Sprintf("Enforcement Interval: %d seconds\n", cfg.EnforceInterval))

	// Get domain counts from enforcement state
	_, blockedCount, _ := enforcement.GetEnforcementState()
	timeWindowDomains := enforcement.GetTimeWindowDomains()
	timeBasedCount := len(timeWindowDomains)
	alwaysBlockCount := blockedCount - timeBasedCount
	if alwaysBlockCount < 0 {
		alwaysBlockCount = 0
	}

	response.WriteString(fmt.Sprintf("Total Domains: %d (%d always blocked, %d time-based)\n\n",
		blockedCount, alwaysBlockCount, timeBasedCount))

	// Show time-based blocked domains (from cached data)
	if timeBasedCount > 0 {
		response.WriteString(fmt.Sprintf("Time-Based Domains (%d) — blocked during the listed windows, accessible otherwise:\n", timeBasedCount))
		for i, domain := range timeWindowDomains {
			response.WriteString(fmt.Sprintf("  %s: blocked %s\n", domain.Name, formatTimeWindows(domain.BlockWindows)))
			if i >= 9 && len(timeWindowDomains) > 10 {
				response.WriteString(fmt.Sprintf("  ... and %d more\n", timeBasedCount-10))
				break
			}
		}
		response.WriteString("\n")
	}

	// Show forbidden programs with time windows
	if cfg.EnableForbiddenPrograms && cfg.ForbiddenPrograms.Enabled && len(cfg.ForbiddenPrograms.Programs) > 0 {
		response.WriteString(fmt.Sprintf("Forbidden Programs (%d) — killed on sight during the listed windows:\n", len(cfg.ForbiddenPrograms.Programs)))

		// Bucket programs by which windows they set. "always killed" requires
		// both lists empty; an allow-only program is forbidden outside those
		// windows, not unconditionally.
		var alwaysBlocked []string
		var windowed []config.ForbiddenProgram

		for _, program := range cfg.ForbiddenPrograms.Programs {
			if len(program.KillWindows) == 0 && len(program.AllowWindows) == 0 {
				alwaysBlocked = append(alwaysBlocked, program.Name)
			} else {
				windowed = append(windowed, program)
			}
		}

		if len(alwaysBlocked) > 0 {
			response.WriteString(fmt.Sprintf("  killed always: %s\n", strings.Join(alwaysBlocked, ", ")))
		}

		for _, program := range windowed {
			hasKill := len(program.KillWindows) > 0
			hasAllow := len(program.AllowWindows) > 0
			switch {
			case hasKill && hasAllow:
				response.WriteString(fmt.Sprintf("  %s: killed %s; otherwise allowed %s, killed outside\n",
					program.Name, formatTimeWindows(program.KillWindows), formatTimeWindows(program.AllowWindows)))
			case hasAllow:
				response.WriteString(fmt.Sprintf("  %s: allowed %s, killed otherwise\n",
					program.Name, formatTimeWindows(program.AllowWindows)))
			default:
				response.WriteString(fmt.Sprintf("  %s: killed %s\n",
					program.Name, formatTimeWindows(program.KillWindows)))
			}
		}
		response.WriteString("\n")
	}

	// Show programs killed on block (browser DNS-cache flush)
	if len(cfg.KillOnBlock) > 0 {
		response.WriteString(fmt.Sprintf("Killed On Block (%d) — terminated on `glocker -block` to flush browser DNS caches:\n",
			len(cfg.KillOnBlock)))
		response.WriteString(fmt.Sprintf("  %s\n\n", strings.Join(cfg.KillOnBlock, ", ")))
	}

	// Show sudoers restrictions
	if cfg.Sudoers.Enabled && len(cfg.Sudoers.AllowWindows) > 0 {
		response.WriteString("Sudoers Restrictions — sudo is PERMITTED during the listed windows, blocked otherwise:\n")
		response.WriteString(fmt.Sprintf("  User: %s\n", cfg.Sudoers.User))
		response.WriteString(fmt.Sprintf("  sudo allowed %s\n\n", formatTimeWindows(cfg.Sudoers.AllowWindows)))
	}

	// Show extension keywords
	if len(cfg.ExtensionKeywords.URLKeywords) > 0 || len(cfg.ExtensionKeywords.ContentKeywords) > 0 {
		response.WriteString("Extension Keywords:\n")

		if len(cfg.ExtensionKeywords.URLKeywords) > 0 {
			response.WriteString(fmt.Sprintf("  URL Keywords (%d): %s\n",
				len(cfg.ExtensionKeywords.URLKeywords),
				strings.Join(cfg.ExtensionKeywords.URLKeywords, ", ")))
		}

		if len(cfg.ExtensionKeywords.ContentKeywords) > 0 {
			response.WriteString(fmt.Sprintf("  Content Keywords (%d): %s\n",
				len(cfg.ExtensionKeywords.ContentKeywords),
				strings.Join(cfg.ExtensionKeywords.ContentKeywords, ", ")))
		}

		if len(cfg.ExtensionKeywords.Whitelist) > 0 {
			response.WriteString(fmt.Sprintf("  Whitelisted: %d domains\n", len(cfg.ExtensionKeywords.Whitelist)))
		}
	}

	response.WriteString("\nEND\n")
	return response.String()
}

// formatTimeWindows converts time windows to a readable string.
func formatTimeWindows(windows []config.TimeWindow) string {
	if len(windows) == 0 {
		return "always"
	}

	var parts []string
	for _, window := range windows {
		days := strings.Join(window.Days, ",")
		parts = append(parts, fmt.Sprintf("%s-%s (%s)", window.Start, window.End, days))
	}
	return strings.Join(parts, "; ")
}

// ProcessReloadRequest reloads the configuration.
func ProcessReloadRequest(cfg *config.Config) {
	slog.Debug("Processing reload request")

	newCfg, err := config.LoadConfig()
	if err != nil {
		log.Printf("ERROR: Failed to reload config: %v", err)
		return
	}

	// Validate new config
	if err := config.ValidateConfig(newCfg); err != nil {
		log.Printf("ERROR: Invalid config: %v", err)
		return
	}

	// Replace config pointer contents
	*cfg = *newCfg

	// Clear domain cache since config changed
	web.ClearDomainCache()

	// Force full enforcement with new config
	enforcement.ForceEnforcement(cfg)

	log.Println("✓ Configuration reloaded successfully")
}

// ProcessUnblockRequest processes a temporary unblock request.
func ProcessUnblockRequest(cfg *config.Config, hostsStr, reason string) error {
	slog.Debug("Processing unblock request", "hosts", hostsStr, "reason", reason)

	// Validate reason against configured valid reasons
	if len(cfg.Unblocking.Reasons) > 0 {
		validReason := false
		for _, validR := range cfg.Unblocking.Reasons {
			if strings.EqualFold(reason, validR) {
				validReason = true
				break
			}
		}
		if !validReason {
			errMsg := fmt.Sprintf("REJECTED: Invalid reason '%s'. Valid reasons: %s",
				reason, strings.Join(cfg.Unblocking.Reasons, ", "))
			log.Println(errMsg)
			return fmt.Errorf("invalid reason: %s (valid reasons: %s)", reason, strings.Join(cfg.Unblocking.Reasons, ", "))
		}
	}

	// Enforce the per-day unblock cap. usedToday counts domain unblocks already
	// recorded in the log during this local calendar day; the in-request counter
	// (unblocked) is added on top so a single multi-domain request can't exceed it.
	maxPerDay := cfg.Unblocking.MaxPerDay
	usedToday := 0
	if maxPerDay > 0 {
		usedToday = countUnblocksToday(cfg)
	}

	hosts := strings.Split(hostsStr, ",")
	unblocked := 0
	rejected := 0
	capRejected := 0
	var rejectedDomains []string
	var unblockedDomains []string

	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}

		// Check if domain can be unblocked using cached enforcement state
		// This avoids reloading the entire config from disk
		canUnblock, inConfig := enforcement.IsUnblockable(host)

		if !canUnblock {
			// Domain is in config but not marked as unblockable - reject
			if inConfig {
				log.Printf("REJECTED UNBLOCK: %s - domain is permanently blocked (not marked as unblockable)", host)
			} else {
				log.Printf("REJECTED UNBLOCK: %s - domain is permanently blocked", host)
			}
			rejected++
			rejectedDomains = append(rejectedDomains, host)
			continue
		}

		// Domain is unblockable or not in config (allow for backward compatibility)

		// Enforce the daily cap before granting.
		if maxPerDay > 0 && usedToday+unblocked >= maxPerDay {
			log.Printf("REJECTED UNBLOCK: %s - daily unblock cap reached (%d/%d used today)", host, usedToday+unblocked, maxPerDay)
			rejected++
			capRejected++
			rejectedDomains = append(rejectedDomains, host)
			continue
		}

		// Add to temporary unblocks
		duration := time.Duration(cfg.Unblocking.TempUnblockTime) * time.Minute
		if duration == 0 {
			duration = 30 * time.Minute
		}
		now := time.Now()
		expiresAt := now.Add(duration)

		state.AddTempUnblock(host, expiresAt)

		// Persist to the unblock log so the per-day cap survives a restart (and so
		// domain unblocks show up in the stats the syncer ships, like -extend does).
		if err := web.LogUnblockEntry(cfg, host, reason, now, expiresAt); err != nil {
			slog.Warn("Failed to record unblock in log", "domain", host, "err", err)
		}

		log.Printf("UNBLOCKED: %s (reason: %s) until %s", host, reason, expiresAt.Format("15:04:05"))
		unblocked++
		unblockedDomains = append(unblockedDomains, host)
	}

	// Log summary
	if unblocked > 0 && rejected > 0 {
		log.Printf("UNBLOCK SUMMARY: %d unblocked (%s), %d rejected (%s)",
			unblocked, strings.Join(unblockedDomains, ", "),
			rejected, strings.Join(rejectedDomains, ", "))
	} else if unblocked > 0 {
		log.Printf("UNBLOCK SUMMARY: %d domain(s) unblocked successfully", unblocked)
	} else if rejected > 0 {
		log.Printf("UNBLOCK SUMMARY: All %d domain(s) rejected - all are permanently blocked", rejected)
	}

	// Force enforcement to apply changes immediately
	if unblocked > 0 {
		enforcement.ForceEnforcement(cfg)
	}

	// Return error if all domains were rejected
	if rejected > 0 && unblocked == 0 {
		if capRejected > 0 {
			return fmt.Errorf("daily unblock limit reached (%d/%d used today); rejected: %s", usedToday, maxPerDay, strings.Join(rejectedDomains, ", "))
		}
		return fmt.Errorf("all domains rejected: %s (permanently blocked, not marked as unblockable)", strings.Join(rejectedDomains, ", "))
	}

	return nil
}

// countUnblocksToday returns how many domain unblocks were recorded in the
// unblock log during the current local calendar day. It backs the
// Unblocking.MaxPerDay cap; a missing or unreadable log counts as zero.
func countUnblocksToday(cfg *config.Config) int {
	entries, err := reports.ParseUnblocksLog(cfg.Unblocking.LogFile)
	if err != nil {
		return 0
	}
	y, m, d := time.Now().Date()
	count := 0
	for _, e := range entries {
		ey, em, ed := e.UnblockTime.Date()
		if ey == y && em == m && ed == d {
			count++
		}
	}
	return count
}

// ProcessBlockRequest adds domains to the block list and persists them to the config file.
func ProcessBlockRequest(cfg *config.Config, hostsStr string) {
	slog.Debug("Processing block request", "hosts", hostsStr)

	var validDomains []string
	hosts := strings.Split(hostsStr, ",")
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if err := config.ValidateDomainName(host); err != nil {
			log.Printf("ERROR: Rejecting domain %q: %v", host, err)
			continue
		}
		validDomains = append(validDomains, host)
	}

	if len(validDomains) == 0 {
		log.Printf("No valid domains to block")
		return
	}

	// Register in memory only — never written to the config file (which
	// `make full-install` regenerates from conf/conf.yaml). ForceEnforcement
	// re-merges these onto the reloaded config on every enforcement cycle, so
	// they last for the life of the daemon but vanish on restart/reinstall.
	runtimeDomains := make([]config.Domain, 0, len(validDomains))
	for _, host := range validDomains {
		runtimeDomains = append(runtimeDomains, config.Domain{Name: host})
		log.Printf("BLOCKED (in-memory): %s", host)
	}
	enforcement.AddRuntimeDomains(runtimeDomains)

	// Force enforcement to apply changes immediately so /etc/hosts has the new
	// entries before we kill browsers — they re-resolve against the updated file.
	enforcement.ForceEnforcement(cfg)

	// Kill configured programs (typically browsers) so their internal DNS caches
	// are dropped and the freshly blocked domains take effect immediately.
	monitoring.KillOnBlock(cfg)
}

// ProcessBlockAppRequest adds programs to the forbidden-programs list so they
// are killed on sight, persisting them to the config file and appending them to
// the in-memory config so the running monitor picks them up on its next tick.
// Programs added this way have no time windows, meaning they are killed 24/7.
func ProcessBlockAppRequest(cfg *config.Config, programsStr string) {
	slog.Debug("Processing block-app request", "programs", programsStr)

	var validNames []string
	for _, name := range strings.Split(programsStr, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := config.ValidateProgramName(name); err != nil {
			log.Printf("ERROR: Rejecting program %q: %v", name, err)
			continue
		}
		validNames = append(validNames, name)
	}

	if len(validNames) == 0 {
		log.Printf("No valid programs to block")
		return
	}

	// Skip names already present in the in-memory config so we don't create
	// duplicate entries that the monitor would scan twice.
	existing := make(map[string]bool, len(cfg.ForbiddenPrograms.Programs))
	for _, p := range cfg.ForbiddenPrograms.Programs {
		existing[p.Name] = true
	}

	// Append to the in-memory config the running monitor goroutine reads — this
	// is what makes newly blocked programs take effect. Kept in memory only,
	// never written to the config file (which `make full-install` regenerates),
	// so it lasts for the life of the daemon but vanishes on restart/reinstall.
	for _, name := range validNames {
		if existing[name] {
			log.Printf("Program %s already blocked, skipping", name)
			continue
		}
		cfg.ForbiddenPrograms.Programs = append(cfg.ForbiddenPrograms.Programs, config.ForbiddenProgram{Name: name})
		existing[name] = true
		log.Printf("APP BLOCKED: %s (killed on sight)", name)
	}

	// If the monitor goroutine was never started (feature disabled), the new
	// entries won't actually be killed until the service restarts with it on.
	if !cfg.ForbiddenPrograms.Enabled {
		log.Printf("WARNING: forbidden_programs is disabled — added programs will not be killed until it is enabled and the service restarts")
	}

	// Kill any matching processes right away rather than waiting for the next
	// monitor tick, so the block feels immediate.
	monitoring.KillForbiddenNow(cfg, validNames)
}

// ProcessExtendRequest grants a one-hour runtime extension for a forbidden
// program marked Extendible in the config. Enforces a rolling-24h cooldown
// per program and logs/emails accountability before returning success.
func ProcessExtendRequest(cfg *config.Config, programName, reason string) error {
	slog.Debug("Processing extend request", "program", programName, "reason", reason)

	programName = strings.TrimSpace(programName)
	reason = strings.TrimSpace(reason)
	if programName == "" {
		return fmt.Errorf("program name cannot be empty")
	}
	if reason == "" {
		return fmt.Errorf("reason cannot be empty")
	}

	// Match against the `name:` field of forbidden programs. Exact (case
	// sensitive) match: the kill matcher itself uses a case-insensitive
	// substring check on `comm`, but the extension is a deliberate grant —
	// we want the user to name the program the same way the config does.
	var matched *config.ForbiddenProgram
	for i, p := range cfg.ForbiddenPrograms.Programs {
		if p.Name == programName {
			matched = &cfg.ForbiddenPrograms.Programs[i]
			break
		}
	}
	if matched == nil {
		return fmt.Errorf("no forbidden program named %q in config", programName)
	}
	if !matched.Extendible {
		return fmt.Errorf("program %q is not marked extendible", programName)
	}

	// Rolling 24h cooldown: any grant in the last 24h blocks a new one.
	if last, ok := state.GetLastExtensionGrant(programName); ok {
		nextAllowed := last.Add(state.ExtensionCooldown)
		if time.Now().Before(nextAllowed) {
			return fmt.Errorf("extension cooldown: next available at %s (last grant %s)",
				nextAllowed.Format("2006-01-02 15:04:05"),
				last.Format("2006-01-02 15:04:05"))
		}
	}

	now := time.Now()
	grant := state.ProgramExtension{
		Program:   programName,
		GrantedAt: now,
		ExpiresAt: now.Add(state.ExtensionDuration),
		Reason:    reason,
	}
	if err := state.AddProgramExtension(grant); err != nil {
		return fmt.Errorf("persist extension: %w", err)
	}

	log.Printf("PROGRAM EXTENDED: %s for %s (reason: %s) until %s",
		programName, state.ExtensionDuration, reason, grant.ExpiresAt.Format("15:04:05"))

	// Reuse the unblock log so the daily report and stats already see this
	// as a deliberate exception. Domain field carries "program:<name>" so
	// it stays distinguishable from domain unblocks.
	if err := web.LogUnblockEntry(cfg, "program:"+programName, reason, grant.GrantedAt, grant.ExpiresAt); err != nil {
		log.Printf("WARN: failed to log program extension: %v", err)
	}

	return nil
}

// ProcessPanicRequest activates panic mode for the specified duration.
func ProcessPanicRequest(cfg *config.Config, minutes int) {
	slog.Debug("Processing panic request", "minutes", minutes)

	now := time.Now()
	panicUntil := now.Add(time.Duration(minutes) * time.Minute)
	state.SetPanicUntil(panicUntil)

	log.Printf("⚠️  PANIC MODE ACTIVATED for %d minutes (until %s)", minutes, panicUntil.Format("15:04:05"))

	// Immediately suspend the system if panic command is configured
	if cfg.PanicCommand != "" {
		// The monitoring goroutine will handle suspension
	}
}
