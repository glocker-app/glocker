package usage

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// KWinSource reads window state on KDE Plasma / KWin on Wayland. KWin blocks apps
// from querying windows, and a KWin script — unlike a GNOME Shell extension —
// cannot export a D-Bus interface, only call out. So the roles are flipped
// relative to GNOMESource: the capture agent owns app.glocker.KWin on the user's
// session bus and exports Push(json); the glocker KWin script (extensions/kwin)
// pushes the window list to it on every window event. Capture returns the last
// push.
//
// The agent runs as the desktop user (the daemon re-execs it uid-dropped — see
// RunAgent), so claiming the name and receiving the script's callDBus both
// happen on the user's own bus. The root daemon couldn't do this: dbus-broker
// refuses its cross-uid connection, and callDBus can only reach the session bus.
//
// Idle time comes from ext-idle-notify-v1 (shared waylandIdle, as on Hyprland),
// since the script doesn't report it.
type KWinSource struct {
	conn   *dbus.Conn
	bridge *kwinBridge
	idle   *waylandIdle // nil if the idle protocol is unavailable → IdleMS -1
}

const (
	kwinBridgeDest  = "app.glocker.KWin"
	kwinBridgePath  = dbus.ObjectPath("/app/glocker/KWin")
	kwinBridgeIface = "app.glocker.KWin"
)

// kwinBridge holds the most recent window list pushed by the KWin script. Push
// is called over D-Bus from the script; snapshot is read by Capture. Guarded by
// a mutex because the two run on different goroutines (godbus dispatches calls).
type kwinBridge struct {
	mu      sync.Mutex
	windows []Window
}

// Push receives the window list as JSON ([{class,instance,title,active}]) from
// the KWin script and stores it. Exported as the D-Bus method app.glocker.KWin.Push.
func (b *kwinBridge) Push(raw string) *dbus.Error {
	var ws []Window
	if err := json.Unmarshal([]byte(raw), &ws); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("kwin: bad window JSON from script: %w", err))
	}
	b.mu.Lock()
	b.windows = ws
	b.mu.Unlock()
	return nil
}

func (b *kwinBridge) snapshot() []Window {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Window, len(b.windows))
	copy(out, b.windows)
	return out
}

// NewKWinSource takes ownership of conn, exports the Push bridge, and claims
// app.glocker.KWin so the KWin script's callDBus reaches it. runtimeDir +
// waylandSock enable best-effort idle tracking (empty socket → IdleMS -1). The
// caller (the selector) has already verified KWin is on the bus.
func NewKWinSource(conn *dbus.Conn, runtimeDir, waylandSock string) (*KWinSource, error) {
	b := &kwinBridge{}
	if err := conn.Export(b, kwinBridgePath, kwinBridgeIface); err != nil {
		conn.Close()
		return nil, fmt.Errorf("kwin: export bridge: %w", err)
	}
	reply, err := conn.RequestName(kwinBridgeDest, dbus.NameFlagDoNotQueue)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("kwin: request name %s: %w", kwinBridgeDest, err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		return nil, fmt.Errorf("kwin: name %s already owned (another glocker running?)", kwinBridgeDest)
	}

	s := &KWinSource{conn: conn, bridge: b}
	if waylandSock != "" {
		if idle, err := newWaylandIdle(runtimeDir, waylandSock); err == nil {
			s.idle = idle
		}
	}
	return s, nil
}

// Capture returns the last window list the script pushed. Before the first push
// (the script pushes on load and on every window event) Windows is empty — a
// valid, if contentless, sample — rather than an error.
func (s *KWinSource) Capture() (Sample, error) {
	idle := int64(-1)
	if s.idle != nil {
		idle = s.idle.IdleMS()
	}
	return Sample{
		Timestamp: time.Now(),
		IdleMS:    idle,
		Windows:   s.bridge.snapshot(),
	}, nil
}

// Close releases the idle watcher, drops the bus name, and closes the connection.
func (s *KWinSource) Close() error {
	if s.idle != nil {
		s.idle.Close()
	}
	if s.conn != nil {
		_, _ = s.conn.ReleaseName(kwinBridgeDest)
		return s.conn.Close()
	}
	return nil
}
