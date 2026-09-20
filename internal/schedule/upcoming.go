// Package schedule looks a short way into the future and reports which
// enforcement decisions are about to flip. Nothing here enforces anything; it
// exists so `glocker -status` can warn "youtube.com blocks in 7 minutes"
// instead of only describing the present.
package schedule

import (
	"sort"
	"time"

	"glocker/internal/config"
	"glocker/internal/enforcement"
	"glocker/internal/monitoring"
)

// DefaultHorizon is how far ahead -status previews.
const DefaultHorizon = 15 * time.Minute

// step is the scan resolution. Time windows are configured as HH:MM, so nothing
// can flip between whole minutes; temporary unblocks and program extensions
// expire on a wall clock and are rounded up to the next minute boundary.
const step = time.Minute

// Kind says which enforcement mechanism a change belongs to.
type Kind int

const (
	KindDomain Kind = iota
	KindProgram
	KindSudo
)

// Change is one enforcement decision flipping at a known time.
type Change struct {
	At       time.Time
	Kind     Kind
	Name     string // domain or program; empty for sudo
	Blocking bool   // true: enforcement starts, false: it lifts
}

// Verb describes the change in one word, from the user's point of view.
func (c Change) Verb() string {
	switch c.Kind {
	case KindProgram:
		if c.Blocking {
			return "kill"
		}
		return "allow"
	case KindSudo:
		if c.Blocking {
			return "lock"
		}
		return "unlock"
	default:
		if c.Blocking {
			return "block"
		}
		return "unblock"
	}
}

// Label is the thing being changed, for display.
func (c Change) Label() string {
	if c.Kind == KindSudo {
		return "sudo"
	}
	return c.Name
}

// Upcoming returns every enforcement change due within horizon of now, earliest
// first. Only the FIRST flip per domain/program is reported: the point is to
// answer "what is about to happen", not to enumerate a whole schedule.
//
// Each mechanism is asked the same question it answers during enforcement, just
// with a future timestamp, so the preview can't drift from what will actually
// happen. Items whose state can never change inside the horizon (the bulk
// blocklist, which is unconditionally blocked) are skipped without a scan.
func Upcoming(cfg *config.Config, now time.Time, horizon time.Duration) []Change {
	if cfg == nil || horizon <= 0 {
		return nil
	}
	var changes []Change

	if cfg.Domains.Enabled {
		for _, d := range cfg.Domains.List {
			// Permanently blocked and not unblockable: no flip is possible.
			if len(d.BlockWindows) == 0 && !d.Unblockable {
				continue
			}
			blockedAt := func(t time.Time) bool {
				if d.Unblockable && enforcement.IsTempUnblocked(d.Name, t) {
					return false
				}
				return enforcement.IsDomainBlockedAt(d, t)
			}
			if at, blocking, ok := firstChange(now, horizon, blockedAt); ok {
				changes = append(changes, Change{At: at, Kind: KindDomain, Name: d.Name, Blocking: blocking})
			}
		}
	}

	if cfg.Programs.Enabled {
		for _, p := range cfg.Programs.List {
			// Killed 24/7 with no extension available: no flip is possible.
			if len(p.KillWindows) == 0 && len(p.AllowWindows) == 0 && !p.Extendible {
				continue
			}
			killedAt := func(t time.Time) bool { return monitoring.ShouldKill(p, t) }
			if at, blocking, ok := firstChange(now, horizon, killedAt); ok {
				changes = append(changes, Change{At: at, Kind: KindProgram, Name: p.Name, Blocking: blocking})
			}
		}
	}

	if cfg.Sudoers.Enabled {
		lockedAt := func(t time.Time) bool { return !enforcement.IsSudoAllowed(cfg, t) }
		if at, blocking, ok := firstChange(now, horizon, lockedAt); ok {
			changes = append(changes, Change{At: at, Kind: KindSudo, Blocking: blocking})
		}
	}

	sort.Slice(changes, func(i, j int) bool {
		if !changes[i].At.Equal(changes[j].At) {
			return changes[i].At.Before(changes[j].At)
		}
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Name < changes[j].Name
	})
	return changes
}

// firstChange walks minute boundaries from now to now+horizon and returns the
// first one at which state differs from its value now, along with that new
// value. ok is false if nothing changes inside the horizon.
func firstChange(now time.Time, horizon time.Duration, state func(time.Time) bool) (time.Time, bool, bool) {
	current := state(now)
	deadline := now.Add(horizon)
	for t := now.Truncate(step).Add(step); !t.After(deadline); t = t.Add(step) {
		if state(t) != current {
			return t, !current, true
		}
	}
	return time.Time{}, false, false
}
