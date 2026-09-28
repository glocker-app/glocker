// Command glockdoc is glocker's liveness watchdog — the doctor that takes
// glocker's pulse. Run periodically by a systemd timer (or a root cron job), it pings the glocker
// IPC socket and appends a one-line JSON sample (timestamp + alive) to the
// heartbeat log.
//
// It intentionally reads nothing from the glocker config file, so it keeps
// recording even after an uninstall removes the config and the daemon — the
// resulting run of "alive:false" samples is exactly what it exists to capture.
// All inputs come from flags, with compiled-in defaults matching what the
// installer bakes into the scheduled command line.
//
//	glockdoc                                   # defaults
//	glockdoc -socket /var/glocker/glocker.sock -log /var/log/glocker/heartbeat.jsonl -timeout 3s
//
// Exit 0 = sample recorded (alive or not); 1 = could not record.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"glocker/internal/config"
	"glocker/internal/heartbeat"
)

func main() {
	socket := flag.String("socket", config.GlockerSock, "path to the glocker IPC socket")
	logPath := flag.String("log", config.DefaultHeartbeatLogFile, "path to the heartbeat JSONL log")
	timeout := flag.Duration("timeout", 3*time.Second, "socket dial/response timeout")
	flag.Parse()

	// The log line is the source of truth; a down daemon is a normal, expected
	// outcome, so it is not an error. Only a failure to record is.
	if _, err := heartbeat.Run(*socket, *logPath, *timeout, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "glockdoc: "+err.Error())
		os.Exit(1)
	}
}
