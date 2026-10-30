package install

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"glocker/internal/config"
	"glocker/internal/sudoplan"
	"glocker/internal/utils"
)

// RestoreSystemChanges removes all glocker modifications and restores the system to its original state.
// This includes cleaning the hosts file, sudoers, Firefox extension, and removing config files.
func RestoreSystemChanges(cfg *config.Config) error {
	log.Println("╔════════════════════════════════════════════════╗")
	log.Println("║           RESTORING SYSTEM CHANGES             ║")
	log.Println("╚════════════════════════════════════════════════╝")
	log.Println()

	utils.ClearLegacyImmutable(legacyImmutablePaths(cfg)...)

	// Clean up hosts file
	log.Println("Restoring hosts file...")
	if err := cleanupHostsFile(cfg); err != nil {
		log.Printf("   Warning: couldn't clean hosts file: %v", err)
	} else {
		log.Println("✓ Hosts file restored")
	}

	// Give sudo back exactly as it was before the takeover.
	sys := sudoplan.Default()
	if sudoplan.Active(sys) {
		log.Println("Restoring sudo access...")
		if err := sudoplan.Restore(sys); err != nil {
			log.Printf("   Warning: couldn't fully restore sudo access: %v", err)
		} else {
			log.Println("✓ Sudo access restored to how it was before install")
		}
	} else if data, err := os.ReadFile(config.SudoersPath); err == nil && strings.Contains(string(data), config.SudoersMarker) {
		// Installs from before the sudoers.d takeover edited /etc/sudoers itself.
		log.Println("Restoring sudoers configuration...")
		if err := restoreSudoers(cfg); err != nil {
			log.Printf("   Warning: couldn't restore sudoers: %v", err)
		} else {
			log.Println("✓ Sudoers configuration restored")
		}
		switch err := os.Remove(config.SudoersBackup); {
		case err == nil:
			log.Println("✓ Sudoers backup removed")
		case !os.IsNotExist(err): // no backup taken is the normal case
			log.Printf("   Warning: couldn't remove sudoers backup: %v", err)
		}
	}

	// Clean up Firefox extension
	log.Println("Removing Firefox extension...")
	if err := UninstallFirefoxExtension(); err != nil {
		log.Printf("   Warning: couldn't remove Firefox extension: %v", err)
	} else {
		log.Println("✓ Firefox extension removed")
	}

	log.Println("Removing Chrome extension policies...")
	if err := UninstallChromeExtension(); err != nil {
		log.Printf("   Warning: couldn't remove Chrome extension: %v", err)
	} else {
		log.Println("✓ Chrome extension policies removed")
	}

	// Clean up the GNOME window-bridge extension (self-skips if not installed).
	RemoveGnomeExtension(cfg)
	// Same for the KWin script.
	RemoveKWinScript(cfg)

	log.Println("Removing config file...")
	if err := os.Remove(config.GlockerConfigFile); err != nil {
		log.Printf("   Warning: couldn't remove config file: %v", err)
	} else {
		log.Println("✓ Config file removed")
	}

	// Remove the conf.d blocklist files so the config directory below is empty
	// and can be cleaned up.
	if entries, err := os.ReadDir(config.GlockerConfDDir); err == nil {
		for _, e := range entries {
			p := filepath.Join(config.GlockerConfDDir, e.Name())
			_ = os.Remove(p)
		}
		if err := os.Remove(config.GlockerConfDDir); err == nil {
			log.Println("✓ conf.d blocklists removed")
		}
	}

	// Remove config directory if empty
	configDir := filepath.Dir(config.GlockerConfigFile)
	if err := os.Remove(configDir); err != nil {
		log.Printf("   Warning: couldn't remove config directory (may not be empty): %v", err)
	} else {
		log.Println("✓ Config directory removed")
	}

	// Remove socket file and its dedicated directory
	if err := os.Remove(config.GlockerSock); err != nil {
		log.Printf("   Warning: couldn't remove socket file: %v", err)
	} else {
		log.Println("✓ Socket file removed")
	}
	if socketDir := filepath.Dir(config.GlockerSock); socketDir != "/" && socketDir != "." {
		// Best-effort: only succeeds if empty, which it should be after the socket is gone.
		_ = os.Remove(socketDir)
	}

	// Remove glocklock binary
	log.Println("Removing glocklock binary...")
	if err := os.Remove(config.GlocklockInstallPath); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("   Warning: couldn't remove glocklock: %v", err)
		}
	} else {
		log.Println("✓ glocklock binary removed")
	}

	// Stop, disable, and remove the glockpeek dashboard service.
	log.Println("Removing glockpeek dashboard service...")
	exec.Command("systemctl", "disable", "--now", "glockpeek.service").Run()
	if err := os.Remove(GlockpeekServicePath); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("   Warning: couldn't remove glockpeek service file: %v", err)
		}
	}
	exec.Command("systemctl", "daemon-reload").Run()

	// Remove glockpeek binary
	log.Println("Removing glockpeek binary...")
	if err := os.Remove(config.GlockpeekInstallPath); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("   Warning: couldn't remove glockpeek: %v", err)
		}
	} else {
		log.Println("✓ glockpeek binary removed")
	}

	// The glockdoc watchdog and its schedule (systemd timer or cron job) are
	// deliberately left in place: it keeps pinging the (now absent) socket and
	// recording "alive:false" samples, so the heartbeat log captures how long
	// glocker stayed uninstalled. To stop it, remove it by hand — a separate,
	// deliberate act (the glocker binary, and so -purge, is gone by now).
	if _, err := os.Stat(config.GlockdocTimerPath); err == nil {
		log.Println("Note: heartbeat watchdog (glockdoc) left running to record this downtime.")
		log.Printf("      To stop it: sudo systemctl disable --now glockdoc.timer && sudo rm %s %s %s",
			config.GlockdocTimerPath, config.GlockdocServicePath, config.GlockdocInstallPath)
	}
	if _, err := os.Stat(config.HeartbeatCronPath); err == nil {
		log.Println("Note: heartbeat watchdog (glockdoc) left running to record this downtime.")
		log.Printf("      To stop it: sudo rm %s %s", config.HeartbeatCronPath, config.GlockdocInstallPath)
	}

	log.Println("✓ System changes restored successfully")
	return nil
}

