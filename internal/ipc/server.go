package ipc

import (
	"bufio"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"glocker/internal/cli"
	"glocker/internal/config"
	"glocker/internal/enforcement"
	"glocker/internal/install"
	"glocker/internal/syncer"
	"glocker/internal/web"
)

// SocketPath is the Unix domain socket the daemon listens on. It lives in a
// dedicated root-owned 0700 directory (see config.GlockerSock) so the directory
// itself gates access — a non-root user can't even traverse into it, unlike the
// old world-writable /tmp location.
const SocketPath = config.GlockerSock

// SetupCommunication creates and starts listening on the Unix domain socket.
func SetupCommunication(cfg *config.Config) error {
	// Access control on the socket. The binary is NOT setuid (dropped 2026-09-30),
	// so a client runs as the invoking user. To let the managed user reach the
	// daemon without setuid or sudo, the socket dir + socket are owned root:glocker
	// (0750 / 0660) and `glocker -install` puts the user in the glocker group.
	// Privileged commands are still gated by the peer's real uid (see HandleConnection),
	// so group access only grants the safe, self-management commands. If the group
	// doesn't exist (e.g. an old install), fall back to root-only 0700/0600.
	gid := glockerGID()
	dirMode, sockMode := os.FileMode(0700), os.FileMode(0600)
	chownGID := 0
	if gid >= 0 {
		dirMode, sockMode, chownGID = 0750, 0660, gid
	}
	dir := filepath.Dir(SocketPath)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("failed to create socket directory %s: %w", dir, err)
	}
	if err := os.Chown(dir, 0, chownGID); err != nil {
		log.Printf("Warning: couldn't chown socket directory: %v", err)
	}
	// MkdirAll won't tighten an existing dir's mode, so enforce it explicitly.
	if err := os.Chmod(dir, dirMode); err != nil {
		log.Printf("Warning: couldn't set socket directory permissions: %v", err)
	}

	// Refuse to start if a daemon is already listening: removing the socket below
	// would otherwise hijack the running (systemd) daemon — the old one becomes
	// unreachable while both keep enforcing. A stale socket (nothing answering) is
	// safe to replace. `glocker -daemon` also refuses to run unless real root now
	// that the binary is not setuid, so a plain user can't start a rival daemon.
	if daemonAlive(SocketPath) {
		return fmt.Errorf("a glocker daemon is already running (socket %s is live); refusing to start a second one", SocketPath)
	}
	// Remove a stale socket file left by an unclean exit.
	os.Remove(SocketPath)

	// Create Unix domain socket
	listener, err := net.Listen("unix", SocketPath)
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}

	// Set permissions (0660 root:glocker when the group exists, else 0600 root).
	if err := os.Chmod(SocketPath, sockMode); err != nil {
		log.Printf("Warning: couldn't set socket permissions: %v", err)
	}
	if chownGID != 0 {
		if err := os.Chown(SocketPath, 0, chownGID); err != nil {
			log.Printf("Warning: couldn't chown socket to the glocker group: %v", err)
		}
	}

	go handleConnections(cfg, listener)
	return nil
}

// glockerGID returns the numeric gid of the "glocker" group, or -1 if it doesn't
// exist (an old, pre-group install → the socket stays root-only).
func glockerGID() int {
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

// privilegedActions are socket commands that tamper with enforcement, re-push data,
// or send mail — they require the caller's REAL uid to be root (i.e. invoked via
// sudo), not merely membership in the glocker group. Everything else is
// self-management the managed user may run directly.
var privilegedActions = map[string]bool{
	"uninstall": true, "resync": true,
}

// peerUIDFn resolves the connecting peer's real uid; a package var so tests can
// stub it (a net.Pipe has no SO_PEERCRED). Production uses peerUID.
var peerUIDFn = peerUID

// peerUID returns the real uid of the process on the other end of a Unix socket
// (SO_PEERCRED). It's how the daemon tells a sudo caller (uid 0) from a plain
// group-member client now that the binary is no longer setuid.
func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("connection is not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return cred.Uid, nil
}

// handleConnections accepts incoming socket connections.
func handleConnections(cfg *config.Config, listener net.Listener) {
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Socket accept error: %v", err)
			continue
		}

		go HandleConnection(cfg, conn)
	}
}

// daemonAlive reports whether a glocker daemon is already listening on path — a
// live socket answers "ping" with "pong". A missing/stale socket returns false.
func daemonAlive(path string) bool {
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		return false
	}
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	return err == nil && strings.Contains(string(buf[:n]), "pong")
}

