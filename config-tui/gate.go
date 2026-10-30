package main

// Editing needs root, and with sudo control on, root is only reachable inside
// the sudo windows. Without root the editor opens read-only and says when sudo
// next opens, worked out from the config's own sudo windows.

import (
	"fmt"
	"time"

	"glocker/internal/config"
	"glocker/internal/utils"
)

// nextFlip scans forward minute by minute (up to 8 days) for the next time
// sudo availability changes. ok is false if it never changes.
func nextFlip(ws []config.TimeWindow, now time.Time) (allowed bool, at time.Time, ok bool) {
	now = now.Truncate(time.Minute)
	allowed = utils.InAnyWindow(ws, now)
	for t := now.Add(time.Minute); t.Before(now.AddDate(0, 0, 8)); t = t.Add(time.Minute) {
		if utils.InAnyWindow(ws, t) != allowed {
			return allowed, t, true
		}
	}
	return allowed, time.Time{}, false
}

func fmtWhen(t, now time.Time) string {
	clock := t.Format("15:04")
	if t.YearDay() != now.YearDay() || t.Year() != now.Year() {
		clock = t.Format("Mon 15:04")
	}
	d := t.Sub(now).Round(time.Minute)
	return fmt.Sprintf("%s (in %dh %02dm)", clock, int(d.Hours()), int(d.Minutes())%60)
}

// gateBanner is the read-only message for a non-root user.
func gateBanner(d *Doc, now time.Time) string {
	s := path(d.cfg(), "sudoers")
	const pre = "Read-only: editing needs sudo. "
	if !getBool(s, "enabled") {
		return pre + "Run `sudo glocker -edit`."
	}
	allowed, at, ok := nextFlip(windows(s, "allow_windows"), now)
	switch {
	case allowed && ok:
		// at is the first blocked minute; window ends are inclusive.
		return pre + "Sudo is open until " + fmtWhen(at.Add(-time.Minute), now) + ". Run `sudo glocker -edit`."
	case allowed:
		return pre + "Run `sudo glocker -edit`."
	case ok:
		return pre + "Sudo is blocked until " + fmtWhen(at, now) + "."
	default:
		return pre + "This config never allows sudo."
	}
}
