package install

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/envcheck"
	"glocker/internal/precheck"
	"glocker/internal/utils"
	"glocker/internal/web"
)

// ensureGlockerGroup creates the `glocker` group (idempotent) and returns its gid,
// or -1 if it couldn't be created/looked up. Group members reach the daemon socket
// and can read the config for the -status/-info fallback, without being root.
func ensureGlockerGroup() int {
	_ = exec.Command("groupadd", "-f", "glocker").Run()
	g, err := user.LookupGroup("glocker")
	if err != nil {
		return -1
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return -1
	}
	return gid
}

const (
	SystemdServiceFile   = "extras/glocker.service"
	GlockpeekServiceFile = "extras/glockpeek.service"
	// LockBackgroundFile is the source (repo-relative) for the default glocklock
	// background, installed to config.LockBackgroundInstallPath.
	LockBackgroundFile = "extras/lock-background.png"
	GlockpeekServicePath = "/etc/systemd/system/glockpeek.service"
)

// logOpenBypassRoutes runs the bypass/recovery-route audit and logs the OPEN ones
// as warnings, so `glocker -install` surfaces enforcement gaps (wheel/NOPASSWD
// sudo, root login, root SSH, autologin) itself. Full report + fixes come from
// `glocker -check`. Read-only.
func logOpenBypassRoutes(managedUser string) {
	var open []precheck.Finding
	for _, f := range precheck.Run(precheck.Options{ManagedUser: managedUser}).Findings {
		if f.Status == precheck.StatusOpen {
			open = append(open, f)
		}
	}
	if len(open) == 0 {
		log.Println("✓ Bypass audit: no open routes — enforcement can't be trivially sidestepped.")
		return
	}
	log.Printf("⚠ Bypass audit: %d route(s) can defeat enforcement until closed:", len(open))
	for _, f := range open {
		log.Printf("   • %s — %s", f.Title, f.Enables)
		if f.ManualFix != "" {
			log.Printf("     fix: %s", f.ManualFix)
		}
	}
	log.Println("   (full audit incl. recovery routes: sudo glocker -check)")
}

