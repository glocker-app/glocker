package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"glocker/internal/cli"
	"glocker/internal/config"
	"glocker/internal/connect"
	"glocker/internal/enforcement"
	"glocker/internal/envcheck"
	"glocker/internal/install"
	"glocker/internal/ipc"
	"glocker/internal/mindful"
	"glocker/internal/monitoring"
	"glocker/internal/precheck"
	"glocker/internal/reports"
	"glocker/internal/state"
	"glocker/internal/syncer"
	"glocker/internal/usage"
	"glocker/internal/web"

	"golang.org/x/term"
)

// version is stamped at build time via -ldflags "-X main.version=..." (git
// describe; see the Makefile). It stays "dev" for a plain `go build`.
var version = "dev"

// readPassphrase reads without echo from a terminal; when stdin is piped it reads
// a single line so `-connect` stays scriptable.
func readPassphrase(prompt string) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		p, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return p, err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}


// ansiRE matches SGR (color/style) escape sequences.
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripANSI removes ANSI SGR sequences so status output stays readable when the
// client's stdout isn't a terminal (piped/redirected) or NO_COLOR is set. The
// daemon always emits color because it can't see the client's terminal, so the
// client decides whether to keep it.
func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// hasControlChar reports whether s contains any control character (incl. \r and
// \n). Command arguments sent over the line-delimited socket must not — a newline
// would inject a second command.
func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// printGroupedUsage renders `glocker -h` in labelled sections instead of one flat
// alphabetical list. Descriptions come from the flag definitions (so they stay in
// one place); -daemon and -conf are internal and intentionally omitted.
func printGroupedUsage() {
	out := flag.CommandLine.Output()
	fmt.Fprintln(out, "glocker — a Linux distraction blocker.")
	fmt.Fprint(out, "\nUsage: glocker [flag]   (no flag: show status, or help if the daemon isn't running)\n\n")

	groups := []struct {
		title string
		names []string
	}{
		{"Status & info", []string{"status", "info", "version"}},
		{"Setup & lifecycle", []string{"init", "install", "uninstall", "note", "purge", "connect", "force"}},
		{"Runtime controls", []string{"block", "block-app", "unblock", "add-keyword", "extend", "panic", "lock"}},
		{"Sync", []string{"resync"}},
		{"Diagnostics", []string{"check"}},
	}
	for _, g := range groups {
		fmt.Fprintf(out, "%s:\n", g.title)
		for _, name := range g.names {
			f := flag.Lookup(name)
			if f == nil {
				continue
			}
			left := "-" + name
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bf.IsBoolFlag() {
				left += " <value>"
			}
			fmt.Fprintf(out, "  %-20s %s\n", left, f.Usage)
		}
		fmt.Fprintln(out)
	}
}

