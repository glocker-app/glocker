// Package schedule looks a short way into the future and reports what is about
// to be taken away: a domain that starts blocking, a program that becomes
// killable, sudo locking. Nothing here enforces anything; it exists so
// `glocker -status` can warn "youtube.com blocks in 7 minutes" instead of only
// describing the present.
//
// Loosening is deliberately NOT reported — when something frees up is what
// `-info` is for, and it isn't something to watch out for.
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

// Change is one enforcement decision tightening at a known time.
type Change struct {
	At   time.Time
	Kind Kind
	Name string // domain or program; empty for sudo
}

// Verb describes the change in one word, from the user's point of view.
func (c Change) Verb() string {
	switch c.Kind {
	case KindProgram:
		return "kill"
	case KindSudo:
		return "lock"
	default:
		return "block"
	}
}

// Label is the thing being changed, for display.
func (c Change) Label() string {
	if c.Kind == KindSudo {
		return "sudo"
	}
	return c.Name
}

// Upcoming returns every enforcement restriction that STARTS within horizon of
// now, earliest first — one entry per domain/program, at its first such moment.
// Something already in force is not reported (there is nothing to warn about),
// and neither is anything loosening.
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
			if at, ok := firstTightening(now, horizon, blockedAt); ok {
				changes = append(changes, Change{At: at, Kind: KindDomain, Name: d.Name})
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
			if at, ok := firstTightening(now, horizon, killedAt); ok {
				changes = append(changes, Change{At: at, Kind: KindProgram, Name: p.Name})
			}
		}
	}

	if cfg.Sudoers.Enabled {
		lockedAt := func(t time.Time) bool { return !enforcement.IsSudoAllowed(cfg, t) }
		if at, ok := firstTightening(now, horizon, lockedAt); ok {
			changes = append(changes, Change{At: at, Kind: KindSudo})
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

// firstTightening walks minute boundaries from now to now+horizon and returns
// the first RISING edge of restricted — the first minute it holds having not
// held the minute before. Looking for the edge rather than the first flip of any
// kind matters: a domain that unblocks in 3 minutes and re-blocks in 10 must
// report the re-block, which filtering "first flip" down to tightenings
// afterwards would have thrown away with the unblock.
func firstTightening(now time.Time, horizon time.Duration, restricted func(time.Time) bool) (time.Time, bool) {
	prev := restricted(now)
	deadline := now.Add(horizon)
	for t := now.Truncate(step).Add(step); !t.After(deadline); t = t.Add(step) {
		cur := restricted(t)
		if cur && !prev {
			return t, true
		}
		prev = cur
	}
	return time.Time{}, false
}