// InstallGlocker performs the complete installation of Glocker on the system.
// This includes copying the binary, config file, setting up systemd service,
// and installing the browser extensions (Firefox and Chrome policies).
// confPath is the source config to install (its directory also holds conf.d/).
// Callers pass a path so the config can live outside the source tree (e.g. a
// private ops repo) while code assets (extras/*.service) stay relative to CWD.
func InstallGlocker(confPath string) error {
	log.Println("╔════════════════════════════════════════════════╗")
	log.Println("║              GLOCKER FULL INSTALL              ║")
	log.Println("╚════════════════════════════════════════════════╝")
	log.Println()

	// Step 1: Validate config file before installation. LoadFile resolves any
	// !include directives (conf.d/ list files) relative to the config's dir first.
	log.Println("Validating configuration file...")
	if _, statErr := os.Stat(confPath); os.IsNotExist(statErr) {
		return fmt.Errorf("%s not found — run `glocker -init` (or `make init`) to create one from the sample", confPath)
	}
	cfgPtr, err := config.LoadFile(confPath)
	if err != nil {
		return fmt.Errorf("reading/parsing %s: %w", confPath, err)
	}
	cfg := *cfgPtr

	if err := config.ValidateConfig(&cfg); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	log.Println("✓ Configuration file is valid")

	// Preflight the host capabilities glocker's enabled features need. Abort if a
	// required one is missing (no systemd to run the service, or no visudo while
	// sudoers management is on) — installing something that won't work is worse
	// than refusing. Merely-degrading gaps (no watchdog scheduler, no immutable-flag
	// support) are warnings.
	envResults := envcheck.Check(cfg.Sudoers.Enabled)
	if err := envcheck.Verify(envResults); err != nil {
		return fmt.Errorf("cannot install: %w", err)
	}
	envcheck.LogAdvisories(envResults, log.Printf)

	// Then the bypass/recovery-route audit (the second half of `glocker -check`),
	// up front so enforcement gaps are visible BEFORE we finish — not left for the
	// user to discover by running the audit later. Read-only; never blocks install
	// (some routes, like recovery mode, you WANT open as break-glass).
	logOpenBypassRoutes(cfg.User)

	// Ensure the log directory exists (and migrate any logs from the old flat
	// /var/log/glocker-* names into it).
	setupLogDir()

	// Step 1b: If a glocker daemon is already running, stop it BEFORE swapping
	// files. Stop→(swap)→start guarantees the new binary actually becomes the
	// running process (a bare `systemctl start` on an already-active unit is a
	// no-op). hosts/sudoers enforcement is unaffected — /etc/hosts stays immutable
	// and sudoers isn't reverted (only -uninstall reverts those), so the only gap
	// is a few seconds of daemon-driven monitoring (forbidden-program killing)
	// during the swap.
	if err := exec.Command("systemctl", "is-active", "--quiet", "glocker.service").Run(); err == nil {
		log.Println("Existing glocker daemon is running — stopping it for in-place upgrade...")
		if err := exec.Command("systemctl", "stop", "glocker.service").Run(); err != nil {
			return fmt.Errorf("failed to stop running daemon before upgrade: %w", err)
		}
		log.Println("✓ Stopped running daemon")
	}

	// Step 2: Get current executable path
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	exePath, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	// The glocker group gates socket access AND lets the managed user read the
	// config for the -status/-info fallback (below), so create it before we set the
	// config's group. Idempotent; the user is added to it later (step 4a).
	glockerGID := ensureGlockerGroup()

	// Step 3: Copy config file from confPath to target location
	log.Printf("Copying config file from %s to %s", confPath, config.GlockerConfigFile)

	// Create config directory if it doesn't exist
	configDir := filepath.Dir(config.GlockerConfigFile)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Copy the config file. Clear any prior immutable flag first — on a reinstall
	// the existing config is `chattr +i`, and CopyFile (os.Create) would fail with
	// EPERM otherwise.
	_ = exec.Command("chattr", "-i", config.GlockerConfigFile).Run()
	if err := utils.CopyFile(confPath, config.GlockerConfigFile); err != nil {
		return fmt.Errorf("failed to copy config file: %w", err)
	}
	log.Printf("✓ Config file copied to %s", config.GlockerConfigFile)

	// The config carries sync.token (a hosted-account bearer credential), so it must
	// not be world-readable. But the managed user needs to read it for `-info` and
	// the `-status` fallback now that the client isn't setuid. So: root:glocker 0640
	// — root + the glocker group (i.e. the managed user, their own token) can read,
	// nobody else. Falls back to root-only 0600 if the group couldn't be created.
	gid, mode := 0, os.FileMode(0o600)
	if glockerGID >= 0 {
		gid, mode = glockerGID, 0o640
	}
	if err := os.Chown(config.GlockerConfigFile, 0, gid); err != nil {
		log.Printf("Warning: couldn't set config file ownership: %v", err)
	}
	if err := os.Chmod(config.GlockerConfigFile, mode); err != nil {
		log.Printf("Warning: couldn't set config file permissions: %v", err)
	}
	if err := exec.Command("chattr", "+i", config.GlockerConfigFile).Run(); err != nil {
		log.Printf("Warning: couldn't set immutable flag on config file: %v", err)
	}

	// Step 3b: Install the generated conf.d/ blocklists that the config pulls in
	// via !include, made immutable like the config itself.
	installConfD(filepath.Join(filepath.Dir(confPath), "conf.d"))

	// Step 4: Install binary. Clear any prior immutable flag, then atomically
	// replace (temp file + rename) so this works even when the old binary is
	// immutable or currently running — a plain copy would fail with "text file
	// busy" (ETXTBSY) on reinstall.
	log.Printf("Installing binary to %s", config.InstallPath)
	_ = exec.Command("chattr", "-i", config.InstallPath).Run()
	if err := utils.ReplaceFile(exePath, config.InstallPath); err != nil {
		return fmt.Errorf("failed to install binary: %w", err)
	}

	// Set ownership to root:root
	if err := os.Chown(config.InstallPath, 0, 0); err != nil {
		log.Printf("Warning: couldn't set ownership to root: %v", err)
	}

	// Plain 0755 — NOT setuid (dropped 2026-09-30). A setuid binary let ANY local
	// user run client commands as root via the socket. Instead the daemon (started
	// by systemd as root) owns the socket root:glocker 0660, the managed user joins
	// the glocker group below, and privileged commands are gated by the caller's real
	// uid (SO_PEERCRED) — so a group member gets self-management commands, and
	// uninstall/resync still require sudo.
	if err := os.Chmod(config.InstallPath, 0o755); err != nil {
		return fmt.Errorf("failed to set binary permissions: %w", err)
	}

	// Set immutable on the installed binary
	if err := exec.Command("chattr", "+i", config.InstallPath).Run(); err != nil {
		log.Printf("Warning: couldn't set immutable flag: %v", err)
	}
	log.Println("✓ Binary installed (0755, not setuid)")

	// Step 4a: Add the managed user to the glocker group (created earlier). Members
	// reach the socket root:glocker 0660 and read the config, so the self-management
	// commands (status, info, unblock, extend, …) work without sudo; uninstall/resync
	// still need real root. NOTE: group membership only takes effect on the user's
	// NEXT login — until then they must use sudo or `newgrp glocker`.
	if cfg.User != "" {
		if err := exec.Command("gpasswd", "-a", cfg.User, "glocker").Run(); err != nil {
			log.Printf("Warning: couldn't add %s to the glocker group: %v", cfg.User, err)
		} else {
			log.Printf("✓ Added %s to the glocker group (log out/in for it to take effect)", cfg.User)
		}
	}

	// Step 4b: Install glocklock binary
	glocklockSource := filepath.Join(filepath.Dir(exePath), "glocklock")
	if _, err := os.Stat(glocklockSource); err == nil {
		log.Printf("Installing glocklock to %s", config.GlocklockInstallPath)
		_ = exec.Command("chattr", "-i", config.GlocklockInstallPath).Run()
		if err := utils.ReplaceFile(glocklockSource, config.GlocklockInstallPath); err != nil {
			log.Printf("Warning: failed to install glocklock binary: %v", err)
		} else {
			// Set ownership to root:root
			if err := os.Chown(config.GlocklockInstallPath, 0, 0); err != nil {
				log.Printf("Warning: couldn't set glocklock ownership to root: %v", err)
			}

			// Set permissions (755, no setuid needed)
			if err := os.Chmod(config.GlocklockInstallPath, 0o755); err != nil {
				log.Printf("Warning: failed to set glocklock permissions: %v", err)
			}

			// Set immutable on the installed binary
			if err := exec.Command("chattr", "+i", config.GlocklockInstallPath).Run(); err != nil {
				log.Printf("Warning: couldn't set immutable flag on glocklock: %v", err)
			}
			log.Println("✓ glocklock binary installed")
		}
	} else {
		log.Printf("Note: glocklock binary not found at %s, skipping", glocklockSource)
	}

	// Step 4b-ii: Install the default lock-screen background (cosmetic; the sample
	// config's violation_tracking.background points at it). Best-effort.
	if _, err := os.Stat(LockBackgroundFile); err == nil {
		if err := os.MkdirAll(filepath.Dir(config.LockBackgroundInstallPath), 0o755); err != nil {
			log.Printf("Warning: couldn't create %s: %v", filepath.Dir(config.LockBackgroundInstallPath), err)
		} else if err := utils.CopyFile(LockBackgroundFile, config.LockBackgroundInstallPath); err != nil {
			log.Printf("Warning: failed to install lock background: %v", err)
		} else {
			_ = os.Chmod(config.LockBackgroundInstallPath, 0o644)
			log.Println("✓ Lock-screen background installed")
		}
	}

	// Step 4c: Install glockpeek binary (no tamper protection - just a log parser)
	glockpeekSource := filepath.Join(filepath.Dir(exePath), "glockpeek")
	if _, err := os.Stat(glockpeekSource); err == nil {
		log.Printf("Installing glockpeek to %s", config.GlockpeekInstallPath)
		// glockpeek runs as its own service, so its binary may be busy on reinstall.
		if err := utils.ReplaceFile(glockpeekSource, config.GlockpeekInstallPath); err != nil {
			log.Printf("Warning: failed to install glockpeek binary: %v", err)
		} else {
			// Set ownership to root:root
			if err := os.Chown(config.GlockpeekInstallPath, 0, 0); err != nil {
				log.Printf("Warning: couldn't set glockpeek ownership to root: %v", err)
			}
			// Set permissions (755)
			if err := os.Chmod(config.GlockpeekInstallPath, 0o755); err != nil {
				log.Printf("Warning: failed to set glockpeek permissions: %v", err)
			}
			log.Println("✓ glockpeek binary installed")
		}
	} else {
		log.Printf("Note: glockpeek binary not found at %s, skipping", glockpeekSource)
	}

	// Step 4d: Install the glockdoc watchdog and schedule it (systemd timer, else
	// cron). Unlike the other sidecars, glockdoc and its schedule intentionally
	// survive uninstall so
	// the gap in heartbeat samples records that glocker was torn down.
	if err := installHeartbeat(&cfg, exePath); err != nil {
		log.Printf("Warning: failed to install heartbeat watchdog: %v", err)
	}

	// Step 5: Install the signed Firefox extension (fetched from the site if not
	// built locally) and its enterprise force-install policy. If the signed xpi
	// can't be obtained, DON'T fail the whole install — enforcement works without
	// the extension — but flag it loudly here and summarise again at the end.
	firefoxExtensionOK := false
	if err := CreateFirefoxExtension(); err != nil {
		log.Println()
		log.Printf("⚠️  Firefox extension NOT installed — couldn't obtain the signed xpi:")
		log.Printf("       %v", err)
		log.Println("⚠️  Firefox content monitoring will be INACTIVE until it's added manually.")
		log.Println()
	} else if err := InstallFirefoxExtension(); err != nil {
		log.Println()
		log.Printf("⚠️  Firefox extension policy NOT installed: %v", err)
		log.Println("⚠️  Firefox content monitoring will be INACTIVE until this is fixed.")
		log.Println()
	} else {
		firefoxExtensionOK = true
	}

	// Step 5a: Chrome-family policies. Chrome fetches the extension itself from
	// glockerapp.com; the policies also close incognito, guest mode and secure
	// DNS, so they go in even if the site can't be reached right now.
	chromeExtensionOK := true
	if err := InstallChromePolicies(); err != nil {
		chromeExtensionOK = false
		log.Println()
		log.Printf("⚠️  Chrome policies NOT fully installed: %v", err)
		log.Println()
	}
	if err := CheckChromeUpdateURL(); err != nil {
		log.Println()
		log.Printf("⚠️  Couldn't reach the Chrome extension: %v", err)
		log.Println("⚠️  Chrome will install it once glockerapp.com is reachable.")
		log.Println()
	}

	// Step 5b: Install the GNOME Shell window-bridge extension (GNOME/Wayland
	// only; self-skips elsewhere) so usage tracking works there too.
	InstallGnomeExtension(&cfg)

	// Step 5c: Take over the user's sudo access so the sudo window applies.
	// Before the service starts, since the daemon writes the drop-in.
	takeOverSudo(&cfg)

	// Step 6: Install systemd service
	servicePath := "/etc/systemd/system/glocker.service"
	log.Println("Installing systemd service...")
	// Lift the immutable flag first (set at the end of a prior install) so the
	// copy doesn't fail with EPERM on a reinstall.
	_ = exec.Command("chattr", "-i", servicePath).Run()
	if err := utils.CopyFile(SystemdServiceFile, servicePath); err != nil {
		return fmt.Errorf("failed to create service file: %w", err)
	}

	// Reload systemd daemon
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	// Enable service
	if err := exec.Command("systemctl", "enable", "glocker.service").Run(); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	// Start (or restart) the service. `restart` rather than `start` so the new
	// binary is guaranteed to run even if the unit is somehow still active here —
	// a bare `start` on an active unit is a silent no-op.
	if err := exec.Command("systemctl", "restart", "glocker.service").Run(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	// Protect service file
	exec.Command("chattr", "+i", servicePath).Run()

	log.Println("✓ Systemd service installed and started")

	// Step 6b: Install the standalone glockpeek dashboard service. It's an
	// unprivileged localhost reader, so it isn't made immutable — clean to
	// remove on uninstall.
	log.Println("Installing glockpeek dashboard service...")
	if err := utils.CopyFile(GlockpeekServiceFile, GlockpeekServicePath); err != nil {
		log.Printf("Warning: failed to install glockpeek service: %v", err)
	} else {
		exec.Command("systemctl", "daemon-reload").Run()
		if err := exec.Command("systemctl", "enable", "--now", "glockpeek.service").Run(); err != nil {
			log.Printf("Warning: failed to enable/start glockpeek service: %v", err)
		} else {
			log.Println("✓ glockpeek dashboard service installed and started")
		}
	}

	// Log the installation event
	if err := web.LogInstallEntry(); err != nil {
		log.Printf("Warning: Failed to log install entry: %v", err)
	}

	log.Println()
	log.Println("🎉 Installation complete!")
	log.Println("   Run 'glocker -status' to check the current status.")
	log.Println("   Re-run 'sudo glocker -check' anytime to re-audit bypass routes + capabilities.")

	if !chromeExtensionOK {
		log.Println()
		log.Println("⚠️  Chrome policies are NOT fully installed (see the warning above).")
	}
	if !firefoxExtensionOK {
		log.Println()
		log.Println("⚠️  Firefox content monitoring is NOT active.")
		log.Println("    The browser extension couldn't be installed (see the warning above).")
		log.Println("    Everything else — domain blocking, forbidden programs, sudoers — still works.")
		log.Println("    To enable it, follow \"Firefox extension (manual install)\" in the README.")
	}

	return nil
}

// installConfD copies the generated conf.d/ blocklist files (the ones the config
// pulls in with !include) to /etc/glocker/conf.d/ and makes them immutable, so
// they can't be quietly emptied to unblock. No-op if conf/conf.d/ is absent
// (a config with only inline domains). Best-effort — warnings, not failures.
func installConfD(srcDir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: couldn't read %s: %v", srcDir, err)
		}
		return
	}
	if err := os.MkdirAll(config.GlockerConfDDir, 0o755); err != nil {
		log.Printf("Warning: couldn't create %s: %v", config.GlockerConfDDir, err)
		return
	}
	installed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		dst := filepath.Join(config.GlockerConfDDir, e.Name())
		_ = exec.Command("chattr", "-i", dst).Run() // clear any prior flag (reinstall)
		if err := utils.CopyFile(filepath.Join(srcDir, e.Name()), dst); err != nil {
			log.Printf("Warning: couldn't install %s: %v", dst, err)
			continue
		}
		_ = os.Chown(dst, 0, 0)
		_ = exec.Command("chattr", "+i", dst).Run()
		installed++
	}
	if installed > 0 {
		log.Printf("✓ Installed %d conf.d blocklist file(s) to %s", installed, config.GlockerConfDDir)
	}
}

