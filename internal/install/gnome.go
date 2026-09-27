package install

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"glocker/internal/config"
	"syscall"
)

const gnomeExtUUID = "glocker-usage@glocker.app"

// InstallGnomeExtension installs + enables the GNOME Shell window-bridge
// extension for the desktop user, so glocker can track windows on GNOME/Wayland
// (Mutter otherwise blocks it). Best-effort and self-skipping: it does nothing
// on non-GNOME systems, on GNOME older than 45 (this is the ES-module build), or
// when no desktop user is known. On Wayland the user must restart their session
// (the shell can't hot-load a new extension) to activate it.
func InstallGnomeExtension(cfg *config.Config) {
	src := filepath.Join("extensions", "gnome", gnomeExtUUID)
	if _, err := os.Stat(src); err != nil {
		return // not installing from a tree that carries the extension
	}
	if _, err := exec.LookPath("gnome-shell"); err != nil {
		return // not a GNOME system
	}
	if v := gnomeShellMajor(); v > 0 && v < 45 {
		log.Printf("GNOME extension: shell %d is older than 45 (needs a legacy build); skipping", v)
		return
	}
	username := desktopUser(cfg)
	if username == "" {
		log.Println("GNOME extension: no desktop user known (set usage_monitor.user); skipping")
		return
	}
	u, err := user.Lookup(username)
	if err != nil {
		log.Printf("GNOME extension: user %q lookup failed (%v); skipping", username, err)
		return
	}

	dst := filepath.Join(u.HomeDir, ".local", "share", "gnome-shell", "extensions", gnomeExtUUID)
	if err := copyExtensionTo(src, dst, u); err != nil {
		log.Printf("GNOME extension: install failed (%v)", err)
		return
	}
	log.Printf("✓ GNOME window-tracking extension installed for %s", username)

	if err := enableExtensionForUser(username, u.Uid); err != nil {
		log.Printf("  couldn't auto-enable it (%v)", err)
		log.Printf("  enable it in your GNOME session: gnome-extensions enable %s", gnomeExtUUID)
	}
	log.Println("  On Wayland, log out and back in to activate window tracking.")
}

// desktopUser is the user whose GNOME session to target: the configured
// top-level `user`, else whoever ran sudo (available at install time).
func desktopUser(cfg *config.Config) string {
	if cfg.User != "" {
		return cfg.User
	}
	if u := os.Getenv("SUDO_USER"); u != "" && u != "root" {
		return u
	}
	return ""
}

// gnomeShellMajor returns the GNOME Shell major version, or 0 if unknown.
func gnomeShellMajor() int {
	out, err := exec.Command("gnome-shell", "--version").Output()
	if err != nil {
		return 0
	}
	return parseGnomeMajor(string(out))
}

// parseGnomeMajor pulls the major version out of e.g. "GNOME Shell 48.7".
func parseGnomeMajor(s string) int {
	for _, f := range strings.Fields(s) {
		if n, err := strconv.Atoi(strings.SplitN(f, ".", 2)[0]); err == nil {
			return n
		}
	}
	return 0
}

func copyExtensionTo(src, dst string, u *user.User) error {
	// Refuse a pre-planted symlink at dst: MkdirAll would follow it (e.g. dst →
	// /etc) and then root would write the extension files into the target.
	if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to install into a symlink: %s", dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	// This runs as root but writes into a user-owned directory, so a malicious
	// user could pre-plant symlinks. Create dest files with O_NOFOLLOW (so a
	// symlink target isn't overwritten as root) and chown with Lchown (so a
	// symlink target — e.g. /etc/shadow — isn't chowned to the user).
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFileNoFollow(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return filepath.Walk(dst, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}

// copyFileNoFollow copies src→dst, refusing to follow a symlink at dst (a
// pre-planted one would otherwise redirect a root write onto a system file).
func copyFileNoFollow(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = out.ReadFrom(in)
	return err
}

// RemoveGnomeExtension disables and deletes the window-bridge extension for the
// desktop user. Best-effort and self-skipping (mirrors InstallGnomeExtension).
func RemoveGnomeExtension(cfg *config.Config) {
	username := desktopUser(cfg)
	if username == "" {
		return
	}
	u, err := user.Lookup(username)
	if err != nil {
		return
	}
	dst := filepath.Join(u.HomeDir, ".local", "share", "gnome-shell", "extensions", gnomeExtUUID)
	if _, err := os.Stat(dst); err != nil {
		return // not installed for this user
	}
	if err := editEnabledExtensions(username, u.Uid, removeEnabledExtension); err != nil {
		log.Printf("GNOME extension: couldn't disable it (%v)", err)
	}
	if err := os.RemoveAll(dst); err != nil {
		log.Printf("GNOME extension: couldn't remove files (%v)", err)
		return
	}
	log.Printf("✓ GNOME window-tracking extension removed for %s", username)
}

// enableExtensionForUser adds the extension to the user's enabled list via their
// session bus, so it loads on the next GNOME start.
func enableExtensionForUser(username, uid string) error {
	return editEnabledExtensions(username, uid, appendEnabledExtension)
}

// editEnabledExtensions reads the user's enabled-extensions list, applies edit,
// and writes it back if it changed — running gsettings as the user (the
// installer is root) against their session bus, non-interactively.
func editEnabledExtensions(username, uid string, edit func(cur, uuid string) (string, bool)) error {
	bus := "unix:path=/run/user/" + uid + "/bus"
	gs := func(args ...string) (string, error) {
		c := exec.Command("sudo", append([]string{"-n", "-u", username, "gsettings"}, args...)...)
		c.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+bus)
		out, err := c.Output()
		return strings.TrimSpace(string(out)), err
	}
	cur, err := gs("get", "org.gnome.shell", "enabled-extensions")
	if err != nil {
		return fmt.Errorf("read enabled-extensions: %w", err)
	}
	next, changed := edit(cur, gnomeExtUUID)
	if !changed {
		return nil
	}
	if _, err := gs("set", "org.gnome.shell", "enabled-extensions", next); err != nil {
		return fmt.Errorf("set enabled-extensions: %w", err)
	}
	return nil
}

// appendEnabledExtension returns the gsettings list value with uuid added and
// whether it changed.
func appendEnabledExtension(cur, uuid string) (string, bool) {
	items := parseGSettingsList(cur)
	if slices.Contains(items, uuid) {
		return cur, false
	}
	return formatGSettingsList(append(items, uuid)), true
}

// removeEnabledExtension returns the gsettings list value with uuid removed and
// whether it changed.
func removeEnabledExtension(cur, uuid string) (string, bool) {
	items := parseGSettingsList(cur)
	out := make([]string, 0, len(items))
	removed := false
	for _, it := range items {
		if it == uuid {
			removed = true
			continue
		}
		out = append(out, it)
	}
	if !removed {
		return cur, false
	}
	return formatGSettingsList(out), true
}

// parseGSettingsList extracts the quoted elements from a gsettings array value
// like "['a', 'b']" or "@as []". Extension UUIDs never contain single quotes,
// so splitting on "'" is safe.
func parseGSettingsList(s string) []string {
	parts := strings.Split(s, "'")
	var out []string
	for i := 1; i < len(parts); i += 2 {
		out = append(out, parts[i])
	}
	return out
}

// formatGSettingsList renders elements as a gsettings array value; the empty
// list uses the typed form "@as []" that gsettings requires.
func formatGSettingsList(items []string) string {
	if len(items) == 0 {
		return "@as []"
	}
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = "'" + it + "'"
	}
	return "[" + strings.Join(q, ", ") + "]"
}
