package install

// The config side of -install. A fresh machine gets the source config. On an
// upgrade the installed config (/etc/glocker/config.yaml and its editable
// includes, such as personal-domains.yaml) is the user's: it's kept unless it's
// unchanged since the last install or the user chooses to replace it, and it's
// converted to the current config_version when older. Every check runs before
// anything is touched, so a failure leaves the machine as it was. Anything that
// changes the installed config is backed up first.

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/renameio/v2"

	"glocker/internal/config"
	"glocker/internal/editor"
)

// keepBackups is how many config backups -install keeps.
const keepBackups = 6

// configPaths are where the installed config, its backups and its checksum
// live (tests point them at a temp dir).
type configPaths struct {
	installed, backups, checksum string
}

var systemConfigPaths = configPaths{config.GlockerConfigFile, config.ConfigBackupDir, config.ConfigChecksumFile}

type configPlan struct {
	paths     configPaths
	installed *editor.Doc // nil on a fresh install
	doc       *editor.Doc // what ends up installed, already migrated
	replace   bool        // doc is the source config, written over the installed one
	from      int         // doc's version before migration
	cfg       *config.Config
}

// planConfig decides what to install and checks it, changing nothing. src is
// the source config (it may not exist: the tarball installer leaves none when
// the user keeps theirs). replaceOK replaces the installed config with src
// without asking; otherwise ask is consulted when the installed config was
// edited since the last install.
func planConfig(src string, replaceOK bool, paths configPaths, ask func(string) bool) (*configPlan, error) {
	p := &configPlan{paths: paths}
	_, srcErr := os.Stat(src)
	if _, err := os.Stat(paths.installed); os.IsNotExist(err) {
		if os.IsNotExist(srcErr) {
			return nil, fmt.Errorf("%s not found — run `glocker -init` (or `make init`) to create one from the sample", src)
		}
		doc, err := editor.Load(src)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", src, err)
		}
		p.doc, p.replace = doc, true
		return p, p.check(src)
	}

	inst, err := editor.Load(paths.installed)
	if err != nil {
		return nil, fmt.Errorf("reading the installed config: %w", err)
	}
	v, err := inst.Version()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", paths.installed, err)
	}
	if v > config.CurrentConfigVersion {
		return nil, fmt.Errorf(`the installed config (%[1]s) is version %[2]d; this glocker reads up to version %[3]d. Downgrades aren't automatic, so nothing was changed.
To go ahead, install a glocker that reads version %[2]d, or move the config aside and install again with a fresh one:
  sudo mv %[1]s %[1]s.v%[2]d`, paths.installed, v, config.CurrentConfigVersion)
	}
	p.installed, p.doc = inst, inst

	if srcErr == nil && !sameFile(src, paths.installed) {
		srcDoc, err := editor.Load(src)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", src, err)
		}
		switch {
		case replaceOK, srcDoc.Fingerprint() == inst.Fingerprint(), unchangedSinceInstall(inst, paths.checksum):
			p.replace = true
		default:
			p.replace = ask(fmt.Sprintf("%s was changed since it was installed (with glocker -edit or by hand).\nKeep it, or replace it with %s? [K/r]: ", paths.installed, src))
		}
		if p.replace {
			p.doc = srcDoc
		}
	}
	name := paths.installed
	if p.replace {
		name = src
	}
	return p, p.check(name)
}

// check converts the chosen config to the current version and validates it.
func (p *configPlan) check(name string) error {
	from, err := p.doc.Migrate()
	p.from = from
	if err != nil {
		return fmt.Errorf(`%[1]s is config version %[2]d and this glocker needs version %[3]d: %[4]v. Nothing was changed.
To go ahead, update the config to version %[3]d by hand (see the release notes) and install again, or move it aside and install again with a fresh one:
  sudo mv %[1]s %[1]s.v%[2]d`, name, from, config.CurrentConfigVersion, err)
	}
	cfg, err := p.doc.Parse()
	if err == nil {
		err = config.ValidateConfig(cfg)
	}
	if err != nil {
		converted := ""
		if from != config.CurrentConfigVersion {
			converted = fmt.Sprintf(" (after converting it from version %d to %d)", from, config.CurrentConfigVersion)
		}
		return fmt.Errorf("%s isn't valid%s: %w\nNothing was changed. Fix it and install again", name, converted, err)
	}
	p.cfg = cfg
	return nil
}

