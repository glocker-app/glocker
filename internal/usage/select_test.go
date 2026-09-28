package usage

import (
	"errors"
	"testing"
)

func TestDetectLinuxSession(t *testing.T) {
	cases := []struct {
		name                       string
		sessionType, wayland, disp string
		optDisplay                 string
		want                       string
	}{
		{"explicit x11", "x11", "", "", "", "x11"},
		{"explicit wayland", "wayland", "", ":0", "", "wayland"}, // wayland wins even with DISPLAY set (XWayland)
		{"wayland via WAYLAND_DISPLAY", "", "wayland-0", "", "", "wayland"},
		{"nested wayland: session says x11 but WAYLAND_DISPLAY set", "x11", "wayland-0", ":0", "", "wayland"},
		{"x11 via DISPLAY", "", "", ":0", "", "x11"},
		{"x11 via opts.Display", "", "", "", ":1", "x11"},
		{"nothing", "", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_SESSION_TYPE", c.sessionType)
			t.Setenv("WAYLAND_DISPLAY", c.wayland)
			t.Setenv("DISPLAY", c.disp)
			if got := detectLinuxSession(Options{Display: c.optDisplay}); got != c.want {
				t.Errorf("detectLinuxSession = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNewSourceWaylandUnreachableBusDegrades(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "wayland")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // no real Hyprland/Wayland sockets
	// A Wayland session whose bus can't be reached must degrade gracefully rather
	// than fail hard (host-independent, unlike asserting Wayland is unsupported).
	_, _, err := NewSource(Options{DBusAddress: "unix:path=/nonexistent/glocker-test-bus"})
	if !errors.Is(err, ErrUnsupportedSession) {
		t.Errorf("unreachable Wayland bus err = %v, want ErrUnsupportedSession", err)
	}
}

func TestNewSourceNoSessionIsUnsupported(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if _, _, err := NewSource(Options{}); !errors.Is(err, ErrUnsupportedSession) {
		t.Errorf("no-session NewSource err = %v, want ErrUnsupportedSession", err)
	}
}
