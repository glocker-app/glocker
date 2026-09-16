package ipc

import (
	"bufio"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"glocker/internal/cli"
	"glocker/internal/config"
	"glocker/internal/enforcement"
	"glocker/internal/install"
	"glocker/internal/web"
)

// SocketPath is the Unix domain socket the daemon listens on. It lives in a
// dedicated root-owned 0700 directory (see config.GlockerSock) so the directory
// itself gates access — a non-root user can't even traverse into it, unlike the
// old world-writable /tmp location.
const SocketPath = config.GlockerSock

// SetupCommunication creates and starts listening on the Unix domain socket.
func SetupCommunication(cfg *config.Config) error {
	// Ensure the parent directory exists and is root-only (0700). This is the
	// primary access control: only root can traverse into it to reach the socket.
	dir := filepath.Dir(SocketPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create socket directory %s: %w", dir, err)
	}
	if err := os.Chown(dir, 0, 0); err != nil {
		log.Printf("Warning: couldn't chown socket directory to root: %v", err)
	}
	// MkdirAll won't tighten an existing dir's mode, so enforce 0700 explicitly.
	if err := os.Chmod(dir, 0700); err != nil {
		log.Printf("Warning: couldn't set socket directory permissions: %v", err)
	}

	// Remove existing socket
	os.Remove(SocketPath)

	// Create Unix domain socket
	listener, err := net.Listen("unix", SocketPath)
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}

	// Set permissions
	if err := os.Chmod(SocketPath, 0600); err != nil {
		log.Printf("Warning: couldn't set socket permissions: %v", err)
	}

	go handleConnections(cfg, listener)
	return nil
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

// HandleConnection processes commands from a single socket connection.
func HandleConnection(cfg *config.Config, conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 1 {
			conn.Write([]byte("ERROR: Invalid format\n"))
			continue
		}

		action := strings.TrimSpace(parts[0])
		slog.Debug("Socket command received", "action", action)

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
				continue
			}
			payload := strings.TrimSpace(parts[1])
			payloadParts := strings.SplitN(payload, ":", 2)
			if len(payloadParts) != 2 {
				conn.Write([]byte("ERROR: Reason required. Use 'unblock:domains:reason'\n"))
				continue
			}
			domains := strings.TrimSpace(payloadParts[0])
			reason := strings.TrimSpace(payloadParts[1])
			if reason == "" {
				conn.Write([]byte("ERROR: Reason cannot be empty\n"))
				continue
			}
			// Process unblock request and check for errors
			if err := cli.ProcessUnblockRequest(cfg, domains, reason); err != nil {
				conn.Write([]byte(fmt.Sprintf("ERROR: %v\n", err)))
				continue
			}
			conn.Write([]byte("OK: Unblock request received\n"))
		case "block":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'block:domains'\n"))
				continue
			}
			domains := strings.TrimSpace(parts[1])
			conn.Write([]byte("OK: Block request received\n"))
			go cli.ProcessBlockRequest(cfg, domains)
		case "block-app":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'block-app:programs'\n"))
				continue
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
				continue
			}
			payload := strings.TrimSpace(parts[1])
			payloadParts := strings.SplitN(payload, ":", 2)
			if len(payloadParts) != 2 {
				conn.Write([]byte("ERROR: Reason required. Use 'extend:program:reason'\n"))
				continue
			}
			program := strings.TrimSpace(payloadParts[0])
			reason := strings.TrimSpace(payloadParts[1])
			if program == "" || reason == "" {
				conn.Write([]byte("ERROR: Program and reason cannot be empty\n"))
				continue
			}
			if err := cli.ProcessExtendRequest(cfg, program, reason); err != nil {
				conn.Write([]byte(fmt.Sprintf("ERROR: %v\n", err)))
				continue
			}
			conn.Write([]byte(fmt.Sprintf("OK: Extension granted for %s\n", program)))
		case "lock":
			conn.Write([]byte("OK: Lock request received\n"))
			go processLockRequest(cfg)
		case "add-keyword":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'add-keyword:keywords'\n"))
				continue
			}
			keywords := strings.TrimSpace(parts[1])
			conn.Write([]byte("OK: Add keyword request received\n"))
			go processAddKeywordRequest(cfg, keywords)
		case "uninstall":
			if len(parts) != 2 {
				conn.Write([]byte("ERROR: Invalid format. Use 'uninstall:reason[:note]'\n"))
				continue
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
				continue
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
					continue
				}
			}
			conn.Write([]byte("OK: Uninstall request received\n"))
			go processUninstallRequest(cfg, reason, note, conn)
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
	if err := install.RestoreSystemChanges(cfg); err != nil {
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
	os.Exit(0)
}
