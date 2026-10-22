package enforcement

import (
	"time"

	"glocker/internal/config"
	"glocker/internal/sudoplan"
	"glocker/internal/utils"
)

// UpdateSudoers puts the allowed or blocked line (per the allow windows, or
// blocked when forceBlock) in glocker's /etc/sudoers.d drop-in. It only acts
// once install has recorded a sudo takeover — until then glocker isn't the
// user's sudo path, the drop-in would restrict nothing, and it returns
// sudoplan.ErrNotTakenOver. /etc/sudoers itself is never edited.
func UpdateSudoers(cfg *config.Config, now time.Time, dryRun bool, forceBlock bool) error {
	if !cfg.Sudoers.Enabled && !forceBlock {
		return nil
	}
	sys := sudoplan.Default()
	if !sudoplan.Active(sys) {
		return sudoplan.ErrNotTakenOver
	}

	line := cfg.Sudoers.BlockedSudoersLine
	if !forceBlock && IsSudoAllowed(cfg, now) {
		line = cfg.Sudoers.AllowedSudoersLine
	}
	if dryRun {
		return nil
	}
	return sudoplan.WriteLine(sys, line)
}

// IsSudoAllowed determines if sudo access should be allowed at the given time
// based on the configured time windows.
func IsSudoAllowed(cfg *config.Config, now time.Time) bool {
	if !cfg.Sudoers.Enabled {
		return true // If not enabled, don't restrict
	}

	// Empty allow windows means sudo is never permitted.
	return utils.InAnyWindow(cfg.Sudoers.AllowWindows, now)
}
