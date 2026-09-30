// Package precheck inspects the machine for "bypass routes": the ways a
// determined user could sidestep glocker's enforcement (become root outside the
// sudo window) or recover from it (uninstall when it breaks).
//
// It is strictly READ-ONLY. It reports what it finds and how to close each route
// by hand; it never changes system state. Opt-in lockdown is a separate step.
//
// The framing matters: every bypass route is also a recovery route — the same
// access that defeats glocker is what lets you uninstall it if it breaks. So the
// report is honest about that, and marks the route you should KEEP as your
// break-glass (recovery mode) rather than urging you to close everything.
package precheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Status classifies a finding.
type Status string

const (
	StatusOpen    Status = "OPEN"    // route is available (can bypass / recover)
	StatusClosed  Status = "CLOSED"  // route is restricted
	StatusInfo    Status = "INFO"    // informational, no action implied
	StatusUnknown Status = "UNKNOWN" // couldn't determine
	StatusNA      Status = "N/A"     // component not present
)

// Finding is one inspected route.
type Finding struct {
	ID         string // stable identifier, e.g. "root-password"
	Title      string
	Status     Status
	Detail     string // what was found
	Enables    string // what this route lets someone do
	ManualFix  string // how to close it by hand ("" when it shouldn't be closed)
	BreakGlass bool   // keep this one — it's the recovery route
}

// Options overrides the files each check reads. Empty fields use the real
// system defaults; tests point them at fixtures.
type Options struct {
	ShadowPath     string
	GrubCfgPaths   []string
	SudoersPath    string
	SudoersDir     string
	SSHDConfig     string
	SSHDConfigDir  string
	GroupPath      string
	AutologinPaths []string
	ManagedUser    string // glocker's managed sudo user, excluded from sudoers noise
}

// Report is the full set of findings.
type Report struct {
	Findings []Finding
}

var defaultGrubPaths = []string{
	"/boot/grub/grub.cfg", "/boot/grub2/grub.cfg",
	"/etc/grub.d/40_custom", "/etc/grub.d/01_users",
}

var defaultAutologinPaths = []string{
	"/etc/gdm3/custom.conf", "/etc/gdm/custom.conf",
	"/etc/lightdm/lightdm.conf", "/etc/sddm.conf",
}

func (o Options) withDefaults() Options {
	if o.ShadowPath == "" {
		o.ShadowPath = "/etc/shadow"
	}
	if o.GrubCfgPaths == nil {
		o.GrubCfgPaths = defaultGrubPaths
	}
	if o.SudoersPath == "" {
		o.SudoersPath = "/etc/sudoers"
	}
	if o.SudoersDir == "" {
		o.SudoersDir = "/etc/sudoers.d"
	}
	if o.SSHDConfig == "" {
		o.SSHDConfig = "/etc/ssh/sshd_config"
	}
	if o.SSHDConfigDir == "" {
		o.SSHDConfigDir = "/etc/ssh/sshd_config.d"
	}
	if o.GroupPath == "" {
		o.GroupPath = "/etc/group"
	}
	if o.AutologinPaths == nil {
		o.AutologinPaths = defaultAutologinPaths
	}
	return o
}

// Run inspects the system and returns the findings.
func Run(opts Options) Report {
	o := opts.withDefaults()
	r := Report{}
	r.Findings = append(r.Findings,
		checkRootPassword(o.ShadowPath),
		checkRecoveryMode(o.GrubCfgPaths),
		checkSSHRoot(o.SSHDConfig, o.SSHDConfigDir),
	)
	r.Findings = append(r.Findings, checkManagedUserSudoGroup(o.SudoersPath, o.SudoersDir, o.GroupPath, o.ManagedUser))
	r.Findings = append(r.Findings, checkSudoers(o.SudoersPath, o.SudoersDir, o.ManagedUser))
	r.Findings = append(r.Findings, checkSudoGroup(o.GroupPath, o.ManagedUser))
	r.Findings = append(r.Findings, checkAutologin(o.AutologinPaths))
	return r
}

// checkRootPassword reads /etc/shadow's root entry. A locked root account means
// `su` can't be used to escape the sudo window; a set or empty password means it
// can.
func checkRootPassword(shadowPath string) Finding {
	f := Finding{ID: "root-password", Title: "Root account password (`su`)"}
	data, err := os.ReadFile(shadowPath)
	if err != nil {
		f.Status = StatusUnknown
		f.Detail = "couldn't read " + shadowPath + " (need root): " + err.Error()
		return f
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "root:") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 2 {
			break
		}
		hash := fields[1]
		f.Enables = "become root with `su`, bypassing glocker's sudo window"
		switch {
		case hash == "":
			f.Status = StatusOpen
			f.Detail = "root has NO password — anyone at a shell can `su` to root"
			f.ManualFix = "sudo passwd -l root   (lock the root account)"
		case strings.HasPrefix(hash, "!"), strings.HasPrefix(hash, "*"):
			f.Status = StatusClosed
			f.Detail = "root account is locked; `su` to root won't work"
		default:
			f.Status = StatusOpen
			f.Detail = "root has a password set; `su -` gives root if it's known"
			f.ManualFix = "sudo passwd -l root   (lock the root account)"
		}
		return f
	}
	f.Status = StatusUnknown
	f.Detail = "no root entry found in " + shadowPath
	return f
}