func main() {
	// Parse command-line flags
	installFlag := flag.Bool("install", false, "Install glocker as a system service")
	initFlag := flag.Bool("init", false, "Generate a starting config from the sample: fills in your login (and optional hosted dashboard + buddy email), keeping the sample's comments. Won't overwrite an existing config. Run as a normal user, before -install.")
	confFlag := flag.String("conf", "conf/conf.yaml", "source config for -install (its dir also holds conf.d/); lets the config live outside the source tree")
	uninstallReason := flag.String("uninstall", "", "Uninstall Glocker and revert all changes (provide reason)")
	uninstallNote := flag.String("note", "", "Optional free-form note to attach to -uninstall (e.g. context for the reason)")
	daemonFlag := flag.Bool("daemon", false, "Run as daemon (for systemd service)")
	statusFlag := flag.Bool("status", false, "Show runtime status (violations, temp unblocks, panic mode)")
	infoFlag := flag.Bool("info", false, "Show configuration info (domains, programs, keywords)")
	blockHosts := flag.String("block", "", "Comma-separated list of hosts to add to always block list")
	blockApps := flag.String("block-app", "", "Comma-separated list of programs to add to the forbidden-programs list (killed on sight, 24/7)")
	unblockHosts := flag.String("unblock", "", "Comma-separated list of hosts to temporarily unblock (format: 'domain1,domain2:reason')")
	extendProgram := flag.String("extend", "", "Grant a one-hour run extension for a forbidden program (format: 'program:reason'). Program must be marked extendible in the config; one grant per 24h.")
	addKeyword := flag.String("add-keyword", "", "Comma-separated list of keywords to add to both URL and content keyword lists")
	panicFlag := flag.Bool("panic", false, "Enter panic mode: suspend the system for panic.duration minutes, re-suspending on early wake")
	lockFlag := flag.Bool("lock", false, "Immediately lock sudoers access (ignores time windows)")
	resyncFlag := flag.Bool("resync", false, "Re-push all local records to glockpeek (backfill), overwriting its copies — use after a fix like enabling encryption for a field. Watch it with `glocker -status`.")
	checkFlag := flag.Bool("check", false, "Read-only preflight + audit (run as root): required capabilities (systemd, visudo, …) AND bypass/recovery routes (blanket sudo grants, wheel/sudo membership, root login, SSH root, autologin).")
	connectFlag := flag.Bool("connect", false, "Connect this machine to your dashboard's encryption: fetch your key vault, unlock it with your passphrase, and install the key so synced data is end-to-end encrypted")
	connectForce := flag.Bool("force", false, "With -connect: replace an existing key even if it differs from your vault (WARNING: makes data encrypted under the old key unreadable)")
	purgeFlag := flag.Bool("purge", false, "With -uninstall: also remove the glockdoc watchdog + its schedule (timer/cron). Alone: clean up a leftover watchdog from an earlier uninstall.")
	versionFlag := flag.Bool("version", false, "Show version information")

	flag.Usage = printGroupedUsage
	flag.Parse()

	// Surface the build version in -status (rendered both here and, for the live
	// path, inside the daemon — same binary, so the same stamped value).
	cli.Version = version

	// Handle version flag
	if *versionFlag {
		fmt.Println("glocker " + version)
		return
	}

	// Generate a starting config from the commented sample (normal user, pre-install).
	if *initFlag {
		res, err := runInit(*confFlag)
		if err != nil {
			log.Fatalf("init failed: %v", err)
		}
		fmt.Printf("\nWrote %s (login: %s).\n", *confFlag, res.login)
		// The tarball installer runs -init then -install back to back, so these
		// "next, go install" steps would print right before the install and just
		// confuse — it prints its own review note afterwards instead. Source users
		// (make init) still see them.
		if os.Getenv("GLOCKER_INSTALLER") == "" {
			fmt.Println("\nNext steps:")
			fmt.Printf("  1. Review %s — schedules, forbidden programs, keywords.\n", *confFlag)
			fmt.Printf("  2. Add your own sites to %s.\n", filepath.Join(filepath.Dir(*confFlag), "conf.d", "personal-domains.yaml"))
			fmt.Println("  3. Run 'make full-install' to fetch the standard blocklists and install glocker.")
			fmt.Println("     (A bare 'glocker -install' skips the blocklist download — nothing to block.)")
		}
		if res.sudoRestricted {
			fmt.Println("\nsudo: allowed during sudoers.allow_windows, blocked outside them. The sample")
			fmt.Println("allows sudo all day except 09:00–10:00. Set your own windows at")
			fmt.Printf("%s:%d.\n", *confFlag, res.sudoWindowLine)
		}
		if res.hosted {
			fmt.Println("\nhosted sync: encrypt is on, so run 'glocker -connect' to install your")
			fmt.Println("encryption key before the syncer will push anything.")
		}
		return
	}

	// Handle security check (read-only; reads /etc/shadow so needs root)
	// Unified check: capabilities (envcheck) + bypass/recovery routes (precheck) in
	// one report. This is the "is my glocker install both able to run and actually
	// enforceable?" command referenced in the docs.
	if *checkFlag {
		if !install.RunningAsRoot(true) {
			log.Fatal("glocker -check must run as root (use sudo) — it reads /etc/shadow and sudoers")
		}
		managedUser, needSudoers := "", false
		if cfg, err := config.LoadConfig(); err == nil {
			managedUser, needSudoers = cfg.User, cfg.Sudoers.Enabled
		}
		fmt.Println("── Capabilities ──")
		for _, r := range envcheck.Check(needSudoers) {
			switch {
			case r.OK:
				fmt.Printf("  ✓ %s\n", r.Name)
			case r.Required:
				fmt.Printf("  ✗ %s — REQUIRED: %s\n", r.Name, r.Detail)
			default:
				fmt.Printf("  ⚠ %s — %s\n", r.Name, r.Detail)
			}
		}
		fmt.Println("\n── Bypass & recovery routes ──")
		fmt.Print(precheck.Run(precheck.Options{ManagedUser: managedUser}).String())
		return
	}

	// Standalone purge: remove a watchdog left over from an earlier uninstall.
	// (Combined with -uninstall it's handled at the end of that flow instead.)
	if *purgeFlag && *uninstallReason == "" {
		if !install.RunningAsRoot(true) {
			log.Fatal("Purge must be run as root (use sudo)")
		}
		install.PurgeWatchdog()
		return
	}

	// Handle installation
	if *installFlag {
		if !install.RunningAsRoot(true) {
			log.Fatal("Installation must be run as root (use sudo)")
		}
		if err := install.InstallGlocker(*confFlag); err != nil {
			log.Fatalf("Installation failed: %v", err)
		}
		return
	}

	// Connect this agent to the account's end-to-end encryption. It writes the
	// master key under /etc/glocker, so it needs root (sudo); the passphrase is the
	// real gate on installing a key.
	if *connectFlag {
		if !install.RunningAsRoot(false) {
			log.Fatal("Connect needs root to write the encryption key under /etc/glocker — run it with sudo")
		}
		cfg, err := config.LoadConfig()
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}
		pass, err := readPassphrase("Dashboard encryption passphrase: ")
		if err != nil {
			log.Fatalf("Could not read passphrase: %v", err)
		}
		keyPath, res, err := connect.Run(cfg, pass, *connectForce)
		if err != nil {
			log.Fatalf("Connect failed: %v", err)
		}
		if res == connect.AlreadyMatches {
			fmt.Printf("\nThe key at %s already matches your dashboard vault — nothing to change.\n", keyPath)
			fmt.Println("(So data already encrypted under it is decryptable with your passphrase.)")
			return
		}
		fmt.Printf("\nEncryption key installed at %s (from your dashboard vault).\n\n", keyPath)
		if cfg.Sync.Encryption() {
			fmt.Println("sync.encrypt is on and the key is in place. Restart glocker to start encrypting:")
			fmt.Println("    sudo systemctl restart glocker")
		} else {
			fmt.Println("⚠  WARNING: sync.encrypt is OFF, so this key sits UNUSED and records still")
			fmt.Println("   upload as PLAINTEXT. To actually encrypt, set it in your agent config:")
			fmt.Println("       sync:")
			fmt.Println("         encrypt: true")
			fmt.Println("   then re-install (make full-install) or restart the glocker service.")
		}
		return
	}

	// Handle uninstallation
	if *uninstallReason != "" {
		if !install.RunningAsRoot(true) {
			log.Fatal("Uninstall must be run as root (use sudo)")
		}

		// Check if glocker is actually installed
		if _, err := os.Stat("/usr/local/bin/glocker"); os.IsNotExist(err) {
			log.Fatal("Glocker is not installed. Nothing to uninstall.")
		}

		// Load config so we can validate the reason and decide whether the
		// mindful uninstall gate applies. Fails closed: if the config can't be
		// read, we refuse rather than uninstall without its guardrails.
		uninstallCfg, err := config.LoadConfig()
		if err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}

		// Validate the reason locally before making the user work for it, so an
		// invalid reason fails fast rather than after the mindful challenge. The
		// daemon re-validates authoritatively. An empty reasons list accepts any.
		if len(uninstallCfg.Lifecycle.Reasons) > 0 {
			valid := false
			for _, r := range uninstallCfg.Lifecycle.Reasons {
				if strings.EqualFold(*uninstallReason, r) {
					valid = true
					break
				}
			}
			if !valid {
				log.Fatalf("Invalid uninstall reason %q. Valid reasons: %s",
					*uninstallReason, strings.Join(uninstallCfg.Lifecycle.Reasons, ", "))
			}
		}

		// Mindful gate: a metronome-paced typing challenge that must be passed
		// before the uninstall reaches the daemon. Fails closed — if the gate is
		// enabled but cannot run (no tty, terminal error), the uninstall is
		// refused rather than silently bypassed. Every uninstall is gated;
		// upgrades no longer uninstall (glocker -install swaps in place), so there
		// is no "maintenance" bypass to abuse.
		if uninstallCfg.MindfulUninstall.Enabled {
			mc := uninstallCfg.MindfulUninstall.WithDefaults()

			// Recency escalation: if the ledger shows a recent uninstall, treat
			// this as a repeat and raise the challenge. The current uninstall
			// isn't logged until the daemon processes it after the gate, so this
			// only ever sees prior teardowns.
			lines := mc.Lines
			if mc.RecencyHours > 0 && mc.RecentLines > 0 {
				since := time.Now().Add(-time.Duration(mc.RecencyHours) * time.Hour)
				recent, rerr := reports.RecentUninstalls(uninstallCfg.Lifecycle.LogFile, since)
				if rerr != nil {
					log.Printf("Warning: couldn't read uninstall history (using base friction): %v", rerr)
				} else if len(recent) > 0 {
					lines = mc.RecentLines
					last := recent[len(recent)-1]
					log.Printf("Repeat uninstall: %d in the last %dh (most recent %q, %s ago). Raising the mindful gate.",
						len(recent), mc.RecencyHours, last.Reason, time.Since(last.Timestamp).Round(time.Minute))
				}
			}

			passed, err := mindful.Run(mindful.Options{
				Sentences: mc.Sentences,
				Lines:     lines,
				Interval:  time.Duration(mc.IntervalMs) * time.Millisecond,
				Deadline:  time.Duration(mc.DeadlineMs) * time.Millisecond,
				Grace:     time.Duration(mc.GraceMs) * time.Millisecond,
			})
			if err != nil {
				log.Fatalf("Mindful gate could not run (uninstall refused): %v", err)
			}
			if !passed {
				log.Println("Uninstall aborted at the mindful gate.")
				os.Exit(1)
			}
		}

		// Send uninstall request to daemon via socket
		conn, err := net.Dial("unix", ipc.SocketPath)
		if err != nil {
			log.Fatalf("Failed to connect to glocker service: %v", err)
		}
		defer conn.Close()

		message := fmt.Sprintf("uninstall:%s", *uninstallReason)
		if *uninstallNote != "" {
			message += ":" + *uninstallNote
		}
		message += "\n"
		conn.Write([]byte(message))

		// Read initial response
		reader := bufio.NewReader(conn)
		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}
		trimmedResponse := strings.TrimSpace(response)
		log.Printf("Response: %s", trimmedResponse)

		if strings.HasPrefix(trimmedResponse, "ERROR:") {
			os.Exit(1)
		}

		// Wait for completion signal
		log.Println("Waiting for uninstall process to complete...")
		completionResponse, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read completion response: %v", err)
		}
		log.Printf("Completion: %s", strings.TrimSpace(completionResponse))

		// Now stop and disable the systemd service (daemon has exited)
		log.Println("Stopping and disabling glocker service...")
		if err := exec.Command("systemctl", "stop", "glocker.service").Run(); err != nil {
			log.Printf("   Warning: couldn't stop service: %v", err)
		} else {
			log.Println("✓ Service stopped")
		}

		if err := exec.Command("systemctl", "disable", "glocker.service").Run(); err != nil {
			log.Printf("   Warning: couldn't disable service: %v", err)
		} else {
			log.Println("✓ Service disabled")
		}

		// The daemon is stopped, so its unit, binary and config can go now.
		// Anything that won't is listed below for a manual rm.
		for _, p := range []string{"/etc/systemd/system/glocker.service", config.InstallPath, config.GlockerConfigFile} {
			if err := os.Remove(p); err == nil {
				log.Printf("✓ Removed %s", p)
			}
		}
		_ = os.Remove(filepath.Dir(config.GlockerConfigFile)) // only if empty

		// Reload systemd daemon
		log.Println("Reloading systemd daemon...")
		if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
			log.Printf("   Warning: couldn't reload systemd daemon: %v", err)
		} else {
			log.Println("✓ Systemd daemon reloaded")
		}

		log.Println()
		log.Println("🎉 Glocker system changes have been restored!")
		log.Println("   All protections have been removed and original settings restored.")
		log.Printf("   Uninstall reason: %s", *uninstallReason)
		if *uninstallNote != "" {
			log.Printf("   Note: %s", *uninstallNote)
		}
		// The E2E master key is deliberately kept so a reinstall can sync again
		// without -connect; say so, since it blocks the rmdir below.
		keyFile := uninstallCfg.Sync.KeyFile()
		if _, err := os.Stat(keyFile); err == nil {
			log.Println()
			log.Printf("Kept: %s (your encryption key, for a reinstall). If you're not reinstalling:", keyFile)
			log.Printf("   rm -f %s", keyFile)
		}

		// Only list what's still there.
		var leftover []string
		for _, p := range []string{"/etc/systemd/system/glocker.service", config.InstallPath, config.GlockerConfigFile} {
			if _, err := os.Stat(p); err == nil {
				leftover = append(leftover, "rm -f "+p)
			}
		}
		if _, err := os.Stat(filepath.Dir(config.GlockerConfigFile)); err == nil {
			leftover = append(leftover, "rmdir "+filepath.Dir(config.GlockerConfigFile))
		}
		if len(leftover) > 0 {
			log.Println()
			log.Println("To complete the uninstall, manually run these commands:")
			for _, c := range leftover {
				log.Printf("   %s", c)
			}
		}

		// -purge also removes the glockdoc watchdog + timer/cron that a normal uninstall
		// leaves behind to record downtime.
		if *purgeFlag {
			log.Println()
			install.PurgeWatchdog()
		}

		return
	}

	// Handle socket-based commands (don't need config)
	if *blockHosts != "" {
		conn, err := net.Dial("unix", ipc.SocketPath)
		if err != nil {
			log.Fatalf("Failed to connect to glocker service: %v", err)
		}
		defer conn.Close()

		message := fmt.Sprintf("block:%s\n", *blockHosts)
		conn.Write([]byte(message))

		reader := bufio.NewReader(conn)
		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}

		log.Printf("Response: %s", strings.TrimSpace(response))
		log.Println("Blocked until glocker restarts; add them to the config to keep them blocked.")
		return
	}

	if *blockApps != "" {
		conn, err := net.Dial("unix", ipc.SocketPath)
		if err != nil {
			log.Fatalf("Failed to connect to glocker service: %v", err)
		}
		defer conn.Close()

		message := fmt.Sprintf("block-app:%s\n", *blockApps)
		conn.Write([]byte(message))

		reader := bufio.NewReader(conn)
		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}

		log.Printf("Response: %s", strings.TrimSpace(response))
		log.Println("Programs will be killed on sight (24/7).")
		return
	}

	if *unblockHosts != "" {
		// Parse format: "domain1,domain2:reason"
		parts := strings.SplitN(*unblockHosts, ":", 2)
		if len(parts) != 2 {
			log.Fatal("ERROR: Reason required. Use format: 'domain1,domain2:reason'")
		}

		domains := strings.TrimSpace(parts[0])
		reason := strings.TrimSpace(parts[1])

		if domains == "" {
			log.Fatal("ERROR: No domains specified")
		}

		if reason == "" {
			log.Fatal("ERROR: Reason cannot be empty")
		}
		// Reject control characters outright — the socket protocol is line-delimited,
		// so a newline is a command-injection attempt, and no legitimate domain or
		// reason contains one. Deliberate and independent of the configured set (so
		// it holds even when no reasons are configured), not a side effect of a
		// string match failing.
		if hasControlChar(domains) {
			log.Fatal("invalid domain")
		}
		if hasControlChar(reason) {
			log.Fatal("invalid reason")
		}
		// Then validate the reason against the configured set (the daemon checks it
		// too, authoritatively) for a clean message listing the valid options.
		if cfg, err := config.LoadConfig(); err == nil && len(cfg.Domains.Unblocking.Reasons) > 0 {
			valid := false
			for _, r := range cfg.Domains.Unblocking.Reasons {
				if strings.EqualFold(reason, r) {
					valid = true
					break
				}
			}
			if !valid {
				log.Fatalf("invalid reason. Valid reasons: %s", strings.Join(cfg.Domains.Unblocking.Reasons, ", "))
			}
		}

		conn, err := net.Dial("unix", ipc.SocketPath)
		if err != nil {
			log.Fatalf("Failed to connect to glocker service: %v", err)
		}
		defer conn.Close()

		message := fmt.Sprintf("unblock:%s:%s\n", domains, reason)
		conn.Write([]byte(message))

		reader := bufio.NewReader(conn)
		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}

		log.Printf("Response: %s", strings.TrimSpace(response))
		return
	}

	if *extendProgram != "" {
		parts := strings.SplitN(*extendProgram, ":", 2)
		if len(parts) != 2 {
			log.Fatal("ERROR: Reason required. Use format: 'program:reason'")
		}
		program := strings.TrimSpace(parts[0])
		reason := strings.TrimSpace(parts[1])
		if program == "" {
			log.Fatal("ERROR: Program name cannot be empty")
		}
		if reason == "" {
			log.Fatal("ERROR: Reason cannot be empty")
		}

		conn, err := net.Dial("unix", ipc.SocketPath)
		if err != nil {
			log.Fatalf("Failed to connect to glocker service: %v", err)
		}
		defer conn.Close()

		message := fmt.Sprintf("extend:%s:%s\n", program, reason)
		conn.Write([]byte(message))

		reader := bufio.NewReader(conn)
		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}

		trimmed := strings.TrimSpace(response)
		log.Printf("Response: %s", trimmed)
		if strings.HasPrefix(trimmed, "ERROR:") {
			os.Exit(1)
		}
		return
	}

	if *addKeyword != "" {
		conn, err := net.Dial("unix", ipc.SocketPath)
		if err != nil {
			log.Fatalf("Failed to connect to glocker service: %v", err)
		}
		defer conn.Close()

		message := fmt.Sprintf("add-keyword:%s\n", *addKeyword)
		conn.Write([]byte(message))

		reader := bufio.NewReader(conn)
		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}

		log.Printf("Response: %s", strings.TrimSpace(response))
		log.Println("Keywords will be added to both URL and content keyword lists.")
		return
	}

	if *panicFlag {
		conn, err := net.Dial("unix", ipc.SocketPath)
		if err != nil {
			log.Fatalf("Failed to connect to glocker service: %v", err)
		}
		defer conn.Close()

		conn.Write([]byte("panic\n"))

		reader := bufio.NewReader(conn)
		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}

		log.Printf("%s", strings.TrimSpace(response))
		return
	}

	if *lockFlag {
		conn, err := net.Dial("unix", ipc.SocketPath)
		if err != nil {
			log.Fatalf("Failed to connect to glocker service: %v", err)
		}
		defer conn.Close()

		conn.Write([]byte("lock\n"))

		reader := bufio.NewReader(conn)
		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}

		log.Printf("Response: %s", strings.TrimSpace(response))
		return
	}

	if *resyncFlag {
		conn, err := net.Dial("unix", ipc.SocketPath)
		if err != nil {
			log.Fatalf("Failed to connect to glocker service: %v", err)
		}
		defer conn.Close()
		conn.Write([]byte("resync:0\n"))
		reader := bufio.NewReader(conn)
		response, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}
		log.Printf("%s", strings.TrimSpace(response))
		log.Println("Watch it push with `glocker -status`.")
		return
	}

	// Handle status command (try socket first, only load config if needed)
	if *statusFlag {
		// GetStatusResponse always emits neon ANSI color (the daemon can't see
		// the client's terminal). Strip it here when our own stdout isn't a TTY
		// or NO_COLOR is set.
		useColor := term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""

		// Live status from the daemon if we can reach the socket; otherwise a
		// static, honestly-labelled fallback (statusStatic picks the right
		// ServiceState — "unknown" when we simply lack root, not "offline").
		body, live := statusFromSocket()
		if !live {
			body = statusStatic()
		}
		if !useColor {
			body = stripANSI(body)
		}
		fmt.Print(body)
		return
	}

	// Handle info command
	if *infoFlag {
		// GetInfoResponse emits ANSI colour (like -status); strip it when our
		// own stdout isn't a TTY or NO_COLOR is set. Prefer the daemon's live
		// render, fall back to on-disk config.
		useColor := term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""
		body, live := reportFromSocket("info")
		if !live {
			body = infoStatic()
		}
		if !useColor {
			body = stripANSI(body)
		}
		fmt.Print(body)
		return
	}

	// Handle default behavior (no flags) - show status or help
	if flag.NFlag() == 0 {
		// Check if socket exists and daemon is running
		if _, err := os.Stat(ipc.SocketPath); err == nil {
			conn, err := net.Dial("unix", ipc.SocketPath)
			if err == nil {
				defer conn.Close()

				log.Println("=== LIVE STATUS ===")
				conn.Write([]byte("status\n"))

				// Read response until END
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					line := scanner.Text()
					if line == "END" {
						break
					}
					fmt.Println(line)
				}
				return
			}
		}

		// Socket not available, show the grouped help.
		printGroupedUsage()
		return
	}

	// Daemon mode (started by systemd or manually with -daemon)
	if !*daemonFlag {
		log.Fatal("No matching command. Use -h for help, or -daemon to start the daemon.")
	}

	// The daemon must be real root (systemd starts it as root). With the binary no
	// longer setuid, a plain user running `glocker -daemon` would fail to bind the
	// root paths anyway — refuse up front so they can't try to stand up a rival
	// daemon and hijack the socket.
	if os.Geteuid() != 0 {
		log.Fatal("the glocker daemon must run as root (it's started by systemd; not for direct use)")
	}

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Setup logging
	config.SetupLogging(cfg)

	log.Println("Starting glocker daemon...")

	// Warn if the machine is missing a capability glocker relies on (systemd,
	// visudo, or a watchdog scheduler). The daemon warns and keeps running — it's already installed, and stopping enforcement
	// would fail open.
	envcheck.LogWarnings(envcheck.Check(cfg.Sudoers.Enabled), log.Printf)

	// Restore program extensions so the rolling-24h cooldown survives restarts.
	if err := state.LoadProgramExtensions(); err != nil {
		log.Printf("Warning: failed to load program extensions: %v", err)
	}

	// Setup IPC socket
	if err := ipc.SetupCommunication(cfg); err != nil {
		log.Fatalf("Failed to setup IPC: %v", err)
	}

	// Start monitoring goroutines
	if cfg.Programs.Enabled {
		go monitoring.MonitorForbiddenPrograms(cfg)
	}

	if cfg.ViolationTracking.Enabled {
		go monitoring.MonitorViolations(cfg)
	}

	if cfg.Panic.Enabled {
		go monitoring.MonitorPanicMode(cfg)
	}

	// Start the web server that serves the /blocked page for hosts-blocked domains
	// and the browser-extension endpoints (/report, /keywords, /sse). Needed
	// whenever domain blocking or content monitoring is on.
	if cfg.Domains.Enabled || cfg.Content.Monitoring.Enabled {
		go web.StartWebTrackingServer(cfg)
	}

	// Start the arbtt-style usage tracker.
	if cfg.UsageMonitor.Enabled {
		go startUsageMonitor(cfg)
	}

	// Ship local records to glockpeek. Local-first: this only mirrors the /var
	// files up on a timer; it never affects recording or enforcement.
	if cfg.Sync.Enabled {
		go syncer.New(cfg).Run()
	}

	// Initial enforcement - build hosts file and store state
	log.Println("Performing initial enforcement...")
	enforcement.InitialEnforcement(cfg)

	// Main enforcement loop - only check for changes. The cadence is a fixed
	// internal detail (the loop just detects time-window transitions, temp-unblock
	// expiry, and hosts tampering), not a user knob, so it's a constant rather
	// than config — which also removes the NewTicker(0) panic when the key was
	// omitted.
	const enforceInterval = 60 * time.Second
	ticker := time.NewTicker(enforceInterval)
	defer ticker.Stop()

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Println("Glocker daemon started successfully")

	for {
		select {
		case <-ticker.C:
			enforcement.EnforcementCheck(cfg)
		case sig := <-sigChan:
			log.Printf("Received signal %v, shutting down...", sig)
			return
		}
	}
}

