package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// ExtensionDuration is the fixed duration of a program-allow extension.
const ExtensionDuration = 1 * time.Hour

// ExtensionCooldown is the minimum spacing between two grants for the same
// program (rolling window, not calendar day — closes the midnight-boundary
// hole where two requests could span 90 minutes across calendar days).
const ExtensionCooldown = 24 * time.Hour

// ProgramExtensionsPath is where grants are persisted so a daemon restart
// (accidental or deliberate) does not reset the cooldown. Exposed as a var
// (not a const) so tests can redirect persistence to a temp dir.
var ProgramExtensionsPath = "/var/lib/glocker/extensions.json"

// ProgramExtension is a runtime grant that temporarily allows a forbidden
// program to run, bypassing kill/allow window evaluation until ExpiresAt.
type ProgramExtension struct {
	Program   string    `json:"program"`
	GrantedAt time.Time `json:"granted_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Reason    string    `json:"reason"`
}

var (
	programExtensions      []ProgramExtension
	programExtensionsMutex sync.RWMutex
)

// LoadProgramExtensions hydrates in-memory state from disk. Missing file is
// not an error — it just means no grants have ever been issued.
func LoadProgramExtensions() error {
	data, err := os.ReadFile(ProgramExtensionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read extensions file: %w", err)
	}
	if len(data) == 0 {
		return nil
	}

	var loaded []ProgramExtension
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("parse extensions file: %w", err)
	}

	programExtensionsMutex.Lock()
	defer programExtensionsMutex.Unlock()
	programExtensions = pruneExpiredForCooldown(loaded, time.Now())
	return nil
}

// AddProgramExtension records a new grant and persists the updated list.
// Caller is responsible for cooldown checking via GetLastExtensionGrant.
// GrantedAt/ExpiresAt are stored without a monotonic clock reading so the
// 1-hour active window and 24-hour cooldown are measured in wall-clock time
// (the monotonic clock pauses across system suspend on Linux, which would
// otherwise stretch both windows by the suspend duration).
func AddProgramExtension(ext ProgramExtension) error {
	ext.GrantedAt = ext.GrantedAt.Round(0)
	ext.ExpiresAt = ext.ExpiresAt.Round(0)
	programExtensionsMutex.Lock()
	programExtensions = append(programExtensions, ext)
	programExtensions = pruneExpiredForCooldown(programExtensions, time.Now())
	snapshot := slices.Clone(programExtensions)
	programExtensionsMutex.Unlock()

	return saveProgramExtensions(snapshot)
}

// GetActiveExtension returns the currently-active grant for a program, if
// any. Returns the zero value and false if no unexpired grant exists.
func GetActiveExtension(program string) (ProgramExtension, bool) {
	now := time.Now()
	programExtensionsMutex.RLock()
	defer programExtensionsMutex.RUnlock()
	for _, ext := range programExtensions {
		if ext.Program == program && now.Before(ext.ExpiresAt) {
			return ext, true
		}
	}
	return ProgramExtension{}, false
}

// ExtensionActiveAt reports whether program has a runtime grant still covering
// the instant at. Unlike GetActiveExtension it takes the time as an argument, so
// callers can ask about a future minute (the -status upcoming-changes preview
// needs to know when a grant lapses and the program becomes killable again).
func ExtensionActiveAt(program string, at time.Time) bool {
	programExtensionsMutex.RLock()
	defer programExtensionsMutex.RUnlock()
	for _, ext := range programExtensions {
		if ext.Program == program && at.Before(ext.ExpiresAt) {
			return true
		}
	}
	return false
}

// GetLastExtensionGrant returns the most recent grant time for a program,
// regardless of whether the grant is still active. Used to enforce the
// rolling-24h cooldown.
func GetLastExtensionGrant(program string) (time.Time, bool) {
	programExtensionsMutex.RLock()
	defer programExtensionsMutex.RUnlock()
	var latest time.Time
	for _, ext := range programExtensions {
		if ext.Program == program && ext.GrantedAt.After(latest) {
			latest = ext.GrantedAt
		}
	}
	return latest, !latest.IsZero()
}

// ClearProgramExtensions wipes all in-memory grants. Used by tests to keep
// package-level state from leaking between cases; production callers should
// use AddProgramExtension and let pruning handle cleanup.
func ClearProgramExtensions() {
	programExtensionsMutex.Lock()
	defer programExtensionsMutex.Unlock()
	programExtensions = nil
}

// GetProgramExtensions returns a copy of all retained grants.
func GetProgramExtensions() []ProgramExtension {
	programExtensionsMutex.RLock()
	defer programExtensionsMutex.RUnlock()
	out := make([]ProgramExtension, len(programExtensions))
	copy(out, programExtensions)
	return out
}

// pruneExpiredForCooldown drops grants whose GrantedAt is older than the
// cooldown window — they can no longer affect either active-grant lookups
// (they expired well before that) or cooldown checks.
func pruneExpiredForCooldown(exts []ProgramExtension, now time.Time) []ProgramExtension {
	cutoff := now.Add(-ExtensionCooldown)
	kept := exts[:0]
	for _, ext := range exts {
		if ext.GrantedAt.After(cutoff) {
			kept = append(kept, ext)
		}
	}
	return kept
}

func saveProgramExtensions(exts []ProgramExtension) error {
	if err := os.MkdirAll(filepath.Dir(ProgramExtensionsPath), 0700); err != nil {
		return fmt.Errorf("create extensions dir: %w", err)
	}

	data, err := json.MarshalIndent(exts, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal extensions: %w", err)
	}

	tmp := ProgramExtensionsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write extensions tmp file: %w", err)
	}
	if err := os.Rename(tmp, ProgramExtensionsPath); err != nil {
		return fmt.Errorf("rename extensions file: %w", err)
	}
	return nil
}
