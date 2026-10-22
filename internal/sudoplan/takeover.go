package sudoplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"glocker/internal/config"
	"glocker/internal/precheck"
	"glocker/internal/utils"
)

// DropInName is glocker's file under sudoers.d. No '.' in it: sudo skips such
// names, which is also why the temp file used while writing it is ignored.
const DropInName = "glocker"

// ErrNotTakenOver means no takeover is recorded, so glocker isn't the user's
// sudo path and writing the drop-in would restrict nothing.
var ErrNotTakenOver = errors.New("sudo window not active: no sudo takeover recorded (re-run glocker -install)")

// System holds the paths and command runner the takeover works against, so
// tests can point it at a fake root.
type System struct {
	OSRelease  string
	Sudoers    string
	SudoersDir string
	GroupFile  string
	StateDir   string // takeover.json + moved/ (mutable state, so under /var)
	// Run executes gpasswd and visudo.
	Run func(name string, args ...string) error
}

// Default is the real system.
func Default() System {
	return System{
		OSRelease:  "/etc/os-release",
		Sudoers:    config.SudoersPath,
		SudoersDir: "/etc/sudoers.d",
		GroupFile:  "/etc/group",
		StateDir:   "/var/lib/glocker/sudo",
		Run: func(name string, args ...string) error {
			out, err := exec.Command(name, args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
}

func (s System) dropIn() string     { return filepath.Join(s.SudoersDir, DropInName) }
func (s System) recordPath() string { return filepath.Join(s.StateDir, "takeover.json") }
func (s System) movedDir() string   { return filepath.Join(s.StateDir, "moved") }

// Record is what a takeover changed, so Restore can undo exactly that.
type Record struct {
	User          string   `json:"user"`
	Plan          string   `json:"plan"`
	RemovedGroups []string `json:"removed_groups,omitempty"`
	// MovedFiles are sudoers.d file names, kept under StateDir/moved/.
	MovedFiles []string `json:"moved_files,omitempty"`
}

// Actions is what a takeover would do on this system right now.
type Actions struct {
	Groups     []string // remove the user from these
	Files      []string // move these sudoers.d files aside
	LegacyLine bool     // strip glocker's old managed line from /etc/sudoers
}

// Empty reports whether there's nothing to take over.
func (a Actions) Empty() bool { return len(a.Groups) == 0 && len(a.Files) == 0 && !a.LegacyLine }

// Describe lists the actions as shell-ish lines for the install prompt.
func (a Actions) Describe(s System, user string) []string {
	var out []string
	for _, g := range a.Groups {
		out = append(out, fmt.Sprintf("gpasswd -d %s %s", user, g))
	}
	for _, f := range a.Files {
		out = append(out, fmt.Sprintf("mv %s %s/", filepath.Join(s.SudoersDir, f), s.movedDir()))
	}
	if a.LegacyLine {
		out = append(out, "remove the old "+config.SudoersMarker+" line from "+s.Sudoers)
	}
	return append(out, "write "+s.dropIn()+" (the sudo window)")
}

var includedirRE = regexp.MustCompile(`(?m)^\s*[@#]includedir\s+/etc/sudoers\.d\s*$`)

// Survey works out what a takeover needs to do for user under plan, or why it
// can't be done.
func Survey(s System, p Plan, user string) (Actions, error) {
	var a Actions
	main, err := os.ReadFile(s.Sudoers)
	if err != nil {
		return a, fmt.Errorf("reading %s: %w", s.Sudoers, err)
	}
	if !includedirRE.Match(main) {
		return a, fmt.Errorf("%s doesn't include /etc/sudoers.d; add `@includedir /etc/sudoers.d` with visudo", s.Sudoers)
	}
	for _, line := range strings.Split(string(main), "\n") {
		if strings.Contains(line, config.SudoersMarker) {
			a.LegacyLine = true
		} else if userBlanket(line, user) {
			return a, fmt.Errorf("%s grants %s sudo directly (%q); glocker doesn't edit that file — remove the line with visudo", s.Sudoers, user, strings.TrimSpace(line))
		}
	}

	blanket := precheck.BlanketSudoGroups(s.Sudoers, s.SudoersDir)
	for _, g := range userGroups(s.GroupFile, user) {
		if !blanket[g] {
			continue
		}
		if !slices.Contains(p.AdminGroups, g) {
			return a, fmt.Errorf("group %q gives %s blanket sudo, which the %s plan doesn't expect; remove %s from it (gpasswd -d %s %s)", g, user, p.Name, user, user, g)
		}
		a.Groups = append(a.Groups, g)
	}

	for _, name := range activeDropIns(s.SudoersDir) {
		data, err := os.ReadFile(filepath.Join(s.SudoersDir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if userBlanket(line, user) {
				a.Files = append(a.Files, name)
				break
			}
		}
	}
	return a, nil
}

// Takeover makes glocker's drop-in the user's only sudo path: it writes the
// drop-in first (so sudo never goes away mid-way), then applies a, recording
// each step. If any other path survives, everything is rolled back.
func Takeover(s System, p Plan, user, line string, a Actions) error {
	if err := WriteLine(s, line); err != nil {
		return err
	}
	rec, err := loadRecord(s)
	if errors.Is(err, os.ErrNotExist) {
		rec = Record{User: user, Plan: p.Name}
	} else if err != nil {
		return err
	}
	if err := saveRecord(s, rec); err != nil {
		return err
	}

	fail := func(err error) error {
		if rerr := Restore(s); rerr != nil {
			return fmt.Errorf("%w (rollback also failed: %v)", err, rerr)
		}
		return err
	}
	for _, g := range a.Groups {
		if err := s.Run("gpasswd", "-d", user, g); err != nil {
			return fail(err)
		}
		rec.RemovedGroups = append(rec.RemovedGroups, g)
		if err := saveRecord(s, rec); err != nil {
			return fail(err)
		}
	}
	if len(a.Files) > 0 {
		if err := os.MkdirAll(s.movedDir(), 0700); err != nil {
			return fail(err)
		}
	}
	for _, f := range a.Files {
		if err := moveFile(filepath.Join(s.SudoersDir, f), filepath.Join(s.movedDir(), f)); err != nil {
			return fail(err)
		}
		rec.MovedFiles = append(rec.MovedFiles, f)
		if err := saveRecord(s, rec); err != nil {
			return fail(err)
		}
	}
	if a.LegacyLine {
		// glocker's own old line; nothing of the user's to give back later.
		if err := stripLegacyLine(s); err != nil {
			return fail(err)
		}
	}

	left, err := Survey(s, p, user)
	if err != nil {
		return fail(err)
	}
	if len(left.Groups) > 0 || len(left.Files) > 0 {
		return fail(fmt.Errorf("%s can still sudo outside glocker after the takeover (groups %v, files %v)", user, left.Groups, left.Files))
	}
	return nil
}

// Restore undoes a recorded takeover: moved files go back, groups are rejoined,
// and glocker's drop-in and record are removed. It's a no-op when nothing is
// recorded.
func Restore(s System) error {
	rec, err := loadRecord(s)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var errs []error
	for _, f := range rec.MovedFiles {
		dst := filepath.Join(s.SudoersDir, f)
		if _, err := os.Stat(dst); err == nil {
			errs = append(errs, fmt.Errorf("%s exists again; left the original in %s", dst, s.movedDir()))
			continue
		}
		if err := moveFile(filepath.Join(s.movedDir(), f), dst); err != nil {
			errs = append(errs, err)
		}
	}
	for _, g := range rec.RemovedGroups {
		if err := s.Run("gpasswd", "-a", rec.User, g); err != nil {
			errs = append(errs, err)
		}
	}
	if err := os.Remove(s.dropIn()); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...) // keep the record so a retry knows what's left
	}
	_ = os.Remove(s.movedDir())
	if err := os.Remove(s.recordPath()); err != nil {
		return err
	}
	_ = os.Remove(s.StateDir)
	return nil
}

// Active reports whether a takeover is recorded.
func Active(s System) bool {
	_, err := os.Stat(s.recordPath())
	return err == nil
}

// WriteLine puts line in glocker's drop-in, validated by visudo first.
func WriteLine(s System, line string) error {
	content := "# The glocker sudo window. Rewritten on schedule; removed by glocker -uninstall.\n" +
		line + " " + config.SudoersMarker + "\n"
	tmp := s.dropIn() + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0440); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	defer os.Remove(tmp)
	if err := s.Run("visudo", "-c", "-f", tmp); err != nil {
		return fmt.Errorf("sudoers validation failed: %w", err)
	}
	if err := os.Rename(tmp, s.dropIn()); err != nil {
		return fmt.Errorf("replacing %s: %w", s.dropIn(), err)
	}
	return nil
}

// stripLegacyLine removes the GLOCKER-MANAGED line older versions kept in the
// main sudoers file.
func stripLegacyLine(s System) error {
	data, err := os.ReadFile(s.Sudoers)
	if err != nil {
		return err
	}
	var kept []string
	for _, l := range strings.Split(string(data), "\n") {
		if !strings.Contains(l, config.SudoersMarker) {
			kept = append(kept, l)
		}
	}
	tmp := s.Sudoers + ".tmp"
	if err := os.WriteFile(tmp, []byte(utils.JoinFileLines(kept)), 0440); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := s.Run("visudo", "-c", "-f", tmp); err != nil {
		return fmt.Errorf("sudoers validation failed: %w", err)
	}
	return os.Rename(tmp, s.Sudoers)
}

// userBlanket reports whether a sudoers line gives user (by name, not group)
// every command. glocker's own marked line doesn't count.
func userBlanket(line, user string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.Contains(t, config.SudoersMarker) {
		return false
	}
	f := strings.Fields(t)
	return len(f) > 1 && f[0] == user && precheck.GrantIsBlanket(t)
}

// userGroups lists the supplementary groups /etc/group puts user in.
func userGroups(groupFile, user string) []string {
	data, err := os.ReadFile(groupFile)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		parts := strings.Split(l, ":")
		if len(parts) < 4 {
			continue
		}
		if slices.Contains(strings.Split(parts[3], ","), user) {
			out = append(out, parts[0])
		}
	}
	return out
}

// activeDropIns lists the sudoers.d files sudo reads: not glocker's own, and
// not names with '.' or a trailing '~', which sudo skips.
func activeDropIns(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || n == DropInName || strings.Contains(n, ".") || strings.HasSuffix(n, "~") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// moveFile copies then removes, since /etc and /var may be on different
// filesystems.
func moveFile(src, dst string) error {
	if err := utils.CopyFile(src, dst); err != nil {
		return fmt.Errorf("moving %s: %w", src, err)
	}
	return os.Remove(src)
}

func loadRecord(s System) (Record, error) {
	var rec Record
	data, err := os.ReadFile(s.recordPath())
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("reading %s: %w", s.recordPath(), err)
	}
	return rec, nil
}

func saveRecord(s System, rec Record) error {
	if err := os.MkdirAll(s.StateDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.recordPath(), append(data, '\n'), 0644)
}
