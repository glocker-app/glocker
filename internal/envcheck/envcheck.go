// Package envcheck verifies that the machine actually supports the mechanisms
// glocker relies on — systemd, visudo, a watchdog scheduler, and a filesystem that can
// hold the immutable flag. When one is missing, the matching feature silently
// no-ops (e.g. tamper protection is off, or the watchdog never runs), so this
// surfaces a clear warning at install and at daemon startup instead.
//
// It is read-only apart from a tiny throwaway file used to probe immutability,
// which is removed immediately.
package envcheck

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Result is one capability check.
type Result struct {
	Name     string
	OK       bool
	Detail   string // impact, set only when OK is false
	Required bool   // an enabled glocker feature can't work without this
}

// Check runs every capability probe. systemd is always required (glocker installs
// and runs as a systemd service); visudo is required only when sudoers management
// is enabled (it validates the sudo restriction). A watchdog scheduler (the
// heartbeat watchdog) and the immutable flag (tamper protection) are advisory — missing them degrades
// glocker, but core blocking still works.
func Check(needSudoers bool) []Result {
	systemd := tool("systemd", "systemctl", "glocker installs and runs as a systemd service")
	systemd.Required = true
	visudo := tool("visudo", "visudo", "sudoers changes can't be validated before they're applied")
	visudo.Required = needSudoers
	return []Result{systemd, visudo, watchdogScheduler(), immutable()}
}

// Verify returns an error naming any required capability that's missing, so the
// installer can abort rather than install something that won't work.
func Verify(results []Result) error {
	var missing []string
	for _, r := range results {
		if r.Required && !r.OK {
			missing = append(missing, r.Name+" — "+r.Detail)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing required capabilities:\n  - %s", strings.Join(missing, "\n  - "))
}

// LogAdvisories logs the non-required capabilities that are missing — degraded
// but still functional (no tamper protection, or no heartbeat watchdog).
func LogAdvisories(results []Result, logf func(string, ...any)) {
	for _, r := range results {
		if !r.OK && !r.Required {
			logf("⚠ %s — %s (glocker still works; this feature won't)", r.Name, r.Detail)
		}
	}
}

// LogWarnings logs every missing capability as a warning. Used by the daemon,
// which keeps running regardless — enforcement failing open is worse than a warning.
func LogWarnings(results []Result, logf func(string, ...any)) {
	for _, r := range results {
		if !r.OK {
			logf("⚠ %s — %s", r.Name, r.Detail)
		}
	}
}

func tool(name, bin, impact string) Result {
	if _, err := exec.LookPath(bin); err != nil {
		return Result{Name: name, Detail: impact}
	}
	return Result{Name: name, OK: true}
}

// watchdogScheduler mirrors the installer's scheduler fallback chain (systemd
// timer, then cron): the watchdog is only unavailable when none of them is.
func watchdogScheduler() Result {
	r := Result{Name: "watchdog scheduler (systemd timer or cron)"}
	for _, bin := range []string{"systemctl", "cron", "crond"} {
		if _, err := exec.LookPath(bin); err == nil {
			r.OK = true
			return r
		}
	}
	r.Detail = "the glockdoc heartbeat watchdog won't run"
	return r
}

// immutable probes whether the filesystem under /etc really supports `chattr +i`
// — glocker uses it to protect /etc/hosts, its config, and its binaries. The
// only reliable test is to try it on a throwaway file (removed immediately).
func immutable() Result {
	r := Result{Name: "immutable flag (chattr +i)"}
	if _, err := exec.LookPath("chattr"); err != nil {
		r.Detail = "chattr not found — tamper protection will be inactive"
		return r
	}
	f, err := os.CreateTemp("/etc", ".glocker-fscheck-*")
	if err != nil {
		r.Detail = "couldn't write a probe file to /etc (need root): " + err.Error()
		return r
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if err := exec.Command("chattr", "+i", path).Run(); err != nil {
		r.Detail = "this filesystem can't hold the immutable flag — tamper protection will be inactive"
		return r
	}
	_ = exec.Command("chattr", "-i", path).Run() // must clear before the deferred remove
	r.OK = true
	return r
}