// PurgeWatchdog removes the glockdoc watchdog binary and its schedule (systemd
// timer and/or cron job). A normal uninstall deliberately leaves them running so the heartbeat gap records the
// downtime; -purge (or a standalone `glocker -purge`) is the explicit "remove
// everything, including the watchdog" cleanup. Idempotent and best-effort.
func PurgeWatchdog() {
	log.Println("Purging glockdoc watchdog...")
	// Clear every scheduler, not just the one a fresh install would pick: an
	// older install may have used a different one.
	for _, s := range watchdogSchedulers {
		s.remove()
	}
	log.Println("✓ Removed glockdoc schedule (systemd timer / cron)")
	if err := os.Remove(config.GlockdocInstallPath); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("   Warning: couldn't remove %s: %v", config.GlockdocInstallPath, err)
		}
	} else {
		log.Printf("✓ Removed %s", config.GlockdocInstallPath)
	}
}

// cleanupHostsFile removes the glocker section from the hosts file.
func cleanupHostsFile(cfg *config.Config) error {
	hostsPath := cfg.Domains.HostsPath
	if hostsPath == "" {
		hostsPath = "/etc/hosts"
	}

	// Read current hosts file
	content, err := os.ReadFile(hostsPath)
	if err != nil {
		return fmt.Errorf("reading hosts file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string

	// Remove everything after glocker start marker
	for _, line := range lines {
		if strings.Contains(line, config.HostsMarkerStart) {
			// Stop processing once we hit the start marker
			break
		}
		newLines = append(newLines, line)
	}

	// Write cleaned content
	newContent := strings.Join(newLines, "\n")
	return os.WriteFile(hostsPath, []byte(newContent), 0644)
}

// restoreSudoers restores the sudoers file from backup or replaces blocked line with allowed line.
func restoreSudoers(cfg *config.Config) error {
	// Check if backup exists
	if _, err := os.Stat(config.SudoersBackup); os.IsNotExist(err) {
		// No backup exists, replace blocked line with allowed line
		return replaceBlockedWithAllowed(cfg)
	}

	// Restore from backup
	backupContent, err := os.ReadFile(config.SudoersBackup)
	if err != nil {
		return fmt.Errorf("reading sudoers backup: %w", err)
	}

	// Write to temporary file for validation
	tmpFile := config.SudoersPath + ".tmp"
	if err := os.WriteFile(tmpFile, backupContent, 0440); err != nil {
		return fmt.Errorf("writing temporary sudoers file: %w", err)
	}
	defer os.Remove(tmpFile)

	// Validate with visudo
	cmd := exec.Command("visudo", "-c", "-f", tmpFile)
	if err := cmd.Run(); err != nil {
		// If backup is invalid, fall back to replacing blocked with allowed
		log.Printf("Backup sudoers file is invalid, replacing blocked line with allowed instead")
		return replaceBlockedWithAllowed(cfg)
	}

	// Validation passed, restore the backup
	if err := os.Rename(tmpFile, config.SudoersPath); err != nil {
		return fmt.Errorf("restoring sudoers file: %w", err)
	}

	// Ensure correct permissions
	return os.Chmod(config.SudoersPath, 0440)
}

// replaceBlockedWithAllowed replaces the blocked sudoers line with the allowed line.
func replaceBlockedWithAllowed(cfg *config.Config) error {
	// Read current sudoers file
	content, err := os.ReadFile(config.SudoersPath)
	if err != nil {
		return fmt.Errorf("reading sudoers file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string
	found := false

	// Look for our managed line and replace blocked with allowed
	for _, line := range lines {
		if strings.Contains(line, config.SudoersMarker) {
			// Replace with allowed line
			newLines = append(newLines, cfg.Sudoers.AllowedSudoersLine+" "+config.SudoersMarker)
			found = true
		} else {
			newLines = append(newLines, line)
		}
	}

	// If we didn't find our managed line, add the allowed line
	if !found {
		newLines = append(newLines, cfg.Sudoers.AllowedSudoersLine+" "+config.SudoersMarker)
	}

	newContent := utils.JoinFileLines(newLines)

	// Write to temporary file for validation
	tmpFile := config.SudoersPath + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(newContent), 0440); err != nil {
		return fmt.Errorf("writing temporary sudoers file: %w", err)
	}
	defer os.Remove(tmpFile)

	// Validate with visudo
	cmd := exec.Command("visudo", "-c", "-f", tmpFile)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudoers validation failed: %w", err)
	}

	// Validation passed, replace the sudoers file
	if err := os.Rename(tmpFile, config.SudoersPath); err != nil {
		return fmt.Errorf("replacing sudoers file: %w", err)
	}

	// Ensure correct permissions
	return os.Chmod(config.SudoersPath, 0440)
}