// setupLogDir creates the log directory (root-owned) and, one time, moves any
// logs from the old flat /var/log/glocker-* names into it. Best-effort: a failure
// only means the daemon recreates the files on first write.
func setupLogDir() {
	if err := os.MkdirAll(config.LogDir, 0o755); err != nil {
		log.Printf("Warning: couldn't create log dir %s: %v", config.LogDir, err)
		return
	}
	_ = os.Chown(config.LogDir, 0, 0)
	moves := map[string]string{
		"/var/log/glocker-unblocks.log":    config.DefaultUnblocksLogFile,
		"/var/log/glocker-reports.log":     config.DefaultReportsLogFile,
		"/var/log/glocker-lifecycle.log":   config.DefaultLifecycleLogFile,
		"/var/log/glocker-usage.jsonl":     config.DefaultUsageLogFile,
		"/var/log/glocker-heartbeat.jsonl": config.DefaultHeartbeatLogFile,
	}
	for old, dst := range moves {
		if _, err := os.Stat(old); err != nil {
			continue // nothing to move
		}
		if _, err := os.Stat(dst); err == nil {
			continue // destination already exists; leave the old file in place
		}
		if err := os.Rename(old, dst); err != nil {
			log.Printf("Warning: couldn't move %s -> %s: %v", old, dst, err)
		} else {
			log.Printf("Moved %s -> %s", old, dst)
		}
	}
}

