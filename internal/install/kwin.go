package install

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"

	"glocker/internal/config"
)

const kwinScriptID = "glocker-usage"

// InstallKWinScript installs + enables the KWin window-tracking script for the
// desktop user, so glocker can track windows on KDE Plasma / Wayland (KWin
// otherwise blocks it). Best-effort and self-skipping: it does nothing on
// non-KDE systems or when no desktop user is known. Unlike GNOME, KWin reloads
// scripts live (reconfigure), so no relogin is needed.
func InstallKWinScript(cfg *config.Config) {
	src := filepath.Join("extensions", "kwin", kwinScriptID)
	if _, err := os.Stat(src); err != nil {
		return // not installing from a tree that carries the script
	}
	if !kdeToolsPresent() {
		return // not a KDE/KWin system
	}
	username := desktopUser(cfg)
	if username == "" {
		log.Println("KWin script: no desktop user known (set usage_monitor.user); skipping")
		return
	}
	u, err := user.Lookup(username)
	if err != nil {
		log.Printf("KWin script: user %q lookup failed (%v); skipping", username, err)
		return
	}

	dst := filepath.Join(u.HomeDir, ".local", "share", "kwin", "scripts", kwinScriptID)
	if err := copyExtensionTo(src, dst, u); err != nil {
		log.Printf("KWin script: install failed (%v)", err)
		return
	}
	log.Printf("✓ KWin window-tracking script installed for %s", username)

	if err := enableKWinScriptForUser(username, u.Uid); err != nil {
		log.Printf("  couldn't auto-enable it (%v)", err)
		log.Printf("  enable it: kwriteconfig6 --file kwinrc --group Plugins --key %sEnabled true, then re-login", kwinScriptID)
	}
}

// RemoveKWinScript disables and deletes the KWin script for the desktop user.
// Best-effort and self-skipping (mirrors InstallKWinScript).
func RemoveKWinScript(cfg *config.Config) {
	username := desktopUser(cfg)
	if username == "" {
		return
	}
	u, err := user.Lookup(username)
	if err != nil {
		return
	}
	dst := filepath.Join(u.HomeDir, ".local", "share", "kwin", "scripts", kwinScriptID)
	if _, err := os.Stat(dst); err != nil {
		return // not installed for this user
	}
	if err := setKWinScriptEnabled(username, u.Uid, "false"); err != nil {
		log.Printf("KWin script: couldn't disable it (%v)", err)
	}
	if err := os.RemoveAll(dst); err != nil {
		log.Printf("KWin script: couldn't remove files (%v)", err)
		return
	}
	reconfigureKWin(username, u.Uid) // drop it from the running compositor too
	log.Printf("✓ KWin window-tracking script removed for %s", username)
}

// kdeToolsPresent reports whether this looks like a KDE/KWin system.
func kdeToolsPresent() bool {
	for _, bin := range []string{"kwin_wayland", "kwriteconfig6", "kwriteconfig5"} {
		if _, err := exec.LookPath(bin); err == nil {
			return true
		}
	}
	return false
}

// enableKWinScriptForUser flips the script on in kwinrc and tells the running
// KWin to reload it — both as the user, against their session bus.
func enableKWinScriptForUser(username, uid string) error {
	if err := setKWinScriptEnabled(username, uid, "true"); err != nil {
		return err
	}
	reconfigureKWin(username, uid) // best-effort live reload
	return nil
}

// setKWinScriptEnabled writes [Plugins] <id>Enabled = value into the user's
// kwinrc via kwriteconfig, run as the user.
func setKWinScriptEnabled(username, uid, value string) error {
	tool := "kwriteconfig6"
	if _, err := exec.LookPath(tool); err != nil {
		tool = "kwriteconfig5"
	}
	return runAsUserWithBus(username, uid, tool,
		"--file", "kwinrc", "--group", "Plugins", "--key", kwinScriptID+"Enabled", value)
}

// reconfigureKWin asks the running compositor to re-read its config, loading or
// unloading the script live. Best-effort: on failure the change still takes on
// the next login.
func reconfigureKWin(username, uid string) {
	_ = runAsUserWithBus(username, uid, "gdbus", "call", "--session",
		"--dest", "org.kde.KWin", "--object-path", "/KWin", "--method", "org.kde.KWin.reconfigure")
}

// runAsUserWithBus runs a command as the user (the installer is root) with their
// session-bus address set, non-interactively — the pattern the GNOME enable uses
// for gsettings, here for kwriteconfig/gdbus.
func runAsUserWithBus(username, uid, name string, args ...string) error {
	c := exec.Command("sudo", append([]string{"-n", "-u", username, name}, args...)...)
	c.Env = append(os.Environ(),
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/"+uid+"/bus",
		"XDG_RUNTIME_DIR=/run/user/"+uid)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, string(out))
	}
	return nil
}
