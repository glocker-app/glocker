package cli

import (
	"fmt"
	"log"
	"log/slog"
	"sort"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/enforcement"
	"glocker/internal/monitoring"
	"glocker/internal/reports"
	"glocker/internal/schedule"
	"glocker/internal/state"
	"glocker/internal/syncer"
	"glocker/internal/web"
)

// ServiceState is what the caller knows about the daemon when rendering status.
type ServiceState int

const (
	// ServiceOnline: the daemon answered over the socket (server-side render).
	ServiceOnline ServiceState = iota
	// ServiceUnknown: the client couldn't reach the socket — almost always
	// because it lacks root (the socket dir is 0700 root), so the daemon may
	// well be running; we simply can't query it from here.
	ServiceUnknown
	// ServiceDown: we had the rights to reach the socket but nothing was there.
	ServiceDown
)

// Version is the build version surfaced in -status. main stamps it from the
// binary's -ldflags value (git describe); it stays "dev" for a plain `go build`.
var Version = "dev"

// GetStatusResponse returns a formatted runtime status report. svc says what the
// caller knows about the daemon so the service line can't contradict reality
// (e.g. a non-root client that can't reach the socket must not claim "offline").
func GetStatusResponse(cfg *config.Config, svc ServiceState) string {
	now := time.Now()
	f := newFrame("glocker", " · runtime status")

	// Meta.
	f.spacer()
	f.row("time", now.Format("2006-01-02 15:04:05"))
	f.row("version", Version)
	switch svc {
	case ServiceOnline:
		f.row("service", good("●")+" online")
	case ServiceDown:
		f.row("service", alert("○")+" offline"+dim(" · daemon not running"))
	default: // ServiceUnknown
		f.row("service", accent("◌")+" unknown"+dim(" · this binary isn't setuid root — use the installed glocker"))
	}

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
	f.head("domains")
	f.row("blocked", fmt.Sprintf("%d", effectiveBlocked))
	f.row("temp unblocks", fmt.Sprintf("%d", activeUnblocks))
	for _, unblock := range unblocks {
		if now.Before(unblock.ExpiresAt) {
			remaining := unblock.ExpiresAt.Sub(now)
			f.detail(unblock.Domain + dim(fmt.Sprintf("  expires in %v", remaining.Round(time.Minute))))
		}
	}

	// Daily unblock budget (unblocking.max_per_day). Counted off the unblock log,
	// so it resets at local midnight and survives a restart.
	usedToday := countUnblocksToday(cfg)
	if cfg.Domains.Unblocking.MaxPerDay > 0 {
		left := max(0, cfg.Domains.Unblocking.MaxPerDay-usedToday)
		f.row("today", fmt.Sprintf("%d / %d", usedToday, cfg.Domains.Unblocking.MaxPerDay)+dim(fmt.Sprintf("   %d left", left)))
	} else {
		f.row("today", fmt.Sprintf("%d", usedToday)+dim("   no daily cap"))
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
	f.head("programs")
	if cfg.Programs.Enabled {
		f.row("forbidden", good("enforced")+dim(fmt.Sprintf("   %d listed", len(cfg.Programs.List))))
	} else {
		f.row("forbidden", alert("off"))
	}
	f.row("extensions", fmt.Sprintf("%d active", activeExtensions))
	for _, ext := range extensions {
		if now.Before(ext.ExpiresAt) {
			remaining := ext.ExpiresAt.Sub(now)
			f.detail(ext.Program + dim(fmt.Sprintf("  %v · %s", remaining.Round(time.Minute), ext.Reason)))
		}
	}

	// Sudoers restriction state: whether sudo is gated, and if so whether it's
	// currently permitted or blocked (per the allow windows).
	f.head("sudo")
	if !cfg.Sudoers.Enabled {
		f.row("restriction", dim("unrestricted"))
	} else {
		if enforcement.IsSudoAllowed(cfg, now) {
			f.row("access", good("allowed")+dim(" now"))
		} else {
			f.row("access", alert("blocked")+dim(" now"))
		}
		if len(cfg.Sudoers.AllowWindows) > 0 {
			f.detail(dim("allowed " + formatTimeWindows(cfg.Sudoers.AllowWindows)))
		} else {
			f.detail(dim("always blocked — no allow windows"))
		}
	}

	// What's about to change. Time-windowed domains, programs and sudo are
	// invisible in a snapshot of "now" — this looks a quarter-hour ahead so a
	// block that is minutes away is something you can plan around rather than
	// walk into.
	f.section("upcoming", fmt.Sprintf(" · next %dm", int(schedule.DefaultHorizon.Minutes())))
	renderUpcoming(f, schedule.Upcoming(cfg, now, schedule.DefaultHorizon), now)

	// Show violation tracking status
	if cfg.ViolationTracking.Enabled {
		violations := state.GetViolations()
		cutoff := now.Add(-time.Duration(cfg.ViolationTracking.TimeWindowMinutes) * time.Minute)
		recent := make([]state.Violation, 0)
		for _, v := range violations {
			if v.Timestamp.After(cutoff) {
				recent = append(recent, v)
			}
		}

		// Colour the recent count only when it has hit the threshold — otherwise
		// it's just a number.
		recentCount := fmt.Sprintf("%d / %d", len(recent), cfg.ViolationTracking.MaxViolations)
		if cfg.ViolationTracking.MaxViolations > 0 && len(recent) >= cfg.ViolationTracking.MaxViolations {
			recentCount = alertB(recentCount)
		}
		f.head("violations")
		f.row("recent", recentCount+dim(fmt.Sprintf("   %dm window", cfg.ViolationTracking.TimeWindowMinutes)))
		f.row("total", fmt.Sprintf("%d", len(violations)))

		// Show the most recent violations in the active window so the
		// user can see *what* tripped the counter, not just *how many*.
		if len(recent) > 0 {
			const maxShown = 5
			// Sort newest-first. We can't rely on slice order: RecordViolation
			// stamps time.Now() *before* taking the append lock, and violations
			// arrive from independent goroutines (the forbidden-program killer,
			// the /report web handler, content reports), so under a burst they
			// can land out of timestamp order.
			sort.Slice(recent, func(i, j int) bool {
				return recent[i].Timestamp.After(recent[j].Timestamp)
			})
			for i := 0; i < len(recent) && i < maxShown; i++ {
				v := recent[i]
				target := v.Host
				if target == "" {
					target = v.URL
				}
				f.detail(dim(v.Timestamp.Format("15:04:05")) + "  " + v.Type + dim("  "+target))
			}
			if len(recent) > maxShown {
				f.detail(dim(fmt.Sprintf("… and %d more", len(recent)-maxShown)))
			}
		}
	}

	// Show panic mode status
	panicUntil := state.GetPanicUntil()
	if !panicUntil.IsZero() && now.Before(panicUntil) {
		remaining := panicUntil.Sub(now)
		f.head("panic")
		f.row("state", alertB("active")+dim("  ")+alert(fmt.Sprintf("%v remaining", remaining.Round(time.Second))))
	}

	// Show glockpeek sync status.
	f.section("sync", " · glockpeek")
	if !cfg.Sync.Enabled {
		f.row("status", dim("disabled"))
	} else {
		ss := state.GetSyncSummary()
		// A `glocker -resync` backfill in flight — one large push that can take a
		// while; surface it so the status doesn't look idle mid-resync.
		if state.IsResyncing() {
			f.row("resync", good("backfilling…")+dim(" re-pushing local records"))
		}
		// Records sitting in the local logs that the next push will carry.
		pending := formatSyncCounts(syncer.PendingCounts(cfg, ss.Cursors))
		interval := time.Duration(cfg.Sync.IntervalSeconds) * time.Second
		if interval <= 0 {
			interval = time.Duration(config.DefaultSyncIntervalSeconds) * time.Second
		}
		if ss.LastSyncAt.IsZero() {
			f.row("status", "enabled"+dim(" — nothing pushed yet"))
			f.row("pending", pending)
		} else {
			f.row("status", "last push "+dim(fmt.Sprintf("%s ago", now.Sub(ss.LastSyncAt).Round(time.Second))))
			nextIn := ss.LastSyncAt.Add(interval).Sub(now).Round(time.Second)
			if nextIn > 0 {
				f.row("pending", pending+dim(fmt.Sprintf("   next in %s", nextIn)))
			} else {
				f.row("pending", pending+dim("   next imminent"))
			}
		}
		// Whether records are encrypted before upload (end-to-end) or sent as plaintext.
		if cfg.Sync.Encryption() {
			f.row("encryption", good("on")+dim(" — end-to-end before upload"))
		} else {
			f.row("encryption", alert("off")+dim(" — uploaded as plaintext"))
		}
		// Surface the most recent push failure (e.g. a require_encryption 422) so a
		// stuck sync is visible without digging through the journal.
		if ss.LastError != "" {
			f.row("error", alert(ss.LastError)+dim(fmt.Sprintf("   %s ago", now.Sub(ss.LastErrorAt).Round(time.Second))))
		}
	}

	return f.close() + "\nEND\n"
}

// renderUpcoming writes the near-future enforcement changes into the status
// frame: a count, then one line per change with its wall-clock time, what it
// does, and how long away it is. Tightening changes (a block starting, a program
// becoming killable, sudo locking) are red; loosening ones green.
func renderUpcoming(f *frame, changes []schedule.Change, now time.Time) {
	if len(changes) == 0 {
		f.row("changes", dim("none scheduled"))
		return
	}
	f.row("changes", fmt.Sprintf("%d", len(changes)))

	const maxShown = 6
	for i, c := range changes {
		if i == maxShown {
			f.detail(dim(fmt.Sprintf("… and %d more", len(changes)-maxShown)))
			break
		}
		verb := padRight(c.Verb(), 8)
		if c.Blocking {
			verb = alert(verb)
		} else {
			verb = good(verb)
		}
		// Round the countdown UP: a change 6m20s away is "in 7m", never "in 6m",
		// so the number is never optimistic about how long you have. It sits in a
		// fixed column next to the wall clock, leaving the variable-length domain
		// or program name last where it can run long without breaking alignment.
		mins := int((c.At.Sub(now) + time.Minute - 1) / time.Minute)
		f.detail(dim(c.At.Format("15:04")) + dim(fmt.Sprintf("  in %2dm   ", mins)) + verb + c.Label())
	}
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

// GetInfoResponse returns a formatted configuration information report in the
// same framed style as -status (see the frame helper in style.go).
func GetInfoResponse(cfg *config.Config) string {
	f := newFrame("glocker", " · configuration")

	// Domains: totals, then the time-based ones spelled out (capped).
	_, blockedCount, _ := enforcement.GetEnforcementState()
	timeWindowDomains := enforcement.GetTimeWindowDomains()
	timeBasedCount := len(timeWindowDomains)
	alwaysBlockCount := blockedCount - timeBasedCount
	if alwaysBlockCount < 0 {
		alwaysBlockCount = 0
	}
	f.head("domains")
	f.row("blocked", fmt.Sprintf("%d", blockedCount))
	f.row("always", fmt.Sprintf("%d", alwaysBlockCount))
	f.row("time-based", fmt.Sprintf("%d", timeBasedCount)+dim("   blocked only in the listed windows"))
	for i, domain := range timeWindowDomains {
		f.detail(domain.Name + dim("  blocked "+formatTimeWindows(domain.BlockWindows)))
		if i >= 9 && len(timeWindowDomains) > 10 {
			f.detail(dim(fmt.Sprintf("… and %d more", timeBasedCount-10)))
			break
		}
	}

	// Forbidden programs, bucketed. "always" requires both window lists empty;
	// an allow-only program is forbidden outside those windows, not always.
	if cfg.Programs.Enabled && len(cfg.Programs.List) > 0 {
		var alwaysBlocked []string
		var windowed []config.ForbiddenProgram
		for _, program := range cfg.Programs.List {
			if len(program.KillWindows) == 0 && len(program.AllowWindows) == 0 {
				alwaysBlocked = append(alwaysBlocked, program.Name)
			} else {
				windowed = append(windowed, program)
			}
		}

		f.head("programs")
		if len(alwaysBlocked) > 0 {
			f.row("killed always", strings.Join(alwaysBlocked, ", "))
		}
		for _, program := range windowed {
			hasKill := len(program.KillWindows) > 0
			hasAllow := len(program.AllowWindows) > 0
			switch {
			case hasKill && hasAllow:
				f.detail(program.Name + dim("  killed "+formatTimeWindows(program.KillWindows)+
					"; else allowed "+formatTimeWindows(program.AllowWindows)+", killed outside"))
			case hasAllow:
				f.detail(program.Name + dim("  allowed "+formatTimeWindows(program.AllowWindows)+", killed otherwise"))
			default:
				f.detail(program.Name + dim("  killed "+formatTimeWindows(program.KillWindows)))
			}
		}
	}

	// Programs killed on `glocker -block` (browser DNS-cache flush).
	if len(cfg.Domains.KillOnBlock) > 0 {
		f.head("kill on block")
		f.row("programs", strings.Join(cfg.Domains.KillOnBlock, ", "))
		f.detail(dim("terminated on `glocker -block` to flush browser DNS caches"))
	}

	// Sudoers: sudo is permitted only during the listed windows.
	if cfg.Sudoers.Enabled && len(cfg.Sudoers.AllowWindows) > 0 {
		f.head("sudo")
		f.row("user", cfg.User)
		f.row("allowed", formatTimeWindows(cfg.Sudoers.AllowWindows)+dim("   blocked otherwise"))
	}

	// Extension keywords.
	if len(cfg.Content.Keywords.URLKeywords) > 0 || len(cfg.Content.Keywords.ContentKeywords) > 0 {
		f.head("keywords")
		if n := len(cfg.Content.Keywords.URLKeywords); n > 0 {
			f.row(fmt.Sprintf("url (%d)", n), strings.Join(cfg.Content.Keywords.URLKeywords, ", "))
		}
		if n := len(cfg.Content.Keywords.ContentKeywords); n > 0 {
			f.row(fmt.Sprintf("content (%d)", n), strings.Join(cfg.Content.Keywords.ContentKeywords, ", "))
		}
		if n := len(cfg.Content.Keywords.Whitelist); n > 0 {
			f.row("whitelisted", fmt.Sprintf("%d domains", n))
		}
	}

	return f.close() + "\nEND\n"
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

// activeUnblockExists reports whether host already has a temporary unblock that
// hasn't expired, so a repeat request can be skipped instead of burning a slot.
func activeUnblockExists(host string, now time.Time) bool {
	for _, u := range state.GetTempUnblocks() {
		if strings.EqualFold(u.Domain, host) && now.Before(u.ExpiresAt) {
			return true
		}
	}
	return false
}

// ProcessUnblockRequest processes a temporary unblock request.
func ProcessUnblockRequest(cfg *config.Config, hostsStr, reason string) error {
	slog.Debug("Processing unblock request", "hosts", hostsStr, "reason", reason)

	// Validate reason against configured valid reasons
	if len(cfg.Domains.Unblocking.Reasons) > 0 {
		validReason := false
		for _, validR := range cfg.Domains.Unblocking.Reasons {
			if strings.EqualFold(reason, validR) {
				validReason = true
				break
			}
		}
		if !validReason {
			errMsg := fmt.Sprintf("REJECTED: Invalid reason '%s'. Valid reasons: %s",
				reason, strings.Join(cfg.Domains.Unblocking.Reasons, ", "))
			log.Println(errMsg)
			return fmt.Errorf("invalid reason: %s (valid reasons: %s)", reason, strings.Join(cfg.Domains.Unblocking.Reasons, ", "))
		}
	}

	// Enforce the per-day unblock cap. usedToday counts domain unblocks already
	// recorded in the log during this local calendar day; the in-request counter
	// (unblocked) is added on top so a single multi-domain request can't exceed it.
	maxPerDay := cfg.Domains.Unblocking.MaxPerDay
	usedToday := 0
	if maxPerDay > 0 {
		usedToday = countUnblocksToday(cfg)
	}

	now := time.Now()
	hosts := strings.Split(hostsStr, ",")
	unblocked := 0
	rejected := 0
	capRejected := 0
	alreadyActive := 0
	var rejectedDomains []string
	var unblockedDomains []string
	var alreadyActiveDomains []string

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

		// Already actively unblocked → skip. Re-issuing would record another log
		// entry and burn a daily slot for no effect (the domain is already open).
		if activeUnblockExists(host, now) {
			log.Printf("SKIP UNBLOCK: %s already has an active unblock; not consuming a daily slot", host)
			alreadyActive++
			alreadyActiveDomains = append(alreadyActiveDomains, host)
			continue
		}

		// Enforce the daily cap before granting.
		if maxPerDay > 0 && usedToday+unblocked >= maxPerDay {
			log.Printf("REJECTED UNBLOCK: %s - daily unblock cap reached (%d/%d used today)", host, usedToday+unblocked, maxPerDay)
			rejected++
			capRejected++
			rejectedDomains = append(rejectedDomains, host)
			continue
		}

		// Add to temporary unblocks
		duration := time.Duration(cfg.Domains.Unblocking.TempUnblockTime) * time.Minute
		if duration == 0 {
			duration = 30 * time.Minute
		}
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
	if alreadyActive > 0 {
		log.Printf("UNBLOCK: %d domain(s) already unblocked, skipped (no slot used): %s", alreadyActive, strings.Join(alreadyActiveDomains, ", "))
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
	entries, err := reports.ParseUnblocksLog(cfg.Domains.Unblocking.LogFile)
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
	existing := make(map[string]bool, len(cfg.Programs.List))
	for _, p := range cfg.Programs.List {
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
		cfg.Programs.List = append(cfg.Programs.List, config.ForbiddenProgram{Name: name})
		existing[name] = true
		log.Printf("APP BLOCKED: %s (killed on sight)", name)
	}

	// If the monitor goroutine was never started (feature disabled), the new
	// entries won't actually be killed until the service restarts with it on.
	if !cfg.Programs.Enabled {
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
	for i, p := range cfg.Programs.List {
		if p.Name == programName {
			matched = &cfg.Programs.List[i]
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
func ProcessPanicRequest(cfg *config.Config) {
	if !cfg.Panic.Enabled {
		log.Printf("Panic mode is disabled (panic.enabled=false); ignoring -panic request")
		return
	}
	minutes := cfg.Panic.Duration
	if minutes <= 0 {
		minutes = 30 // sensible default when panic.duration is unset
	}

	panicUntil := time.Now().Add(time.Duration(minutes) * time.Minute)
	state.SetPanicUntil(panicUntil)

	log.Printf("⚠️  PANIC MODE ACTIVATED for %d minutes (until %s)", minutes, panicUntil.Format("15:04:05"))
	// The MonitorPanicMode goroutine handles the (re-)suspension.
}
