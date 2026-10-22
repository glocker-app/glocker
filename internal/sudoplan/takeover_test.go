package sudoplan

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeSystem builds a fake root. Its Run edits the fake group file for gpasswd
// and, like sudo-rs's visudo, rejects a file without a trailing newline.
func fakeSystem(t *testing.T, osRelease, sudoers, group string, dropIns map[string]string) System {
	t.Helper()
	dir := t.TempDir()
	s := System{
		OSRelease:  filepath.Join(dir, "os-release"),
		Sudoers:    filepath.Join(dir, "sudoers"),
		SudoersDir: filepath.Join(dir, "sudoers.d"),
		GroupFile:  filepath.Join(dir, "group"),
		StateDir:   filepath.Join(dir, "var", "sudo"),
	}
	write := func(p, c string) {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(s.OSRelease, osRelease)
	write(s.Sudoers, sudoers)
	write(s.GroupFile, group)
	if err := os.MkdirAll(s.SudoersDir, 0755); err != nil {
		t.Fatal(err)
	}
	for n, c := range dropIns {
		write(filepath.Join(s.SudoersDir, n), c)
	}
	s.Run = func(name string, args ...string) error {
		switch name {
		case "visudo":
			data, err := os.ReadFile(args[len(args)-1])
			if err != nil {
				return err
			}
			if !strings.HasSuffix(string(data), "\n") {
				return errors.New("missing line terminator at end of file")
			}
			return nil
		case "gpasswd":
			op, user, grp := args[0], args[1], args[2]
			data, _ := os.ReadFile(s.GroupFile)
			var out []string
			for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
				p := strings.Split(l, ":")
				if p[0] == grp {
					var members []string
					if p[3] != "" {
						members = strings.Split(p[3], ",")
					}
					members = slices.DeleteFunc(members, func(m string) bool { return m == user })
					if op == "-a" {
						members = append(members, user)
					}
					p[3] = strings.Join(members, ",")
				}
				out = append(out, strings.Join(p, ":"))
			}
			return os.WriteFile(s.GroupFile, []byte(strings.Join(out, "\n")+"\n"), 0644)
		}
		return errors.New("unexpected command " + name)
	}
	return s
}

