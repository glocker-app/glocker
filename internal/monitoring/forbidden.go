package monitoring

import (
	"fmt"
	"log"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
	"glocker/internal/utils"
)

// MonitorForbiddenPrograms continuously monitors and kills forbidden programs based on time windows.
func MonitorForbiddenPrograms(cfg *config.Config) {
	// Set default check interval if not specified
	checkInterval := cfg.Programs.CheckInterval
	if checkInterval == 0 {
		checkInterval = 5 // Default: check every 5 seconds
	}

	slog.Debug("Starting forbidden programs monitoring", "check_interval_seconds", checkInterval, "programs_count", len(cfg.Programs.List))

	ticker := time.NewTicker(time.Duration(checkInterval) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		currentDay := now.Weekday().String()[:3]
		currentTime := now.Format("15:04")

		slog.Debug("Checking for forbidden programs", "current_day", currentDay, "current_time", currentTime)

		for _, program := range cfg.Programs.List {
			if shouldKill(program, now, currentDay, currentTime) {
				killMatchingProcesses(cfg, program.Name)
			}
		}
	}
}

// shouldKill is the full kill decision for a single program: it consults
// the configured windows and then defers to any active runtime extension.
func shouldKill(program config.ForbiddenProgram, now time.Time, currentDay, currentTime string) bool {
	if !isProgramForbidden(program, now, currentDay, currentTime) {
		return false
	}
	if program.Extendible {
		if _, active := state.GetActiveExtension(program.Name); active {
			slog.Debug("Program kept alive by active extension", "program", program.Name)
			return false
		}
	}
	return true
}

// isProgramForbidden decides whether a program should be killed right now.
// Precedence: a matching kill window always wins. If only allow windows are
// set, the program is forbidden outside them. If both lists are empty, the
// program is always forbidden (legacy default).
func isProgramForbidden(program config.ForbiddenProgram, now time.Time, currentDay, currentTime string) bool {
	if inAnyWindow(program.KillWindows, now, currentDay, currentTime) {
		slog.Debug("Program is forbidden in current kill window", "program", program.Name)
		return true
	}

	if len(program.AllowWindows) > 0 {
		if inAnyWindow(program.AllowWindows, now, currentDay, currentTime) {
			return false
		}
		slog.Debug("Program is forbidden outside its allow windows", "program", program.Name)
		return true
	}

	if len(program.KillWindows) == 0 {
		slog.Debug("Program has no windows - blocking completely", "program", program.Name)
		return true
	}

	return false
}

// inAnyWindow reports whether the current time falls inside any of the given
// windows, honoring day-of-week membership and overnight wraparound.
func inAnyWindow(windows []config.TimeWindow, now time.Time, currentDay, currentTime string) bool {
	for _, window := range windows {
		dayToCheck := currentDay
		if window.Start > window.End && currentTime <= window.End {
			// We're in the early-morning portion of a window that started yesterday.
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

// terminateMatching finds processes whose `comm` contains programName
// (case-insensitive) and kills them (TERM, then KILL after a grace period),
// skipping glocker/systemd/kernel/pid 1. It performs no violation tracking,
// notification, or email — callers layer those on. Returns descriptions of the
// processes killed and the number of distinct processes that matched.
func terminateMatching(programName string) (killed []string, matched int) {
	// Use `comm` (kernel task name) for matching — it's the binary basename,
	// never contains spaces, so paths like "/home/user/GOG Games/.../FTL.amd64"
	// don't trip up whitespace-based field parsing the way `ps aux` does.
	cmd := exec.Command("ps", "-eo", "pid=,comm=,args=")
	output, err := cmd.Output()
	if err != nil {
		slog.Debug("Failed to get process list", "error", err)
		return nil, 0
	}

	lines := strings.Split(string(output), "\n")
	processGroups := make(map[string][]state.ProcessInfo)

	slog.Debug("Starting process matching", "program_filter", programName, "total_lines", len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		pid := fields[0]
		comm := fields[1]

		if !strings.Contains(strings.ToLower(comm), strings.ToLower(programName)) {
			continue
		}

		slog.Debug("Extracted process info", "pid", pid, "comm", comm)

		// Don't kill our own process or system processes
		if strings.Contains(strings.ToLower(comm), "glocker") ||
			strings.Contains(strings.ToLower(comm), "systemd") ||
			strings.Contains(strings.ToLower(comm), "kernel") ||
			pid == "1" {
			slog.Debug("Skipping protected process", "pid", pid, "comm", comm)
			continue
		}

		processInfo := state.ProcessInfo{
			PID:         pid,
			Name:        comm,
			CommandLine: line,
		}

		processGroups[pid] = append(processGroups[pid], processInfo)
	}

	matched = len(processGroups)
	for _, processes := range processGroups {
		if len(processes) == 0 {
			continue
		}

		proc := processes[0]
		slog.Debug("Found matching process", "pid", proc.PID, "name", proc.Name)

		// Kill the process
		if err := exec.Command("kill", proc.PID).Run(); err == nil {
			killed = append(killed, fmt.Sprintf("%s (PID: %s)", proc.Name, proc.PID))
			log.Printf("KILLED PROGRAM: %s (PID: %s) - matched filter: %s", proc.Name, proc.PID, programName)

			// Wait then force kill if still running
			time.Sleep(2 * time.Second)
			exec.Command("kill", "-9", proc.PID).Run()
		}
	}

	return killed, matched
}

// killMatchingProcesses finds and kills processes matching the given program
// name, recording a violation and sending accountability notifications.
func killMatchingProcesses(cfg *config.Config, programName string) {
	// Record violation only once per program name (not per subprocess). The
	// matched count is known before any kill is attempted, mirroring the
	// original behavior of recording on detection rather than success.
	_, matched := terminateMatching(programName)
	if matched > 0 && cfg.ViolationTracking.Enabled {
		RecordViolation(cfg, "forbidden_program", programName, fmt.Sprintf("Killed %d process(es)", matched))
	}
}

// KillForbiddenNow immediately kills any processes matching the given program
// names, recording violations and sending accountability notifications just as
// the periodic monitor would. It is used by `glocker -block-app` so a freshly
// blocked program is terminated right away instead of waiting for the next
// monitor tick. Programs added via -block-app have no time windows (killed
// always), so no window check is needed.
func KillForbiddenNow(cfg *config.Config, names []string) {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		killMatchingProcesses(cfg, name)
	}
}

// KillOnBlock terminates the processes listed in cfg.Domains.KillOnBlock. It is meant to
// run right after a domain is blocked: browsers cache DNS internally, so a newly
// blocked domain stays reachable until they restart. Unlike forbidden-program
// kills this ignores time windows and is NOT recorded as a violation — closing a
// browser so it re-reads /etc/hosts is expected behavior, not an infraction.
func KillOnBlock(cfg *config.Config) {
	if len(cfg.Domains.KillOnBlock) == 0 {
		return
	}

	var allKilled []string
	for _, name := range cfg.Domains.KillOnBlock {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		killed, _ := terminateMatching(name)
		allKilled = append(allKilled, killed...)
	}

	if len(allKilled) > 0 {
		log.Printf("KILLED ON BLOCK: terminated %d process(es) to flush DNS caches: %s",
			len(allKilled), strings.Join(allKilled, ", "))
	}
}
