package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const v0Config = `# my config
log_level: "info"
domains:
  enabled: true
  list:
    - !include conf.d/personal-domains.yaml
`

const personal = `- name: "mine.com"
`

// setupConfig writes a config + personal-domains.yaml into dir.
func setupConfig(t *testing.T, dir, main, pers string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "config.yaml"), main)
	write(t, filepath.Join(dir, "conf.d/personal-domains.yaml"), pers)
	return filepath.Join(dir, "config.yaml")
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testPaths(t *testing.T) configPaths {
	root := t.TempDir()
	return configPaths{
		installed: filepath.Join(root, "etc/config.yaml"),
		backups:   filepath.Join(root, "var/config-backups"),
		checksum:  filepath.Join(root, "var/config.sha256"),
	}
}

func noAsk(t *testing.T) func(string) bool {
	return func(q string) bool { t.Fatalf("unexpected prompt: %s", q); return false }
}

func install(t *testing.T, p *configPlan) string {
	t.Helper()
	backup, err := p.apply()
	if err != nil {
		t.Fatal(err)
	}
	if err := recordChecksum(p.paths); err != nil {
		t.Fatal(err)
	}
	return backup
}

func TestPlanMissingSourcePointsToInit(t *testing.T) {
	_, err := planConfig(filepath.Join(t.TempDir(), "conf.yaml"), false, testPaths(t), noAsk(t))
	if err == nil || !strings.Contains(err.Error(), "-init") {
		t.Fatalf("want a pointer to -init, got %v", err)
	}
}

func TestFreshInstallConvertsSourceAndCopiesPersonal(t *testing.T) {
	paths := testPaths(t)
	src := setupConfig(t, t.TempDir(), v0Config, personal)
	p, err := planConfig(src, false, paths, noAsk(t))
	if err != nil {
		t.Fatal(err)
	}
	if b := install(t, p); b != "" {
		t.Errorf("fresh install took a backup: %s", b)
	}
	got := read(t, paths.installed)
	if !strings.HasPrefix(got, "config_version: 1\n# my config\n") {
		t.Errorf("installed config:\n%s", got)
	}
	if read(t, filepath.Join(filepath.Dir(paths.installed), "conf.d/personal-domains.yaml")) != personal {
		t.Error("personal-domains.yaml not installed")
	}
	if read(t, src) != v0Config {
		t.Error("source config was modified")
	}
}

// The tarball upgrade: no source config, an unversioned installed one. It's kept,
// converted in place, backed up first; personal-domains.yaml is untouched.
func TestUpgradeKeepsAndConvertsInstalled(t *testing.T) {
	paths := testPaths(t)
	setupConfig(t, filepath.Dir(paths.installed), v0Config, personal)
	persPath := filepath.Join(filepath.Dir(paths.installed), "conf.d/personal-domains.yaml")

	p, err := planConfig(filepath.Join(t.TempDir(), "conf.yaml"), true, paths, noAsk(t))
	if err != nil {
		t.Fatal(err)
	}
	backup := install(t, p)
	if p.replace || p.from != 0 {
		t.Fatalf("replace=%v from=%d", p.replace, p.from)
	}
	if got := read(t, paths.installed); got != "config_version: 1\n"+v0Config {
		t.Errorf("converted config:\n%s", got)
	}
	if read(t, persPath) != personal {
		t.Error("personal-domains.yaml changed")
	}
	if backup == "" || read(t, filepath.Join(backup, "config.yaml")) != v0Config ||
		read(t, filepath.Join(backup, "conf.d/personal-domains.yaml")) != personal {
		t.Errorf("backup %q missing or wrong", backup)
	}

	// Same version again: nothing to change, no new backup.
	p, err = planConfig(filepath.Join(t.TempDir(), "conf.yaml"), true, paths, noAsk(t))
	if err != nil {
		t.Fatal(err)
	}
	if b := install(t, p); b != "" {
		t.Errorf("unchanged config was backed up again: %s", b)
	}
}