// installHeartbeat installs the glockdoc watchdog binary and schedules it on a
// fixed cadence. All parameters are baked into the scheduled command line as
// flags so the watchdog never depends on the config file (removed on
// uninstall). The binary and its schedule are deliberately left in place by
// uninstall so the watchdog keeps recording the resulting downtime.
//
// Scheduling is pluggable: each mechanism in watchdogSchedulers is tried in
// order and the first that works wins; the others are cleared so a reinstall
// that switches mechanism never runs the watchdog twice.
func installHeartbeat(cfg *config.Config, exePath string) error {
	if !cfg.Heartbeat.Enabled {
		log.Println("Heartbeat watchdog disabled in config, skipping")
		return nil
	}

	src := filepath.Join(filepath.Dir(exePath), "glockdoc")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("glockdoc binary not found at %s (run 'make build-all'): %w", src, err)
	}

	if err := utils.CopyFile(src, config.GlockdocInstallPath); err != nil {
		return fmt.Errorf("copying glockdoc binary: %w", err)
	}
	if err := os.Chown(config.GlockdocInstallPath, 0, 0); err != nil {
		log.Printf("Warning: couldn't set glockdoc ownership to root: %v", err)
	}
	if err := os.Chmod(config.GlockdocInstallPath, 0o755); err != nil {
		return fmt.Errorf("setting glockdoc permissions: %w", err)
	}
	log.Println("✓ glockdoc binary installed")

	// Resolve parameters through the config's resolvers (the one source of the
	// defaults).
	interval := int(cfg.Heartbeat.Interval() / time.Minute)
	logFile := cfg.Heartbeat.LogFile
	if logFile == "" {
		logFile = config.DefaultHeartbeatLogFile
	}
	cmdLine := fmt.Sprintf("%s -socket %s -log %s -timeout %s",
		config.GlockdocInstallPath, config.GlockerSock, logFile, cfg.Heartbeat.Timeout())

	var failures []string
	for i, s := range watchdogSchedulers {
		if err := s.install(interval, cmdLine); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", s.name, err))
			continue
		}
		for j, other := range watchdogSchedulers {
			if j != i {
				other.remove()
			}
		}
		log.Printf("✓ Heartbeat watchdog scheduled every %d min via %s", interval, s.name)
		return nil
	}
	return fmt.Errorf("no scheduler could run the watchdog (%s)", strings.Join(failures, "; "))
}

