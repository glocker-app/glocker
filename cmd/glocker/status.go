package main

import (
	"bufio"
	"net"
	"os"
	"strings"

	"glocker/internal/cli"
	"glocker/internal/config"
	"glocker/internal/ipc"
)

// reportFromSocket dials the running daemon, sends cmd, and returns the body
// (every line up to the END sentinel, joined). The second return is false when
// the socket can't be reached — typically because this process lacks access to
// the root:glocker socket.
func reportFromSocket(cmd string) (string, bool) {
	conn, err := net.Dial("unix", ipc.SocketPath)
	if err != nil {
		return "", false
	}
	defer conn.Close()

	conn.Write([]byte(cmd + "\n"))
	var b strings.Builder
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if sc.Text() == "END" {
			break
		}
		b.WriteString(sc.Text())
		b.WriteByte('\n')
	}
	return b.String(), true
}

// statusFromSocket returns the live -status body from the daemon.
func statusFromSocket() (string, bool) { return reportFromSocket("status") }

// infoStatic renders the -info report from on-disk config when the daemon
// socket can't be reached.
func infoStatic() string {
	cfg, err := config.LoadConfig()
	if err != nil {
		return "  could not load config: " + err.Error() + "\n"
	}
	config.SetupLogging(cfg)
	return strings.TrimSuffix(cli.GetInfoResponse(cfg), "\nEND\n") + "\n"
}

// statusStatic renders the fallback report from on-disk config when the daemon
// socket can't be reached. It picks an honest service state: "unknown" when we
// merely lack access (the daemon is very likely still running, we just can't
// query it), "down" only when we could have reached it and nothing was there.
func statusStatic() string {
	cfg, err := config.LoadConfig()
	if err != nil {
		return "  could not load config: " + err.Error() + "\n"
	}
	config.SetupLogging(cfg)

	svc := cli.ServiceDown
	if os.Geteuid() != 0 {
		svc = cli.ServiceUnknown
	}
	return strings.TrimSuffix(cli.GetStatusResponse(cfg, svc), "\nEND\n") + "\n"
}
