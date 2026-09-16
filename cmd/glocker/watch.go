package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"glocker/internal/cli"
	"glocker/internal/config"
	"glocker/internal/ipc"
)

// statusFromSocket dials the running daemon and returns the status body (every
// line up to the END sentinel, joined). The second return is false when the
// socket can't be reached — typically because this process lacks root, since
// the socket lives in a 0700 root-owned dir.
func statusFromSocket() (string, bool) {
	conn, err := net.Dial("unix", ipc.SocketPath)
	if err != nil {
		return "", false
	}
	defer conn.Close()

	conn.Write([]byte("status\n"))
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

// statusStatic renders the fallback report from on-disk config when the daemon
// socket can't be reached. It picks an honest service state: "unknown" when we
// merely lack root (the daemon is very likely still running, we just can't
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

// runStatusWatch repaints the status on the alternate screen every few seconds,
// animating a braille spinner + countdown until the next refresh, until Ctrl-C.
func runStatusWatch() {
	fetch := func() string {
		if body, live := statusFromSocket(); live {
			return body
		}
		return statusStatic()
	}

	// A live view only makes sense on a terminal; piped, just print once.
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Print(stripANSI(fetch()))
		return
	}

	fmt.Print("\x1b[?1049h\x1b[?25l")       // alt screen + hide cursor
	defer fmt.Print("\x1b[?25h\x1b[?1049l") // restore on exit
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	braille := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	const interval = 2 * time.Second
	const tick = 90 * time.Millisecond
	frame := 0

	// paint repaints in place: home the cursor, overwrite every line (each
	// cleared to end-of-line), then clear anything left below a now-shorter
	// frame. No full-screen wipe first, so the redraw doesn't flash.
	paint := func(body string) {
		fmt.Print("\x1b[H" + strings.ReplaceAll(body, "\n", "\x1b[K\n") + "\x1b[J")
	}

	for {
		paint(fetch())

		deadline := time.Now().Add(interval)
		for {
			remain := time.Until(deadline)
			if remain <= 0 {
				break
			}
			secs := int(remain.Round(time.Second) / time.Second)
			fmt.Printf("\r  %s %s\x1b[K",
				watchAccent(string(braille[frame%len(braille)])),
				watchDim(fmt.Sprintf("refreshing in %ds · ctrl-c to quit", secs)))
			frame++
			select {
			case <-sig:
				return
			case <-time.After(tick):
			}
		}
	}
}

// Small truecolor helpers for the watch UI (package cli's are unexported).
func watchColor(r, g, b int, s string) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", r, g, b, s)
}

func watchAccent(s string) string { return watchColor(200, 160, 85, s) }
func watchDim(s string) string    { return watchColor(105, 115, 130, s) }