// watchdogScheduler is one way of running glockdoc periodically.
type watchdogScheduler struct {
	name    string
	install func(intervalMin int, cmdLine string) error
	remove  func() // best-effort, idempotent
}

// watchdogSchedulers in priority order. A systemd timer comes first because
// glocker already requires systemd; cron is the fallback.
var watchdogSchedulers = []watchdogScheduler{
	{"systemd timer", installWatchdogTimer, removeWatchdogTimer},
	{"cron", installWatchdogCron, removeWatchdogCron},
}

func installWatchdogTimer(intervalMin int, cmdLine string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return err
	}
	service := fmt.Sprintf(`[Unit]
Description=glockdoc heartbeat watchdog for glocker

[Service]
Type=oneshot
ExecStart=%s
`, cmdLine)
	// OnBootSec fires right away when the timer starts after boot has already
	// passed (i.e. at install), then every interval after each run.
	timer := fmt.Sprintf(`[Unit]
Description=Run the glockdoc heartbeat watchdog every %d min

[Timer]
OnBootSec=1min
OnUnitActiveSec=%dmin
AccuracySec=30s

[Install]
WantedBy=timers.target
`, intervalMin, intervalMin)

	if err := os.WriteFile(config.GlockdocServicePath, []byte(service), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", config.GlockdocServicePath, err)
	}
	if err := os.WriteFile(config.GlockdocTimerPath, []byte(timer), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", config.GlockdocTimerPath, err)
	}
	// restart (not just enable --now) so a reinstall picks up a changed interval.
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", "glockdoc.timer"},
		{"restart", "glockdoc.timer"},
	} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func removeWatchdogTimer() {
	if _, err := os.Stat(config.GlockdocTimerPath); err != nil {
		return
	}
	_ = exec.Command("systemctl", "disable", "--now", "glockdoc.timer").Run()
	_ = exec.Command("systemctl", "stop", "glockdoc.service").Run()
	for _, p := range []string{config.GlockdocTimerPath, config.GlockdocServicePath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			log.Printf("   Warning: couldn't remove %s: %v", p, err)
		}
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
}