// checkRecoveryMode reports whether the bootloader is password-protected. An
// unprotected GRUB means recovery/single-user mode is reachable — which is the
// break-glass we WANT to keep, so an open result here is framed as "keep it".
func checkRecoveryMode(paths []string) Finding {
	f := Finding{ID: "recovery-mode", Title: "Recovery mode (GRUB)", BreakGlass: true}
	sawAny := false
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sawAny = true
		s := string(data)
		if strings.Contains(s, "password_pbkdf2") || strings.Contains(s, "set superusers") {
			f.Status = StatusClosed
			f.BreakGlass = false
			f.Detail = "GRUB is password-protected (recovery mode gated)"
			f.Enables = "with the GRUB password, boot to a root shell to recover"
			return f
		}
	}
	if !sawAny {
		f.Status = StatusUnknown
		f.BreakGlass = false
		f.Detail = "no GRUB config found; couldn't determine (may not be GRUB)"
		return f
	}
	f.Status = StatusOpen
	f.Detail = "no GRUB password — recovery mode is reachable"
	f.Enables = "boot to a root shell to recover glocker if it breaks (your break-glass)"
	f.ManualFix = "" // deliberately keep this one; don't recommend closing it
	return f
}

// checkSSHRoot reads sshd_config (+ drop-ins) for PermitRootLogin.
func checkSSHRoot(mainCfg, dir string) Finding {
	f := Finding{ID: "ssh-root", Title: "SSH root login"}
	value, found := "", false
	scan := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.EqualFold(fields[0], "PermitRootLogin") {
				value, found = strings.ToLower(fields[1]), true // last wins
			}
		}
	}
	if _, err := os.Stat(mainCfg); err != nil {
		f.Status = StatusNA
		f.Detail = "OpenSSH server not installed"
		return f
	}
	// Drop-ins are read after the main file (they override in OpenSSH).
	scan(mainCfg)
	if entries, err := os.ReadDir(dir); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			scan(filepath.Join(dir, n))
		}
	}
	if !found {
		value = "prohibit-password" // OpenSSH default since 7.0
	}
	f.Enables = "log in as root over the network, outside glocker's reach"
	switch value {
	case "no":
		f.Status = StatusClosed
		f.Detail = "PermitRootLogin no"
	case "yes":
		f.Status = StatusOpen
		f.Detail = "PermitRootLogin yes — root can log in over SSH with a password"
		f.ManualFix = "set 'PermitRootLogin no' in sshd_config, then reload sshd"
	default: // prohibit-password / without-password / forced-commands-only
		f.Status = StatusOpen
		f.Detail = "PermitRootLogin " + value + " — root SSH allowed (key-based)"
		f.ManualFix = "set 'PermitRootLogin no' in sshd_config, then reload sshd"
	}
	return f
}

// checkSudoers surfaces sudo grants other than glocker's own managed line —
// especially NOPASSWD, which sidesteps the whole sudo-window mechanism.
func checkSudoers(mainPath, dir, managedUser string) Finding {
	f := Finding{ID: "sudoers-extra", Title: "Other blanket sudo grants"}
	files := []string{mainPath}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	var grants []string
	nopasswd := false
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "#") || !strings.Contains(t, "ALL=") {
				continue
			}
			if strings.Contains(t, "GLOCKER-MANAGED") {
				continue // glocker's own managed line
			}
			if strings.HasPrefix(t, "root ") || strings.HasPrefix(t, "root\t") {
				continue // root is inherently root — not an escalation route
			}
			if strings.HasPrefix(t, "%") {
				continue // group grants are covered by the per-user + group-member checks
			}
			if managedUser != "" && strings.HasPrefix(t, managedUser+" ") {
				continue // glocker manages this user's own line
			}
			if !grantIsBlanket(t) {
				continue // command-scoped grants can't open a root shell or remove glocker
			}
			if strings.Contains(strings.ToUpper(t), "NOPASSWD") {
				nopasswd = true
			}
			grants = append(grants, t)
		}
	}
	f.Enables = "run commands as root without glocker's window restriction"
	if len(grants) == 0 {
		f.Status = StatusClosed
		f.Detail = "no other user has a blanket sudo grant outside glocker's line"
		return f
	}
	f.Status = StatusOpen
	if nopasswd {
		f.Title += " (incl. NOPASSWD)"
	}
	if len(grants) > 6 {
		grants = append(grants[:6], "… and more")
	}
	f.Detail = "grants found:\n           " + strings.Join(grants, "\n           ")
	f.ManualFix = "review /etc/sudoers and /etc/sudoers.d; remove grants you don't want"
	return f
}

