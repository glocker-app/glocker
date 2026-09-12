package install

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"glocker/internal/config"
	"glocker/internal/envcheck"
	"glocker/internal/utils"
	"glocker/internal/web"
)

const (
	SystemdServiceFile   = "extras/glocker.service"
	GlockpeekServiceFile = "extras/glockpeek.service"
	GlockpeekServicePath = "/etc/systemd/system/glockpeek.service"
)

// InstallGlocker performs the complete installation of Glocker on the system.
// This includes copying the binary, config file, setting up systemd service,
// and installing the Firefox extension.
func InstallGlocker() error {
	log.Println("╔════════════════════════════════════════════════╗")
	log.Println("║              GLOCKER FULL INSTALL              ║")
	log.Println("╚════════════════════════════════════════════════╝")
	log.Println()

	// Step 1: Validate config file before installation. LoadFile resolves any
	// !include directives (conf.d/ list files) relative to conf/ first.
	log.Println("Validating configuration file...")
	cfgPtr, err := config.LoadFile("conf/conf.yaml")
	if err != nil {
		return fmt.Errorf("reading/parsing conf/conf.yaml: %w", err)
	}
	cfg := *cfgPtr

	if err := config.ValidateConfig(&cfg); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	log.Println("✓ Configuration file is valid")

	// Preflight the host capabilities glocker's enabled features need. Abort if a
	// required one is missing (no systemd to run the service, or no visudo while
	// sudoers management is on) — installing something that won't work is worse
	// than refusing. Merely-degrading gaps (no cron watchdog, no immutable-flag
	// support) are warnings. (The separate bypass-routes security audit,
	// `glocker -security-check`, is not part of install.)
	envResults := envcheck.Check(cfg.Sudoers.Enabled)
	if err := envcheck.Verify(envResults); err != nil {
		return fmt.Errorf("cannot install: %w", err)
	}
	envcheck.LogAdvisories(envResults, log.Printf)

	// Step 1b: If a glocker daemon is already running, stop it BEFORE touching any
	// files. Its tamper monitor (30s tick) checksums the binary/config/unit and
	// raises the alarm the instant they change; stopping first kills that monitor
	// so an in-place upgrade is silent. Stop→(swap)→start also guarantees the new
	// binary actually becomes the running process (a bare `systemctl start` on an
	// already-active unit is a no-op). hosts/sudoers enforcement is unaffected by
	// stopping the daemon — /etc/hosts stays immutable and sudoers isn't reverted
	// (only -uninstall reverts those), so the only gap is a few seconds of
	// daemon-driven monitoring during the swap.
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

	// Step 3: Copy config file from conf/conf.yaml to target location
	log.Printf("Copying config file from conf/conf.yaml to %s", config.GlockerConfigFile)

	// Create config directory if it doesn't exist
	configDir := filepath.Dir(config.GlockerConfigFile)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Copy the config file. Clear any prior immutable flag first — on a reinstall
	// the existing config is `chattr +i`, and CopyFile (os.Create) would fail with
	// EPERM otherwise.
	_ = exec.Command("chattr", "-i", config.GlockerConfigFile).Run()
	if err := utils.CopyFile("conf/conf.yaml", config.GlockerConfigFile); err != nil {
		return fmt.Errorf("failed to copy config file: %w", err)
	}
	log.Printf("✓ Config file copied to %s", config.GlockerConfigFile)

	// Set ownership and make config file immutable
	if err := os.Chown(config.GlockerConfigFile, 0, 0); err != nil {
		log.Printf("Warning: couldn't set config file ownership: %v", err)
	}
	if err := exec.Command("chattr", "+i", config.GlockerConfigFile).Run(); err != nil {
		log.Printf("Warning: couldn't set immutable flag on config file: %v", err)
	}

	// Step 3b: Install the generated conf.d/ blocklists that the config pulls in
	// via !include, made immutable like the config itself.
	installConfD()

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

	// Set setuid bit (4755 = rwsr-xr-x)
	if err := os.Chmod(config.InstallPath, 0o755|os.ModeSetuid|os.ModeSetgid); err != nil {
		return fmt.Errorf("failed to set setuid bit: %w", err)
	}

	// Set immutable on the installed binary
	if err := exec.Command("chattr", "+i", config.InstallPath).Run(); err != nil {
		log.Printf("Warning: couldn't set immutable flag: %v", err)
	}
	log.Println("✓ Binary installed with setuid permissions")

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

	// Step 4d: Install the glockdoc watchdog and its root cron job. Unlike the
	// other sidecars, glockdoc and its cron intentionally survive uninstall so
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

	// Step 5b: Install the GNOME Shell window-bridge extension (GNOME/Wayland
	// only; self-skips elsewhere) so usage tracking works there too.
	InstallGnomeExtension(&cfg)

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
	log.Println("   Run 'glocker -status' to check the current status")

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
func installConfD() {
	srcDir := "conf/conf.d"
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

// installHeartbeat installs the glockdoc watchdog binary and a root cron job
// that runs it on a fixed cadence. All parameters are baked into the cron line
// as flags so the watchdog never depends on the config file (removed on
// uninstall). The binary and cron file are deliberately left in place by
// uninstall so the watchdog keeps recording the resulting downtime.
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

	// Resolve parameters, falling back to defaults.
	interval := cfg.Heartbeat.IntervalMinutes
	if interval <= 0 {
		interval = 30
	}
	logFile := cfg.Heartbeat.LogFile
	if logFile == "" {
		logFile = config.DefaultHeartbeatLogFile
	}
	timeout := cfg.Heartbeat.TimeoutSeconds
	if timeout <= 0 {
		timeout = 3
	}

	// Bake every parameter into the cron line so glockdoc needs no config file.
	// cron.d requires a username field and a trailing newline.
	cronLine := fmt.Sprintf("*/%d * * * * root %s -socket %s -log %s -timeout %ds\n",
		interval, config.GlockdocInstallPath, config.GlockerSock, logFile, timeout)

	if err := os.WriteFile(config.HeartbeatCronPath, []byte(cronLine), 0644); err != nil {
		return fmt.Errorf("writing cron file %s: %w", config.HeartbeatCronPath, err)
	}
	// cron.d ignores files that are group/world-writable or not root-owned.
	if err := os.Chown(config.HeartbeatCronPath, 0, 0); err != nil {
		log.Printf("Warning: couldn't set cron file ownership to root: %v", err)
	}
	if err := os.Chmod(config.HeartbeatCronPath, 0o644); err != nil {
		log.Printf("Warning: couldn't set cron file mode: %v", err)
	}

	// Warn (don't fail) if no cron daemon is available to run the job.
	if _, err := exec.LookPath("cron"); err != nil {
		if _, err2 := exec.LookPath("crond"); err2 != nil {
			log.Printf("Warning: no cron/crond found in PATH — the heartbeat watchdog won't run until a cron daemon is installed")
		}
	}

	log.Printf("✓ Heartbeat watchdog scheduled every %d min (%s)", interval, config.HeartbeatCronPath)
	return nil
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