const (
	ubuntuRelease  = "ID=ubuntu\nID_LIKE=debian\n"
	fedoraRelease  = "ID=fedora\n"
	omarchyRelease = "ID=omarchy\nID_LIKE=arch\n"
	archRelease    = "ID=arch\n"
	debianRelease  = "ID=debian\n"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDetect(t *testing.T) {
	for rel, want := range map[string]string{
		ubuntuRelease: "ubuntu",
		"ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n": "ubuntu",
		fedoraRelease:                "fedora",
		omarchyRelease:               "omarchy",
		archRelease:                  "arch",
		"ID=manjaro\nID_LIKE=arch\n": "arch",
	} {
		s := fakeSystem(t, rel, "", "", nil)
		p, _, ok := Detect(s)
		if !ok || p.Name != want {
			t.Errorf("Detect(%q) = %q, %v; want %q", rel, p.Name, ok, want)
		}
	}
	for _, rel := range []string{debianRelease, "ID=opensuse-tumbleweed\nID_LIKE=\"opensuse suse\"\n", ""} {
		if p, _, ok := Detect(fakeSystem(t, rel, "", "", nil)); ok {
			t.Errorf("Detect(%q) = %q, want unsupported", rel, p.Name)
		}
	}
}

// Each distro's default first-user setup is taken over and then restored to
// exactly what it was.
func TestTakeoverAndRestorePerDistro(t *testing.T) {
	cases := []struct {
		name, release, sudoers, group string
		dropIns                       map[string]string
		wantGroups, wantFiles         []string
	}{
		{
			name:    "ubuntu cloud image",
			release: ubuntuRelease,
			sudoers: "root ALL=(ALL:ALL) ALL\n%sudo ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n",
			group:   "sudo:x:27:alice\nadm:x:4:alice\n",
			dropIns: map[string]string{
				"90-cloud-init-users": "alice ALL=(ALL) NOPASSWD:ALL\n",
				"README":              "# docs\n",
			},
			wantGroups: []string{"sudo"},
			wantFiles:  []string{"90-cloud-init-users"},
		},
		{
			name:       "fedora",
			release:    fedoraRelease,
			sudoers:    "root ALL=(ALL) ALL\n%wheel ALL=(ALL) ALL\n#includedir /etc/sudoers.d\n",
			group:      "wheel:x:10:alice\n",
			wantGroups: []string{"wheel"},
		},
		{
			name:    "omarchy",
			release: omarchyRelease,
			sudoers: "root ALL=(ALL:ALL) ALL\n# %wheel ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n",
			group:   "wheel:x:998:alice\n",
			dropIns: map[string]string{
				"04_alice":     "alice ALL=(ALL) ALL\n",
				"omarchy-dns":  "%wheel ALL=(root) NOPASSWD: /usr/bin/omarchy-dns DHCP\n",
				"ignored.conf": "alice ALL=(ALL) ALL\n", // sudo skips names with '.'
			},
			wantFiles: []string{"04_alice"}, // wheel is scoped here, so it stays
		},
		{
			name:       "arch, hand install",
			release:    archRelease,
			sudoers:    "root ALL=(ALL:ALL) ALL\n%wheel ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n",
			group:      "wheel:x:998:alice\n",
			wantGroups: []string{"wheel"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := fakeSystem(t, c.release, c.sudoers, c.group, c.dropIns)
			p, _, ok := Detect(s)
			if !ok {
				t.Fatal("unsupported")
			}
			a, err := Survey(s, p, "alice")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(a.Groups, c.wantGroups) || !slices.Equal(a.Files, c.wantFiles) {
				t.Fatalf("survey = groups %v files %v; want %v %v", a.Groups, a.Files, c.wantGroups, c.wantFiles)
			}
			groupBefore := read(t, s.GroupFile)
			if err := Takeover(s, p, "alice", "alice ALL=(ALL) ALL", a); err != nil {
				t.Fatal(err)
			}
			if !Active(s) {
				t.Fatal("not active after takeover")
			}
			if got := read(t, s.dropIn()); !strings.Contains(got, "alice ALL=(ALL) ALL # GLOCKER-MANAGED\n") {
				t.Fatalf("drop-in = %q", got)
			}
			for _, f := range c.wantFiles {
				if _, err := os.Stat(filepath.Join(s.SudoersDir, f)); !os.IsNotExist(err) {
					t.Errorf("%s still in sudoers.d", f)
				}
			}
			if left, err := Survey(s, p, "alice"); err != nil || len(left.Groups)+len(left.Files) > 0 {
				t.Fatalf("after takeover: %+v %v", left, err)
			}
			if c.sudoers != read(t, s.Sudoers) {
				t.Error("main sudoers was edited")
			}

			if err := Restore(s); err != nil {
				t.Fatal(err)
			}
			if Active(s) {
				t.Error("still active after restore")
			}
			if _, err := os.Stat(s.StateDir); !os.IsNotExist(err) {
				t.Error("state dir left behind")
			}
			if _, err := os.Stat(s.dropIn()); !os.IsNotExist(err) {
				t.Error("drop-in left behind")
			}
			if got := userGroups(s.GroupFile, "alice"); !slices.Equal(got, userGroups(writeTemp(t, groupBefore), "alice")) {
				t.Errorf("groups after restore = %v, before %q", got, groupBefore)
			}
			for n, content := range c.dropIns {
				if got := read(t, filepath.Join(s.SudoersDir, n)); got != content {
					t.Errorf("%s after restore = %q, want %q", n, got, content)
				}
			}
		})
	}
}

func writeTemp(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSurveyRefuses(t *testing.T) {
	ubuntu := func(sudoers, group string) System { return fakeSystem(t, ubuntuRelease, sudoers, group, nil) }
	p := Plans[0]
	cases := map[string]System{
		"no includedir":          ubuntu("root ALL=(ALL) ALL\n%sudo ALL=(ALL:ALL) ALL\n", "sudo:x:27:alice\n"),
		"direct line in sudoers": ubuntu("alice ALL=(ALL) ALL\n@includedir /etc/sudoers.d\n", ""),
		"unexpected group":       ubuntu("%admins ALL=(ALL) ALL\n@includedir /etc/sudoers.d\n", "admins:x:5:alice\n"),
	}
	for name, s := range cases {
		if _, err := Survey(s, p, "alice"); err == nil {
			t.Errorf("%s: survey succeeded, want refusal", name)
		}
	}
}

// An upgrade from the old /etc/sudoers rewriting strips glocker's own line.
func TestTakeoverStripsLegacyLine(t *testing.T) {
	s := fakeSystem(t, fedoraRelease,
		"root ALL=(ALL) ALL\n%wheel ALL=(ALL) ALL\n#includedir /etc/sudoers.d\nalice ALL=(ALL) NOPASSWD:ALL # GLOCKER-MANAGED",
		"wheel:x:10:alice\n", nil)
	a, err := Survey(s, Plans[1], "alice")
	if err != nil || !a.LegacyLine {
		t.Fatalf("survey = %+v, %v; want LegacyLine", a, err)
	}
	if err := Takeover(s, Plans[1], "alice", "alice ALL=(ALL) ALL", a); err != nil {
		t.Fatal(err)
	}
	if got := read(t, s.Sudoers); strings.Contains(got, "GLOCKER-MANAGED") || !strings.HasSuffix(got, "\n") {
		t.Fatalf("sudoers = %q", got)
	}
}

// A failure part-way rolls back to the starting state.
func TestTakeoverRollsBack(t *testing.T) {
	s := fakeSystem(t, ubuntuRelease, "%sudo ALL=(ALL:ALL) ALL\n@includedir /etc/sudoers.d\n", "sudo:x:27:alice\n",
		map[string]string{"90-cloud-init-users": "alice ALL=(ALL) NOPASSWD:ALL\n"})
	a, err := Survey(s, Plans[0], "alice")
	if err != nil {
		t.Fatal(err)
	}
	run := s.Run
	s.Run = func(name string, args ...string) error {
		if name == "gpasswd" && args[0] == "-d" {
			return errors.New("gpasswd: boom")
		}
		return run(name, args...)
	}
	if err := Takeover(s, Plans[0], "alice", "alice ALL=(ALL) ALL", a); err == nil {
		t.Fatal("takeover succeeded, want error")
	}
	if Active(s) {
		t.Error("record left after rollback")
	}
	if _, err := os.Stat(s.dropIn()); !os.IsNotExist(err) {
		t.Error("drop-in left after rollback")
	}
	if !slices.Contains(userGroups(s.GroupFile, "alice"), "sudo") {
		t.Error("alice lost sudo group")
	}
}

func TestWriteLineValidates(t *testing.T) {
	s := fakeSystem(t, ubuntuRelease, "", "", nil)
	s.Run = func(string, ...string) error { return errors.New("syntax error") }
	if err := WriteLine(s, "alice ALL=(ALL) ALL"); err == nil {
		t.Fatal("want validation error")
	}
	if _, err := os.Stat(s.dropIn()); !os.IsNotExist(err) {
		t.Error("invalid drop-in was installed")
	}
}