// checkManagedUserSudoGroup is the specific, actionable version of the generic
// sudo-grant list: it reports whether THE MANAGED USER is a member of a group that
// has a blanket sudo grant (`%group … = … ALL`) — which lets them sudo past the
// window regardless of glocker's line — and gives the one command to fix it.
func checkManagedUserSudoGroup(sudoersMain, sudoersDir, groupPath, managedUser string) Finding {
	f := Finding{ID: "user-sudo-group", Title: "Your account can sudo past the window"}
	if managedUser == "" {
		f.Status = StatusUnknown
		f.Detail = "no managed user configured (set the top-level `user`)"
		return f
	}
	f.Enables = "you can sudo to root at any time, ignoring glocker's sudo window"

	blanket := blanketSudoGroups(sudoersMain, sudoersDir)
	var hits []string
	if data, err := os.ReadFile(groupPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Split(line, ":")
			if len(fields) < 4 || !blanket[fields[0]] {
				continue
			}
			for _, m := range strings.Split(fields[3], ",") {
				if strings.TrimSpace(m) == managedUser {
					hits = append(hits, fields[0])
				}
			}
		}
	}
	if len(hits) == 0 {
		f.Status = StatusClosed
		f.Detail = managedUser + " is not in any group with a blanket sudo grant"
		return f
	}
	sort.Strings(hits)
	f.Status = StatusOpen
	f.Detail = fmt.Sprintf("%s is in %s, which grant%s blanket sudo (e.g. %%%s ALL=(ALL) ALL)",
		managedUser, strings.Join(hits, ", "), plural(hits), hits[0])
	fixes := make([]string, len(hits))
	for i, g := range hits {
		fixes[i] = "sudo gpasswd -d " + managedUser + " " + g
	}
	f.ManualFix = strings.Join(fixes, "  ;  ") + "   (then glocker's window is your only sudo path)"
	return f
}

func plural(s []string) string {
	if len(s) == 1 {
		return "s"
	}
	return ""
}

// blanketSudoGroups returns the groups that have an UNRESTRICTED sudo grant
// (`%group … = … ALL`) in sudoers — the ones that fully bypass the window. A
// command-scoped grant (`%group … = … /usr/bin/foo`) is not blanket and is ignored.
func blanketSudoGroups(mainPath, dir string) map[string]bool {
	out := map[string]bool{}
	files := []string{mainPath}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(line)
			if !strings.HasPrefix(t, "%") || !strings.Contains(t, "ALL=") || !grantIsBlanket(t) {
				continue
			}
			name := t[1:]
			if i := strings.IndexAny(name, " \t"); i >= 0 {
				name = name[:i]
			}
			// %group and %:nonunix, and #gid forms — normalise a leading ':' away.
			out[strings.TrimPrefix(name, ":")] = true
		}
	}
	return out
}

// grantIsBlanket reports whether a sudoers line's command spec is exactly ALL
// (optionally behind NOPASSWD:/PASSWD:), i.e. it grants every command.
func grantIsBlanket(line string) bool {
	tail := line
	if i := strings.LastIndex(line, ")"); i >= 0 {
		tail = line[i+1:] // after the (runas) spec, if any
	} else if i := strings.Index(line, "="); i >= 0 {
		tail = line[i+1:]
	}
	tail = strings.TrimSpace(tail)
	tail = strings.TrimSpace(strings.TrimPrefix(tail, "NOPASSWD:"))
	tail = strings.TrimSpace(strings.TrimPrefix(tail, "PASSWD:"))
	return tail == "ALL"
}

// checkSudoGroup lists other members of sudo/wheel (users who can escalate).
func checkSudoGroup(groupPath, managedUser string) Finding {
	f := Finding{ID: "sudo-group", Title: "Other users who can sudo", Status: StatusInfo}
	data, err := os.ReadFile(groupPath)
	if err != nil {
		f.Status = StatusUnknown
		f.Detail = "couldn't read " + groupPath
		return f
	}
	seen := map[string]bool{}
	var others []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		if fields[0] != "sudo" && fields[0] != "wheel" {
			continue
		}
		for _, m := range strings.Split(fields[3], ",") {
			m = strings.TrimSpace(m)
			if m == "" || m == managedUser || m == "root" || seen[m] {
				continue
			}
			seen[m] = true
			others = append(others, m)
		}
	}
	if len(others) == 0 {
		f.Status = StatusClosed
		f.Detail = "no other users in sudo/wheel"
		return f
	}
	sort.Strings(others)
	f.Detail = "sudo/wheel members besides you: " + strings.Join(others, ", ")
	f.Enables = "these accounts can escalate to root independently of glocker"
	return f
}

// checkAutologin reports whether a display manager is set to log a user in
// automatically (physical access then yields a session with no password).
func checkAutologin(paths []string) Finding {
	f := Finding{ID: "autologin", Title: "Automatic login"}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.ToLower(string(data))
		if strings.Contains(s, "automaticloginenable=true") ||
			strings.Contains(s, "autologin-user=") && !strings.Contains(s, "autologin-user=\n") ||
			strings.Contains(s, "[autologin]") {
			f.Status = StatusOpen
			f.Detail = "a display manager is configured for automatic login (" + filepath.Base(p) + ")"
			f.Enables = "physical access yields a logged-in session with no password"
			f.ManualFix = "disable automatic login in your display manager settings"
			return f
		}
	}
	f.Status = StatusClosed
	f.Detail = "no automatic login configured"
	return f
}
