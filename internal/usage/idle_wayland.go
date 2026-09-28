package usage

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neurlang/wayland/wl"
	"github.com/neurlang/wayland/wlclient"
)

// idleTimeoutMS is the ext-idle-notify threshold. The compositor sends "idled"
// after this much inactivity, so idle times shorter than it read as 0 — the
// resolution of IdleMS on this backend.
const idleTimeoutMS = 1000

// waylandIdle tracks time since the last user input through the
// ext-idle-notify-v1 protocol, which any compositor implementing it (Hyprland,
// sway, KDE…) offers. It prefers the v2 input-idle notification, which ignores
// idle inhibitors: a playing video isn't user activity, matching what X11's
// MIT-SCREEN-SAVER reports.
//
// One goroutine owns the connection and dispatches events. neurlang's Context
// isn't safe to close under a concurrent read, so Close only asks that goroutine
// to stop (waking it with a sync round-trip) and lets it close the connection.
type waylandIdle struct {
	display *wl.Display

	mu        sync.Mutex
	idleSince time.Time // zero while the user is active
	alive     bool

	stopping atomic.Bool
	done     chan struct{}
}

// connectEnvMu serialises the XDG_RUNTIME_DIR swap in connectWayland.
var connectEnvMu sync.Mutex

// connectWayland connects to socket name in runtimeDir. neurlang's Connect only
// resolves names against $XDG_RUNTIME_DIR, which the root daemon doesn't have
// (and must not point at one user's dir permanently), so set it just for the
// dial and restore it.
func connectWayland(runtimeDir, name string) (*wl.Display, error) {
	connectEnvMu.Lock()
	defer connectEnvMu.Unlock()
	old, had := os.LookupEnv("XDG_RUNTIME_DIR")
	os.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	defer func() {
		if had {
			os.Setenv("XDG_RUNTIME_DIR", old)
		} else {
			os.Unsetenv("XDG_RUNTIME_DIR")
		}
	}()
	return wl.Connect(name)
}

func newWaylandIdle(runtimeDir, socketName string) (*waylandIdle, error) {
	display, err := connectWayland(runtimeDir, socketName)
	if err != nil {
		return nil, fmt.Errorf("wayland idle: connect: %w", err)
	}
	w := &waylandIdle{display: display, alive: true, done: make(chan struct{})}

	reg, err := display.GetRegistry()
	if err != nil {
		display.Context().Close()
		return nil, fmt.Errorf("wayland idle: registry: %w", err)
	}
	g := &idleGlobals{registry: reg}
	reg.AddGlobalHandler(g)
	if err := wlclient.DisplayRoundtrip(display); err != nil {
		display.Context().Close()
		return nil, fmt.Errorf("wayland idle: registry roundtrip: %w", err)
	}
	if g.notifier == nil || g.seat == nil {
		display.Context().Close()
		return nil, fmt.Errorf("wayland idle: compositor lacks ext_idle_notifier_v1 or wl_seat")
	}

	n := newIdleNotification(display.Context(), w)
	if g.notifierVersion >= 2 {
		err = g.notifier.getInputIdleNotification(n, idleTimeoutMS, g.seat)
	} else {
		err = g.notifier.getIdleNotification(n, idleTimeoutMS, g.seat)
	}
	if err == nil {
		err = wlclient.DisplayRoundtrip(display)
	}
	if err != nil {
		display.Context().Close()
		return nil, fmt.Errorf("wayland idle: create notification: %w", err)
	}

	go w.run()
	return w, nil
}

// run dispatches events until the connection drops or Close asks it to stop.
func (w *waylandIdle) run() {
	defer close(w.done)
	ctx := w.display.Context()
	for !w.stopping.Load() {
		if err := ctx.Run(); err != nil && err != wl.ErrContextRunProxyNil {
			break
		}
	}
	w.mu.Lock()
	w.alive = false
	w.mu.Unlock()
	ctx.Close()
}

// IdleMS returns milliseconds since the last input, or -1 once the connection
// to the compositor is gone.
func (w *waylandIdle) IdleMS() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.alive {
		return -1
	}
	if w.idleSince.IsZero() {
		return 0
	}
	return time.Since(w.idleSince).Milliseconds()
}

// Close stops the event goroutine, which closes the connection.
func (w *waylandIdle) Close() {
	if w.stopping.Swap(true) {
		return
	}
	_, _ = w.display.Sync() // wake the blocked read; the reply ends the loop
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
	}
}

func (w *waylandIdle) handleIdled() {
	w.mu.Lock()
	// The compositor fires after idleTimeoutMS of inactivity; idle began then.
	w.idleSince = time.Now().Add(-idleTimeoutMS * time.Millisecond)
	w.mu.Unlock()
}

func (w *waylandIdle) handleResumed() {
	w.mu.Lock()
	w.idleSince = time.Time{}
	w.mu.Unlock()
}

// ── Registry ────────────────────────────────────────────────────────────────

type idleGlobals struct {
	registry        *wl.Registry
	seat            *wl.Seat
	notifier        *idleNotifier
	notifierVersion uint32
}

func (g *idleGlobals) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case "wl_seat":
		if g.seat == nil { // the first seat is the user's
			g.seat = wlclient.RegistryBindSeatInterface(g.registry, ev.Name, 1)
		}
	case "ext_idle_notifier_v1":
		g.notifierVersion = min(ev.Version, 2)
		g.notifier = &idleNotifier{}
		g.registry.Context().Register(g.notifier)
		_ = g.registry.Bind(ev.Name, "ext_idle_notifier_v1", g.notifierVersion, g.notifier)
	}
}

// ── ext-idle-notify-v1 proxies (hand-written; neurlang has no binding) ─────

// idleNotifier is ext_idle_notifier_v1. It has no events.
type idleNotifier struct{ wl.BaseProxy }

func (n *idleNotifier) Dispatch(*wl.Event) {}

// Request opcodes: 0 destroy, 1 get_idle_notification, 2 get_input_idle_notification (v2).
func (n *idleNotifier) getIdleNotification(id *idleNotification, timeoutMS uint32, seat *wl.Seat) error {
	return n.Context().SendRequest(n, 1, id, timeoutMS, seat)
}

func (n *idleNotifier) getInputIdleNotification(id *idleNotification, timeoutMS uint32, seat *wl.Seat) error {
	return n.Context().SendRequest(n, 2, id, timeoutMS, seat)
}

// idleNotification is ext_idle_notification_v1: events 0 idled, 1 resumed.
type idleNotification struct {
	wl.BaseProxy
	owner *waylandIdle
}

func newIdleNotification(ctx *wl.Context, owner *waylandIdle) *idleNotification {
	n := &idleNotification{owner: owner}
	ctx.Register(n)
	return n
}

func (n *idleNotification) Dispatch(ev *wl.Event) {
	switch ev.Opcode {
	case 0:
		n.owner.handleIdled()
	case 1:
		n.owner.handleResumed()
	}
}