// apply writes the planned config. It returns the backup directory, if it
// took one.
func (p *configPlan) apply() (string, error) {
	changes, err := p.doc.Changes()
	if err != nil {
		return "", err
	}
	backup := ""
	if p.installed != nil && (p.replace || len(changes) > 0) {
		if backup, err = backupConfig(p.installed, p.paths.backups, time.Now()); err != nil {
			return "", fmt.Errorf("backing up the installed config: %w", err)
		}
	}

	if p.replace {
		files, err := p.doc.Contents()
		if err != nil {
			return backup, err
		}
		dir := filepath.Dir(p.paths.installed)
		for _, f := range files {
			dst := p.paths.installed
			if f.File.Rel != "" {
				if filepath.IsAbs(f.File.Rel) {
					continue // lives outside the config dir; nothing to install
				}
				dst = filepath.Join(dir, f.File.Rel)
			}
			mode := os.FileMode(0o644)
			if dst == p.paths.installed {
				mode = 0o600 // the caller opens it to the glocker group
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return backup, err
			}
			if err := renameio.WriteFile(dst, f.New, mode); err != nil {
				return backup, fmt.Errorf("writing %s: %w", dst, err)
			}
			if os.Geteuid() == 0 {
				_ = os.Chown(dst, 0, 0)
			}
		}
	} else if _, err := p.doc.Write(changes, ""); err != nil {
		return backup, err
	}
	return backup, nil
}

// recordChecksum notes the installed config as this install left it.
func recordChecksum(paths configPaths) error {
	d, err := editor.Load(paths.installed)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.checksum), 0o755); err != nil {
		return err
	}
	return renameio.WriteFile(paths.checksum, []byte(d.Fingerprint()+"\n"), 0o600)
}

func unchangedSinceInstall(d *editor.Doc, checksumFile string) bool {
	b, err := os.ReadFile(checksumFile)
	return err == nil && strings.TrimSpace(string(b)) == d.Fingerprint()
}

func sameFile(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

// backupConfig copies the installed config files into a new dated, numbered
// directory under dir (2026-10-30-1, 2026-10-30-2, …) and prunes all but the
// newest keepBackups.
func backupConfig(d *editor.Doc, dir string, now time.Time) (string, error) {
	day := now.Format("2006-01-02")
	n := 1
	for _, b := range listBackups(dir) {
		if b.day == day && b.n >= n {
			n = b.n + 1
		}
	}
	dst := filepath.Join(dir, fmt.Sprintf("%s-%d", day, n))
	files, err := d.Contents()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		data, err := os.ReadFile(f.File.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		rel := filepath.Base(f.File.Path)
		if f.File.Rel != "" && !filepath.IsAbs(f.File.Rel) {
			rel = f.File.Rel
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(out, data, 0o600); err != nil {
			return "", err
		}
	}
	all := listBackups(dir)
	for i := 0; i < len(all)-keepBackups; i++ {
		_ = os.RemoveAll(filepath.Join(dir, all[i].name))
	}
	return dst, nil
}

type backupDir struct {
	name, day string
	n         int
}

// listBackups returns the backup directories under dir, oldest first.
func listBackups(dir string) []backupDir {
	entries, _ := os.ReadDir(dir)
	var out []backupDir
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || len(name) < 12 || name[10] != '-' {
			continue
		}
		if _, err := time.Parse("2006-01-02", name[:10]); err != nil {
			continue
		}
		n, err := strconv.Atoi(name[11:])
		if err != nil {
			continue
		}
		out = append(out, backupDir{name, name[:10], n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].day != out[j].day {
			return out[i].day < out[j].day
		}
		return out[i].n < out[j].n
	})
	return out
}

// askTTY asks on the terminal and reports whether the answer starts with r
// (replace). No terminal, or anything else, keeps the installed config.
func askTTY(q string) bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		log.Println("No terminal to ask on — keeping the installed config.")
		return false
	}
	defer tty.Close()
	fmt.Fprint(tty, q)
	line, _ := bufio.NewReader(tty).ReadString('\n')
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "r")
}
