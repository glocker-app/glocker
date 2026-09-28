// Command usageprobe is a throwaway tool for exercising the usage Source
// selector: it picks the backend for the current session, captures Sample(s),
// and prints a readable summary per capture. Use it to check usage tracking on a
// new machine or session — e.g. a nested Wayland compositor
// (`dbus-run-session -- gnome-shell --nested --wayland`, or `sway`) — without
// installing the daemon. It only prints to the screen; it never writes a file or
// the database.
//
//	go run ./cmd/usageprobe                 # one capture
//	go run ./cmd/usageprobe -interval 2s    # watch the active window as you switch apps
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"glocker/internal/usage"
)

func main() {
	display := flag.String("display", "", "X11 DISPLAY override (empty = $DISPLAY)")
	dbusAddr := flag.String("dbus", "", "Wayland session-bus address override (empty = $DBUS_SESSION_BUS_ADDRESS)")
	runtimeDir := flag.String("runtime-dir", "", "session runtime dir, e.g. /run/user/1000 (empty = $XDG_RUNTIME_DIR)")
	interval := flag.Duration("interval", 0, "capture repeatedly at this interval (0 = once)")
	flag.Parse()

	src, backend, err := usage.NewSource(usage.Options{
		Display:     *display,
		XAuthority:  os.Getenv("XAUTHORITY"),
		DBusAddress: *dbusAddr,
		RuntimeDir:  *runtimeDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "no usage source: %v\n", err)
		os.Exit(1)
	}
	defer src.Close()
	fmt.Fprintf(os.Stderr, "backend: %s\n", backend)

	for {
		sample, err := src.Capture()
		if err != nil {
			fmt.Fprintf(os.Stderr, "capture: %v\n", err)
			os.Exit(1)
		}
		printSample(sample)
		if *interval <= 0 {
			return
		}
		time.Sleep(*interval)
	}
}

// printSample renders one capture as a header line (time + active window + idle)
// followed by the full window list, then a separator.
func printSample(s usage.Sample) {
	head := "idle / no active window"
	if a := s.Active(); a != nil {
		head = a.Class
		if a.Title != "" {
			head += " — " + a.Title
		}
	}
	idle := "unknown"
	if s.IdleMS >= 0 {
		idle = fmt.Sprintf("%.1fs", float64(s.IdleMS)/1000)
	}
	fmt.Printf("%s : %s   (idle %s)\n", s.Timestamp.Format("15:04:05"), head, idle)

	for _, w := range s.Windows {
		mark := "  "
		if w.Active {
			mark = "* "
		}
		title := w.Title
		if title == "" {
			title = "(no title)"
		}
		fmt.Printf("   - %s%s · %s\n", mark, w.Class, title)
	}
	fmt.Println("---------------------------------------------")
}