// startUsageMonitor runs the arbtt-style usage tracker for the daemon's
// lifetime, sampling the focused window + idle time into the configured JSONL
// log. A missing or unreachable session is logged and retried each interval
// (see usage.AutoSource); it never brings down the daemon.
func startUsageMonitor(cfg *config.Config) {
	um := cfg.UsageMonitor

	logFile := um.LogFile
	if logFile == "" {
		logFile = config.DefaultUsageLogFile
	}
	interval := um.Interval()
	// Pick a backend for the user's session (Hyprland, GNOME/Wayland or X11;
	// others degrade gracefully). The daemon runs as root with none of the
	// session's environment, so point it at the desktop user's runtime dir
	// (Wayland/Hyprland sockets) and session bus; X11 uses Display/XAuthority.
	// The source re-selects on its own: the daemon usually starts before anyone
	// logs in, and sessions come and go.
	var runtimeDir string
	dbusAddr := um.DBusAddress
	if cfg.User != "" {
		if u, err := user.Lookup(cfg.User); err == nil {
			runtimeDir = "/run/user/" + u.Uid
			if dbusAddr == "" {
				dbusAddr = "unix:path=" + runtimeDir + "/bus"
			}
		} else {
			log.Printf("usage monitor: cannot resolve user %q (%v)", cfg.User, err)
		}
	}
	source := usage.NewAutoSource(usage.Options{
		Display: um.Display, XAuthority: um.XAuthority, DBusAddress: dbusAddr, RuntimeDir: runtimeDir,
	}, log.Printf)
	defer source.Close()

	sink, err := usage.NewJSONLFileSink(logFile)
	if err != nil {
		log.Printf("usage monitor: cannot open log %s: %v", logFile, err)
		return
	}
	defer sink.Close()

	tracker := usage.NewTracker(source, sink, usage.Config{
		Interval: interval,
		OnError:  func(err error) { slog.Debug("usage monitor sample error", "err", err) },
	})
	log.Printf("usage monitor: sampling every %s to %s", interval, logFile)
	if err := tracker.Run(context.Background()); err != nil && err != context.Canceled {
		log.Printf("usage monitor stopped: %v", err)
	}
}
