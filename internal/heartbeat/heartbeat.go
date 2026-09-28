// Package heartbeat implements the glockdoc liveness probe. Run periodically by
// a systemd timer (or root cron job), it pings the glocker IPC socket and appends a one-line JSON
// sample recording whether the daemon answered.
//
// It reads nothing from the glocker config file: every input arrives as an
// explicit parameter (the scheduled command line supplies them as flags). That is
// deliberate — the config is deleted on uninstall, which is exactly when the
// watchdog must keep recording so the gap in "alive" samples captures that
// glocker was torn down.
package heartbeat

import (
	"encoding/json"
	"net"
	"os"
	"strings"
	"time"
)

// Sample is one liveness observation appended to the heartbeat log.
type Sample struct {
	Timestamp time.Time `json:"timestamp"`
	Alive     bool      `json:"alive"`
}

// Probe connects to the unix socket at socketPath, sends a ping, and reports
// whether the daemon replied with a pong within timeout. Any failure — missing
// socket, no listener, slow/garbled reply — reads as not-alive, which is the
// signal we care about.
func Probe(socketPath string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(timeout))

	if _, err := conn.Write([]byte("ping\n")); err != nil {
		return false
	}

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return false
	}
	return strings.Contains(string(buf[:n]), "pong")
}

// Append writes one sample as a JSON line to logPath, creating the file if it
// does not exist.
func Append(logPath string, s Sample) error {
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(line)
	return err
}

// Run performs one probe-and-record cycle: it probes the socket and appends a
// sample stamped `now`, returning whether the daemon was alive. `now` is a
// parameter so the cycle is deterministic under test.
func Run(socketPath, logPath string, timeout time.Duration, now time.Time) (alive bool, err error) {
	alive = Probe(socketPath, timeout)
	return alive, Append(logPath, Sample{Timestamp: now, Alive: alive})
}