// HandleConnection processes commands from a single socket connection.
func HandleConnection(cfg *config.Config, conn net.Conn) {
	defer conn.Close()

	// One command per connection: read a SINGLE line, dispatch it, then close (the
	// deferred conn.Close). The daemon used to loop over every line, so a newline
	// embedded in a command argument could inject a second, separate command;
	// reading exactly one closes that at the daemon itself, regardless of who
	// connects. Unknown actions fall to the default case and are rejected.
	scanner := bufio.NewScanner(conn)
	if scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			return
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 1 {
			conn.Write([]byte("ERROR: Invalid format\n"))
			return
		}

		action := strings.TrimSpace(parts[0])
		slog.Debug("Socket command received", "action", action)

		// Privileged commands require the caller's real uid to be root (sudo). Group
		// membership alone (which reaches the socket) is not enough — it only grants
		// the safe self-management commands. This replaces the old setuid model, where
		// any local user connected as root.
		if privilegedActions[action] {
			if uid, err := peerUIDFn(conn); err != nil || uid != 0 {
				slog.Warn("rejecting privileged command from non-root peer", "action", action, "uid", uid, "err", err)
				conn.Write([]byte("ERROR: this command requires root — run it with sudo\n"))
				return
			}
		}

		switch action {
		case "ping":
			// Liveness probe used by the glockdoc watchdog: a reply proves the
			// daemon is running and responsive, not merely that the socket file
			// exists.
			conn.Write([]byte("pong\n"))
		case "status":
			response := cli.GetStatusResponse(cfg, cli.ServiceOnline)
			conn.Write([]byte(response))
		case "info":
			response := cli.GetInfoResponse(cfg)
			conn.Write([]byte(response))
		case "unblock":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'unblock:domains:reason'\n"))
				return
			}
			payload := strings.TrimSpace(parts[1])
			payloadParts := strings.SplitN(payload, ":", 2)
			if len(payloadParts) != 2 {
				conn.Write([]byte("ERROR: Reason required. Use 'unblock:domains:reason'\n"))
				return
			}
			domains := strings.TrimSpace(payloadParts[0])
			reason := strings.TrimSpace(payloadParts[1])
			if reason == "" {
				conn.Write([]byte("ERROR: Reason cannot be empty\n"))
				return
			}
			// Process unblock request and check for errors
			if err := cli.ProcessUnblockRequest(cfg, domains, reason); err != nil {
				conn.Write([]byte(fmt.Sprintf("ERROR: %v\n", err)))
				return
			}
			conn.Write([]byte("OK: Unblock request received\n"))
		case "block":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'block:domains'\n"))
				return
			}
			domains := strings.TrimSpace(parts[1])
			conn.Write([]byte("OK: Block request received\n"))
			go cli.ProcessBlockRequest(cfg, domains)
		case "block-app":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'block-app:programs'\n"))
				return
			}
			programs := strings.TrimSpace(parts[1])
			conn.Write([]byte("OK: Block-app request received\n"))
			go cli.ProcessBlockAppRequest(cfg, programs)
		case "panic":
			conn.Write([]byte("OK: Entering panic mode\n"))
			go cli.ProcessPanicRequest(cfg)
		case "extend":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'extend:program:reason'\n"))
				return
			}
			payload := strings.TrimSpace(parts[1])
			payloadParts := strings.SplitN(payload, ":", 2)
			if len(payloadParts) != 2 {
				conn.Write([]byte("ERROR: Reason required. Use 'extend:program:reason'\n"))
				return
			}
			program := strings.TrimSpace(payloadParts[0])
			reason := strings.TrimSpace(payloadParts[1])
			if program == "" || reason == "" {
				conn.Write([]byte("ERROR: Program and reason cannot be empty\n"))
				return
			}
			if err := cli.ProcessExtendRequest(cfg, program, reason); err != nil {
				conn.Write([]byte(fmt.Sprintf("ERROR: %v\n", err)))
				return
			}
			conn.Write([]byte(fmt.Sprintf("OK: Extension granted for %s\n", program)))
		case "lock":
			conn.Write([]byte("OK: Lock request received\n"))
			go processLockRequest(cfg)
		case "resync":
			// Rewind the syncer's cursors and backfill, re-pushing local records so
			// glockpeek's copies are overwritten (idempotent upsert). Optional
			// payload is a "since" TS in ms; absent/blank means full history.
			var since int64
			if len(parts) == 2 {
				if v, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64); err == nil {
					since = v
				}
			}
			if syncer.Resync(since) {
				if since > 0 {
					conn.Write([]byte(fmt.Sprintf("OK: resync triggered (backfill from %d)\n", since)))
				} else {
					conn.Write([]byte("OK: resync triggered (full backfill)\n"))
				}
			} else {
				conn.Write([]byte("ERROR: syncer is not running (sync disabled, or encryption key unusable)\n"))
			}
		case "add-keyword":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'add-keyword:keywords'\n"))
				return
			}
			keywords := strings.TrimSpace(parts[1])
			conn.Write([]byte("OK: Add keyword request received\n"))
			go processAddKeywordRequest(cfg, keywords)
		case "uninstall":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'uninstall:reason[:note]'\n"))
				return
			}
			payload := strings.TrimSpace(parts[1])
			payloadParts := strings.SplitN(payload, ":", 2)
			reason := strings.TrimSpace(payloadParts[0])
			note := ""
			if len(payloadParts) == 2 {
				note = strings.TrimSpace(payloadParts[1])
			}
			if reason == "" {
				conn.Write([]byte("ERROR: Reason cannot be empty\n"))
				return
			}
			// Validate reason against configured valid reasons
			if len(cfg.Lifecycle.Reasons) > 0 {
				validReason := false
				for _, validR := range cfg.Lifecycle.Reasons {
					if strings.EqualFold(reason, validR) {
						validReason = true
						break
					}
				}
				if !validReason {
					errMsg := fmt.Sprintf("ERROR: Invalid reason '%s'. Valid reasons: %s\n",
						reason, strings.Join(cfg.Lifecycle.Reasons, ", "))
					conn.Write([]byte(errMsg))
					return
				}
			}
			conn.Write([]byte("OK: Uninstall request received\n"))
			// Synchronous, not `go`: one command per connection means HandleConnection
			// returns right after this and its deferred conn.Close() fires. A background
			// goroutine would then race a closed socket and never deliver COMPLETED (the
			// client sees EOF and aborts its systemd teardown). Running it here keeps the
			// socket open until COMPLETED is sent; it ends in os.Exit, so it never falls
			// through to read a second command.
			processUninstallRequest(cfg, reason, note, conn)
		default:
			conn.Write([]byte("ERROR: Unknown action\n"))
		}
	}
}

