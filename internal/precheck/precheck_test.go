package precheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRootPassword(t *testing.T) {
	cases := []struct {
		name, line string
		want       Status
	}{
		{"locked-bang", "root:!:19000:0:99999:7:::", StatusClosed},
		{"locked-star", "root:*:19000:0:99999:7:::", StatusClosed},
		{"has-password", "root:$6$abc$def:19000:0:99999:7:::", StatusOpen},
		{"empty-password", "root::19000:0:99999:7:::", StatusOpen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "shadow")
			writeFile(t, p, c.line+"\ndaemon:*:19000::::::\n")
			if got := checkRootPassword(p).Status; got != c.want {
				t.Fatalf("status = %q, want %q", got, c.want)
			}
		})
	}

	t.Run("unreadable", func(t *testing.T) {
		if got := checkRootPassword("/nonexistent/shadow").Status; got != StatusUnknown {
			t.Fatalf("status = %q, want UNKNOWN", got)
		}
	})
}

func TestCheckRecoveryMode(t *testing.T) {
	dir := t.TempDir()
	protected := filepath.Join(dir, "grub-protected.cfg")
	writeFile(t, protected, "set superusers=\"admin\"\npassword_pbkdf2 admin grub.pbkdf2...\n")
	if f := checkRecoveryMode([]string{protected}); f.Status != StatusClosed || f.BreakGlass {
		t.Fatalf("protected: got status=%q breakGlass=%v, want CLOSED/false", f.Status, f.BreakGlass)
	}

	open := filepath.Join(dir, "grub-open.cfg")
	writeFile(t, open, "menuentry 'Linux' { linux /vmlinuz }\n")
	if f := checkRecoveryMode([]string{open}); f.Status != StatusOpen || !f.BreakGlass {
		t.Fatalf("open: got status=%q breakGlass=%v, want OPEN/true", f.Status, f.BreakGlass)
	}

	if f := checkRecoveryMode([]string{filepath.Join(dir, "missing.cfg")}); f.Status != StatusUnknown {
		t.Fatalf("missing: status = %q, want UNKNOWN", f.Status)
	}
}

func TestCheckSSHRoot(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "sshd_config")

	writeFile(t, cfg, "PermitRootLogin no\n")
	if got := checkSSHRoot(cfg, filepath.Join(dir, "none")).Status; got != StatusClosed {
		t.Fatalf("no: status = %q, want CLOSED", got)
	}

	writeFile(t, cfg, "PermitRootLogin yes\n")
	if got := checkSSHRoot(cfg, filepath.Join(dir, "none")).Status; got != StatusOpen {
		t.Fatalf("yes: status = %q, want OPEN", got)
	}

	writeFile(t, cfg, "# nothing set here\n")
	if got := checkSSHRoot(cfg, filepath.Join(dir, "none")).Status; got != StatusOpen {
		t.Fatalf("default: status = %q, want OPEN (prohibit-password)", got)
	}

	// Drop-in overrides the main file (last wins).
	dropDir := filepath.Join(dir, "sshd_config.d")
	writeFile(t, cfg, "PermitRootLogin yes\n")
	writeFile(t, filepath.Join(dropDir, "99-hard.conf"), "PermitRootLogin no\n")
	if got := checkSSHRoot(cfg, dropDir).Status; got != StatusClosed {
		t.Fatalf("dropin: status = %q, want CLOSED", got)
	}

	if got := checkSSHRoot(filepath.Join(dir, "absent"), dir).Status; got != StatusNA {
		t.Fatalf("absent: status = %q, want N/A", got)
	}
}

func TestCheckSudoers(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sudoers")
	sudoersD := filepath.Join(dir, "sudoers.d")

	// root's own line, the glocker-managed line, a group grant, and a command-scoped
	// grant are all NOT bypasses to flag here; only bob's blanket NOPASSWD user grant is.
	writeFile(t, main, "root ALL=(ALL) ALL\nalice ALL=(ALL) ALL # GLOCKER-MANAGED\n%sudo ALL=(ALL:ALL) ALL\n")
	writeFile(t, filepath.Join(sudoersD, "extra"), "bob ALL=(ALL) NOPASSWD:ALL\ncarol ALL=(root) NOPASSWD: /usr/bin/foo\n")

	f := checkSudoers(main, sudoersD, "alice")
	if f.Status != StatusOpen {
		t.Fatalf("status = %q, want OPEN", f.Status)
	}
	if want := "bob ALL=(ALL) NOPASSWD:ALL"; !strings.Contains(f.Detail, want) {
		t.Fatalf("detail missing %q: %q", want, f.Detail)
	}
	for _, notWant := range []string{"root ALL", "%sudo", "carol"} {
		if strings.Contains(f.Detail, notWant) {
			t.Errorf("detail should not flag %q: %q", notWant, f.Detail)
		}
	}
	if !strings.Contains(f.Title, "NOPASSWD") {
		t.Fatalf("title should flag NOPASSWD: %q", f.Title)
	}

	// Clean system: only root, the managed line, and a command-scoped grant.
	writeFile(t, main, "root ALL=(ALL) ALL\nalice ALL=(ALL) ALL # GLOCKER-MANAGED\n")
	if got := checkSudoers(main, filepath.Join(dir, "empty"), "alice").Status; got != StatusClosed {
		t.Fatalf("clean: status = %q, want CLOSED", got)
	}
}