func TestNewerInstalledConfigAborts(t *testing.T) {
	paths := testPaths(t)
	newer := "config_version: 99\n" + v0Config
	setupConfig(t, filepath.Dir(paths.installed), newer, personal)
	src := setupConfig(t, t.TempDir(), v0Config, personal)
	_, err := planConfig(src, true, paths, noAsk(t))
	if err == nil || !strings.Contains(err.Error(), "Downgrades") || !strings.Contains(err.Error(), "sudo mv") {
		t.Fatalf("want a downgrade abort with a way forward, got %v", err)
	}
	if read(t, paths.installed) != newer {
		t.Error("installed config changed")
	}
}

func TestBadVersionAborts(t *testing.T) {
	paths := testPaths(t)
	setupConfig(t, filepath.Dir(paths.installed), "config_version: x\n"+v0Config, personal)
	if _, err := planConfig("", false, paths, noAsk(t)); err == nil {
		t.Fatal("a non-numeric config_version should abort")
	}
}

func TestInvalidConvertedConfigAborts(t *testing.T) {
	paths := testPaths(t)
	bad := v0Config + "sudoers:\n  enabled: true\n" // sudoers needs a user
	setupConfig(t, filepath.Dir(paths.installed), bad, personal)
	_, err := planConfig("", false, paths, noAsk(t))
	if err == nil || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Fatalf("got %v", err)
	}
	if read(t, paths.installed) != bad {
		t.Error("installed config changed")
	}
}

// A source install (make install): the installed config is replaced silently when
// it's what the last install left, and the user is asked when it was edited.
func TestSourceInstallAsksOnlyWhenEdited(t *testing.T) {
	paths := testPaths(t)
	srcDir := t.TempDir()
	src := setupConfig(t, srcDir, v0Config, personal)
	p, err := planConfig(src, false, paths, noAsk(t))
	if err != nil {
		t.Fatal(err)
	}
	install(t, p)

	// Source changes, installed untouched since: replaced without asking.
	write(t, src, strings.Replace(v0Config, `"info"`, `"debug"`, 1))
	p, err = planConfig(src, false, paths, noAsk(t))
	if err != nil {
		t.Fatal(err)
	}
	if !p.replace {
		t.Fatal("unchanged installed config should be replaced")
	}
	if install(t, p) == "" {
		t.Error("replace should back up the old config")
	}

	// Installed edited (as -edit would): asked; keeping leaves it alone.
	edited := strings.Replace(read(t, paths.installed), `"debug"`, `"warn"`, 1)
	write(t, paths.installed, edited)
	asked := false
	p, err = planConfig(src, false, paths, func(string) bool { asked = true; return false })
	if err != nil {
		t.Fatal(err)
	}
	install(t, p)
	if !asked || p.replace || read(t, paths.installed) != edited {
		t.Errorf("asked=%v replace=%v", asked, p.replace)
	}

	// Answering replace installs the source.
	write(t, paths.installed, strings.Replace(edited, `"warn"`, `"error"`, 1))
	p, err = planConfig(src, false, paths, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	install(t, p)
	if !strings.Contains(read(t, paths.installed), `"debug"`) {
		t.Error("replace didn't install the source")
	}
}

func TestBackupsArePrunedToNewest(t *testing.T) {
	paths := testPaths(t)
	setupConfig(t, filepath.Dir(paths.installed), v0Config, personal)
	p, err := planConfig("", false, paths, noAsk(t))
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var last string
	for i := 0; i < keepBackups+3; i++ {
		if last, err = backupConfig(p.installed, paths.backups, day); err != nil {
			t.Fatal(err)
		}
	}
	all := listBackups(paths.backups)
	if len(all) != keepBackups || all[0].name != "2026-10-30-4" || filepath.Base(last) != "2026-10-30-9" {
		t.Errorf("backups = %+v, last %s", all, last)
	}
	// A later day sorts after, even with a lower number.
	if _, err := backupConfig(p.installed, paths.backups, day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	all = listBackups(paths.backups)
	if all[len(all)-1].name != "2026-10-31-1" || all[0].name != "2026-10-30-5" {
		t.Errorf("backups = %+v", all)
	}
}
