package usage

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/godbus/dbus/v5"

	"glocker/internal/utils"
)

// ErrUnsupportedSession means no usage backend fits the current session — a
// Wayland session before its backend exists, an unknown/headless session, or a
// non-Linux OS. Callers should treat it as "usage tracking unavailable here" and
// carry on, not fail.
var ErrUnsupportedSession = errors.New("usage: unsupported session")

// Options configures source selection. Fields are backend-specific and ignored
// by backends that don't use them.
type Options struct {
	Display     string // X11 DISPLAY override (empty = $DISPLAY)
	XAuthority  string // X11 XAUTHORITY override (empty = inherit)
	DBusAddress string // Wayland session-bus address (empty = $DBUS_SESSION_BUS_ADDRESS)
	// RuntimeDir is the desktop user's runtime dir (/run/user/<uid>), where the
	// Wayland and Hyprland sockets live. Empty = $XDG_RUNTIME_DIR. The root
	// daemon sets it, since it has none of the session's environment.
	RuntimeDir string
}

// NewSource picks a Source for the current desktop session and returns it with a
// short backend name (e.g. "linux/x11") for logging. It is the single place that
// maps an environment to a backend, hiding the OS and compositor behind the
// Source interface.
//
// Today on Linux: X11, Hyprland (IPC + ext-idle-notify) and GNOME/Wayland (via
// the glocker Shell extension). Other Wayland compositors return
// ErrUnsupportedSession; other OSes are future work — add a case to the switch.
func NewSource(opts Options) (Source, string, error) {
	switch runtime.GOOS {
	case "linux":
		return newLinuxSource(opts)
	default:
		return nil, "", fmt.Errorf("%w: %s is not supported", ErrUnsupportedSession, runtime.GOOS)
	}
}

// detectLinuxSession returns "x11", "wayland", or "" (unknown) for the current
// Linux session.
//
// A reachable Wayland compositor (WAYLAND_DISPLAY) is the strongest signal and
// is checked first: it's set even in a nested Wayland-in-X11 session, where
// XDG_SESSION_TYPE still reports the *outer* "x11" and DISPLAY points at
// XWayland — using the X11 backend there would see the wrong (or no) windows.
// Only when there's no Wayland socket do we fall back to XDG_SESSION_TYPE /
// DISPLAY for X11.
func detectLinuxSession(opts Options) string {
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("XDG_SESSION_TYPE") == "wayland" {
		return "wayland"
	}
	if opts.Display != "" || os.Getenv("DISPLAY") != "" || os.Getenv("XDG_SESSION_TYPE") == "x11" {
		return "x11"
	}
	return ""
}

func newLinuxSource(opts Options) (Source, string, error) {
	if opts.XAuthority != "" {
		os.Setenv("XAUTHORITY", opts.XAuthority)
	}
	runtimeDir := opts.RuntimeDir
	if runtimeDir == "" {
		runtimeDir = os.Getenv("XDG_RUNTIME_DIR")
	}
	// A live Hyprland instance is the most specific signal and needs no
	// environment, so check it first — before a configured X display, which on
	// Hyprland would only reach XWayland.
	if inst, ok := findHyprland(runtimeDir); ok {
		return newHyprlandSource(runtimeDir, inst), "linux/wayland/hyprland", nil
	}
	session := detectLinuxSession(opts)
	if session != "wayland" && utils.LiveWaylandSocket(runtimeDir) != "" {
		session = "wayland" // the root daemon sees no WAYLAND_DISPLAY; the socket is the proof
	}
	switch session {
	case "x11":
		src, err := NewX11SourceDisplay(opts.Display)
		return src, "linux/x11", err
	case "wayland":
		return newWaylandSource(opts)
	default:
		return nil, "", fmt.Errorf("%w: no X11 or Wayland session detected", ErrUnsupportedSession)
	}
}