func TestCheckManagedUserSudoGroup(t *testing.T) {
	dir := t.TempDir()
	sudoers := filepath.Join(dir, "sudoers")
	// %sudo grants blanket root; %scoped only a specific command.
	writeFile(t, sudoers, "%sudo ALL=(ALL:ALL) ALL\n%scoped ALL=(root) NOPASSWD: /usr/bin/foo\n")

	// Stub the OS group resolution (the real one hits /etc/passwd + NSS).
	orig := resolveUserGroups
	defer func() { resolveUserGroups = orig }()
	userGroups := map[string][]string{}
	resolveUserGroups = func(u string) ([]string, error) { return userGroups[u], nil }

	// alice is in sudo (blanket) → OPEN, with a gpasswd fix naming her + the group.
	userGroups["alice"] = []string{"alice", "sudo", "scoped"} // primary + secondary
	f := checkManagedUserSudoGroup(sudoers, filepath.Join(dir, "none"), "alice")
	if f.Status != StatusOpen {
		t.Fatalf("status = %q, want OPEN", f.Status)
	}
	if !strings.Contains(f.Detail, "alice is in sudo") {
		t.Errorf("detail should name the user + group: %q", f.Detail)
	}
	if !strings.Contains(f.ManualFix, "gpasswd -d alice sudo") {
		t.Errorf("fix should be the gpasswd command: %q", f.ManualFix)
	}

	// bob is only in the command-scoped group → not a blanket bypass → CLOSED.
	userGroups["bob"] = []string{"bob", "scoped"}
	if got := checkManagedUserSudoGroup(sudoers, filepath.Join(dir, "none"), "bob").Status; got != StatusClosed {
		t.Errorf("bob (command-scoped only): status = %q, want CLOSED", got)
	}
}

func TestCheckSudoGroup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "group")
	writeFile(t, p, "sudo:x:27:alice,bob\nwheel:x:10:carol\nusers:x:100:\n")
	f := checkSudoGroup(p, "alice")
	if f.Status != StatusInfo {
		t.Fatalf("status = %q, want INFO", f.Status)
	}
	for _, want := range []string{"bob", "carol"} {
		if !strings.Contains(f.Detail, want) {
			t.Fatalf("detail missing %q: %q", want, f.Detail)
		}
	}
	if strings.Contains(f.Detail, "alice") {
		t.Fatalf("managed user should be excluded: %q", f.Detail)
	}
}

func TestCheckAutologin(t *testing.T) {
	dir := t.TempDir()
	on := filepath.Join(dir, "custom.conf")
	writeFile(t, on, "[daemon]\nAutomaticLoginEnable=true\nAutomaticLogin=alice\n")
	if got := checkAutologin([]string{on}).Status; got != StatusOpen {
		t.Fatalf("on: status = %q, want OPEN", got)
	}
	off := filepath.Join(dir, "off.conf")
	writeFile(t, off, "[daemon]\n# nothing\n")
	if got := checkAutologin([]string{off}).Status; got != StatusClosed {
		t.Fatalf("off: status = %q, want CLOSED", got)
	}
}

func TestRunOpenCount(t *testing.T) {
	dir := t.TempDir()
	shadow := filepath.Join(dir, "shadow")
	writeFile(t, shadow, "root:!:19000:0:99999:7:::\n") // locked -> closed
	grub := filepath.Join(dir, "grub.cfg")
	writeFile(t, grub, "menuentry 'Linux' {}\n") // open -> break-glass (not counted)
	sshd := filepath.Join(dir, "sshd_config")
	writeFile(t, sshd, "PermitRootLogin yes\n") // open -> counts
	sudoers := filepath.Join(dir, "sudoers")
	writeFile(t, sudoers, "alice ALL=(ALL) ALL # GLOCKER-MANAGED\n")
	group := filepath.Join(dir, "group")
	writeFile(t, group, "sudo:x:27:alice\n")

	r := Run(Options{
		ShadowPath:     shadow,
		GrubCfgPaths:   []string{grub},
		SSHDConfig:     sshd,
		SSHDConfigDir:  filepath.Join(dir, "none"),
		SudoersPath:    sudoers,
		SudoersDir:     filepath.Join(dir, "none"),
		GroupPath:      group,
		AutologinPaths: []string{filepath.Join(dir, "none")},
		ManagedUser:    "alice",
	})
	// Only SSH root login is an enforcement-weakening open route; recovery mode is
	// open but is the break-glass, so OpenCount excludes it.
	if got := r.OpenCount(); got != 1 {
		t.Fatalf("OpenCount = %d, want 1\n%s", got, r.String())
	}
}