// SendSocketMessage sends a message to the glocker socket and returns the response.
func SendSocketMessage(action, payload string) (string, error) {
	conn, err := net.Dial("unix", SocketPath)
	if err != nil {
		return "", fmt.Errorf("failed to connect to socket: %w", err)
	}
	defer conn.Close()

	message := action
	if payload != "" {
		message += ":" + payload
	}
	message += "\n"

	if _, err := conn.Write([]byte(message)); err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	var response strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "END" {
			break
		}
		response.WriteString(line)
		response.WriteString("\n")
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	return response.String(), nil
}

// processLockRequest forces sudoers to be locked immediately.
func processLockRequest(cfg *config.Config) {
	slog.Debug("Processing lock request - forcing sudoers block")

	now := time.Now()
	// Force block sudoers regardless of time windows (forceBlock = true)
	if err := enforcement.UpdateSudoers(cfg, now, false, true); err != nil {
		log.Printf("ERROR: Failed to lock sudoers: %v", err)
	} else {
		enforcement.SetSudoForceLocked(true) // so -status shows the forced lock
		log.Println("✓ Sudoers access locked")
	}
}

// processAddKeywordRequest adds keywords to both URL and content keyword lists.
func processAddKeywordRequest(cfg *config.Config, keywordsStr string) {
	slog.Debug("Processing add-keyword request", "keywords", keywordsStr)

	keywords := strings.Split(keywordsStr, ",")
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}

		// Add to both URL and content keywords
		cfg.Content.Keywords.URLKeywords = append(cfg.Content.Keywords.URLKeywords, keyword)
		cfg.Content.Keywords.ContentKeywords = append(cfg.Content.Keywords.ContentKeywords, keyword)

		log.Printf("KEYWORD ADDED: %s", keyword)
	}

	// TODO: Persist to config file and broadcast to SSE clients
}

// processUninstallRequest handles the uninstallation process.
func processUninstallRequest(cfg *config.Config, reason, note string, conn net.Conn) {
	if note != "" {
		log.Printf("Uninstall requested: %s (note: %s)", reason, note)
	} else {
		log.Printf("Uninstall requested: %s", reason)
	}

	// Log the uninstall request
	if err := web.LogUninstallEntry(cfg, reason, note); err != nil {
		log.Printf("Warning: Failed to log uninstall entry: %v", err)
	}

	// Restore system changes
	if err := uninstallRestore(cfg); err != nil {
		log.Printf("ERROR: Failed to restore system changes: %v", err)
		conn.Write([]byte(fmt.Sprintf("ERROR: Failed to restore system: %v\n", err)))
		return
	}

	// Send completion signal
	conn.Write([]byte("COMPLETED: System changes restored\n"))

	// Give time for the message to be sent
	time.Sleep(100 * time.Millisecond)

	log.Println("Uninstall complete. Exiting daemon.")

	// Exit daemon
	uninstallExit(0)
}

// Injection points so the uninstall handshake can be exercised in a test without
// restoring the real system or terminating the test process.
var (
	uninstallRestore = install.RestoreSystemChanges
	uninstallExit    = os.Exit
)