func installWatchdogCron(intervalMin int, cmdLine string) error {
	if !cronDaemonPresent() {
		return fmt.Errorf("no cron daemon (cron/crond) in PATH")
	}
	if err := os.MkdirAll(filepath.Dir(config.HeartbeatCronPath), 0o755); err != nil {
		return err
	}
	// cron.d requires a username field and a trailing newline.
	cronLine := fmt.Sprintf("*/%d * * * * root %s\n", intervalMin, cmdLine)
	if err := os.WriteFile(config.HeartbeatCronPath, []byte(cronLine), 0o644); err != nil {
		return fmt.Errorf("writing cron file %s: %w", config.HeartbeatCronPath, err)
	}
	// cron.d ignores files that are group/world-writable or not root-owned.
	if err := os.Chown(config.HeartbeatCronPath, 0, 0); err != nil {
		log.Printf("Warning: couldn't set cron file ownership to root: %v", err)
	}
	if err := os.Chmod(config.HeartbeatCronPath, 0o644); err != nil {
		log.Printf("Warning: couldn't set cron file mode: %v", err)
	}
	return nil
}

func removeWatchdogCron() {
	if err := os.Remove(config.HeartbeatCronPath); err != nil && !os.IsNotExist(err) {
		log.Printf("   Warning: couldn't remove %s: %v", config.HeartbeatCronPath, err)
	}
}

func cronDaemonPresent() bool {
	for _, bin := range []string{"cron", "crond"} {
		if _, err := exec.LookPath(bin); err == nil {
			return true
		}
	}
	return false
}

// RunningAsRoot checks if the process is running with root privileges.
// If real is true, checks the real user ID; otherwise checks effective user ID.
func RunningAsRoot(real bool) bool {
	var uid int
	if real {
		uid = os.Getuid() // Real user ID - who actually ran the command
	} else {
		uid = os.Geteuid() // Effective user ID - current privileges (affected by setuid)
	}
	return uid == 0
}