// newWaylandSource sub-selects a Wayland backend by compositor, using the
// session bus (opts.DBusAddress, or the environment). GNOME is detected by
// GNOME Shell being on the bus — more reliable than XDG_CURRENT_DESKTOP, which a
// nested session inherits from its parent. (Hyprland is picked earlier, in
// newLinuxSource.) Other wlroots compositors and KDE come later.
func newWaylandSource(opts Options) (Source, string, error) {
	conn, err := connectBus(opts.DBusAddress)
	if err != nil {
		return nil, "", fmt.Errorf("%w: no session bus (%v)", ErrUnsupportedSession, err)
	}
	if nameHasOwner(conn, "org.gnome.Shell") {
		// GNOME needs our extension for the window list. If it isn't up, fail with
		// an actionable message instead of picking a backend that errors every capture.
		if !nameHasOwner(conn, glockerBridgeDest) {
			conn.Close()
			return nil, "", fmt.Errorf("%w: GNOME/Wayland detected, but the glocker GNOME Shell extension isn't installed/enabled (see extensions/gnome)",
				ErrUnsupportedSession)
		}
		return NewGNOMESource(conn), "linux/wayland/gnome", nil
	}
	if nameHasOwner(conn, "org.kde.KWin") {
		// KDE Plasma / KWin on Wayland. Unlike GNOME we can't check for the script
		// up front (the daemon, not the script, owns the bridge name); the KWin
		// script pushes to us. NewKWinSource claims app.glocker.KWin; the first
		// Capture is empty until the script's first push. Idle via ext-idle-notify.
		runtimeDir := opts.RuntimeDir
		if runtimeDir == "" {
			runtimeDir = os.Getenv("XDG_RUNTIME_DIR")
		}
		src, err := NewKWinSource(conn, runtimeDir, utils.LiveWaylandSocket(runtimeDir))
		if err != nil {
			return nil, "", fmt.Errorf("%w: KDE/Wayland: %v", ErrUnsupportedSession, err)
		}
		return src, "linux/wayland/kwin", nil
	}
	conn.Close()
	return nil, "", fmt.Errorf("%w: Wayland session (%s) has no supported backend yet",
		ErrUnsupportedSession, envOr("XDG_CURRENT_DESKTOP", "unknown"))
}

// connectBus opens a private session-bus connection: to addr if given, else the
// bus named by the environment. The caller owns and must Close it.
func connectBus(addr string) (*dbus.Conn, error) {
	if addr != "" {
		return dbus.Connect(addr)
	}
	return dbus.ConnectSessionBus()
}

// nameHasOwner reports whether a well-known name is currently owned on conn.
func nameHasOwner(conn *dbus.Conn, name string) bool {
	var has bool
	err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&has)
	return err == nil && has
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// AutoSource is a Source that selects its backend lazily and re-selects after a
// failure. The daemon starts at boot, usually before anyone has logged in, and a
// session can end or its compositor restart at any time, so a backend chosen
// once at startup would go stale. Each Capture (re)selects if needed. Errors
// are still returned, but logged only when they change, so a machine with no
// session doesn't log every interval.
type AutoSource struct {
	opts      Options
	logf      func(string, ...any)
	newSource func(Options) (Source, string, error) // NewSource; swapped in tests

	cur     Source
	name    string
	lastErr string
}

// NewAutoSource returns an AutoSource; logf receives backend changes/failures.
func NewAutoSource(opts Options, logf func(string, ...any)) *AutoSource {
	return &AutoSource{opts: opts, logf: logf, newSource: NewSource}
}

func (a *AutoSource) Capture() (Sample, error) {
	if a.cur == nil {
		src, name, err := a.newSource(a.opts)
		if err != nil {
			a.note("usage monitor: no usable backend yet (%v); retrying each interval", err)
			return Sample{}, err
		}
		a.cur, a.name, a.lastErr = src, name, ""
		a.logf("usage monitor: using %s backend", name)
	}
	sample, err := a.cur.Capture()
	if err != nil {
		a.note("usage monitor: %s backend failed (%v); re-selecting", a.name, err)
		a.cur.Close()
		a.cur = nil
		return sample, err
	}
	a.lastErr = ""
	return sample, nil
}

// note logs msg unless it's the same as the previous one.
func (a *AutoSource) note(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if msg != a.lastErr {
		a.logf("%s", msg)
		a.lastErr = msg
	}
}

// Close releases the current backend, if any.
func (a *AutoSource) Close() error {
	if a.cur != nil {
		err := a.cur.Close()
		a.cur = nil
		return err
	}
	return nil
}
