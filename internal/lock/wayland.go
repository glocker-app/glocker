// Wayland backend for the screen locker, built on the ext-session-lock-v1
// protocol. The compositor guarantees the lock surface stays on top and that
// input is confined to it — so, unlike X11, there is no manual keyboard grab to
// defeat. This backend renders a solid full-screen surface per output and
// unlocks automatically when the timer expires.
//
// Not yet rendered on Wayland: the countdown/message text and background image
// the X11 backend draws. That needs glyph rendering into the shm buffer; the
// Config carries the fields so it can be added here later without touching
// callers.
package lock

import (
	"fmt"
	"log"
	"sync"
	"time"

	wlos "github.com/neurlang/wayland/os"
	"github.com/neurlang/wayland/wl"
	"github.com/neurlang/wayland/wlclient"
	ext "github.com/tuxx/wayland-ext-session-lock-go"
)

// pollInterval bounds how often the lock loop wakes to check the timer between
// Wayland event roundtrips.
const pollInterval = 50 * time.Millisecond

// waylandLocker is the Wayland (ext-session-lock-v1) implementation of Backend.
type waylandLocker struct {
	cfg Config

	display     *wl.Display
	registry    *wl.Registry
	compositor  *wl.Compositor
	shm         *wl.Shm
	lockManager *ext.SessionLockManager
	lock        *ext.SessionLock
	outputs     []*wl.Output
	surfaces    []*waylandSurface

	// Written only from the (single) dispatch goroutine via event handlers.
	locked   bool
	finished bool
	// protoErr is the compositor's fatal wl_display.error, if one arrived. The
	// compositor disconnects right after sending it, so the call that surfaces
	// the failure usually just sees a broken pipe; this carries the real cause.
	protoErr error

	stopOnce sync.Once
	stop     chan struct{}
}

func newWayland(cfg Config) (*waylandLocker, error) {
	return &waylandLocker{cfg: cfg, stop: make(chan struct{})}, nil
}

// Name identifies the backend in logs.
func (w *waylandLocker) Name() string { return "wayland" }

// Stop ends the lock early; safe to call from another goroutine.
func (w *waylandLocker) Stop() { w.stopOnce.Do(func() { close(w.stop) }) }

// Lock requests a session lock, paints each output, and blocks until the
// configured duration elapses or Stop is called.
func (w *waylandLocker) Lock() error {
	display, err := wlclient.DisplayConnect(nil)
	if err != nil {
		return fmt.Errorf("connect to wayland: %w", err)
	}
	w.display = display
	defer wlclient.DisplayDisconnect(display)
	display.AddErrorHandler(w)

	registry, err := display.GetRegistry()
	if err != nil {
		return w.fail("get wayland registry", err)
	}
	w.registry = registry
	registry.AddGlobalHandler(w)
	registry.AddGlobalRemoveHandler(w)

	// Discover the globals (compositor, shm, outputs, lock manager).
	if err := wlclient.DisplayRoundtrip(display); err != nil {
		return w.fail("wayland registry roundtrip", err)
	}
	if w.compositor == nil || w.shm == nil {
		return fmt.Errorf("wayland compositor/shm not available")
	}
	if w.lockManager == nil {
		return fmt.Errorf("compositor does not support ext_session_lock_v1")
	}
	if len(w.outputs) == 0 {
		return fmt.Errorf("no wayland outputs to lock")
	}

	// Request the lock and create a surface per output.
	lk, err := w.lockManager.Lock()
	if err != nil {
		return w.fail("request session lock", err)
	}
	w.lock = lk
	ext.SessionLockAddListener(lk, w)
	for _, out := range w.outputs {
		if err := w.addSurface(out); err != nil {
			return w.fail("create lock surface", err)
		}
	}

	// Single-threaded event + timer loop: each roundtrip flushes our requests
	// and processes incoming events (configure/locked/finished); between them we
	// check the deadline and the stop signal.
	deadline := time.Now().Add(w.cfg.Duration)
	for {
		if err := wlclient.DisplayRoundtrip(display); err != nil {
			return w.fail("wayland roundtrip", err)
		}
		if w.finished {
			// Compositor ended the lock itself (e.g. denied it).
			w.lock.Destroy()
			_ = wlclient.DisplayRoundtrip(display)
			return fmt.Errorf("session lock ended by the compositor")
		}
		select {
		case <-w.stop:
			return w.unlock()
		default:
		}
		if !time.Now().Before(deadline) {
			return w.unlock()
		}
		time.Sleep(pollInterval)
	}
}

// fail wraps err for the step that hit it, preferring the compositor's protocol
// error (the actual cause) over the transport error it leads to.
func (w *waylandLocker) fail(step string, err error) error {
	if w.protoErr != nil {
		return fmt.Errorf("%s: %w", step, w.protoErr)
	}
	return fmt.Errorf("%s: %w", step, err)
}

// unlock tears the lock down and flushes the request to the compositor.
func (w *waylandLocker) unlock() error {
	if w.lock == nil {
		return nil
	}
	if w.locked {
		w.lock.UnlockAndDestroy()
	} else {
		w.lock.Destroy()
	}
	return wlclient.DisplayRoundtrip(w.display)
}

// addSurface creates a lock surface for one output and wires its listener.
func (w *waylandLocker) addSurface(out *wl.Output) error {
	surface, err := w.compositor.CreateSurface()
	if err != nil {
		return err
	}
	lockSurface, err := w.lock.GetLockSurface(surface, out)
	if err != nil {
		return err
	}
	s := &waylandSurface{parent: w, wlSurface: surface, lockSurface: lockSurface}
	ext.SessionLockSurfaceAddListener(lockSurface, s)
	w.surfaces = append(w.surfaces, s)
	return nil
}

// ── Registry globals ────────────────────────────────────────────────────────

func (w *waylandLocker) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case "wl_compositor":
		w.compositor = wlclient.RegistryBindCompositorInterface(w.registry, ev.Name, 4)
	case "wl_shm":
		w.shm = wlclient.RegistryBindShmInterface(w.registry, ev.Name, 1)
	case "wl_output":
		w.outputs = append(w.outputs, wlclient.RegistryBindOutputInterface(w.registry, ev.Name, 3))
	case "ext_session_lock_manager_v1":
		w.lockManager = ext.BindSessionLockManager(w.registry, ev.Name, 1)
	}
}

func (w *waylandLocker) HandleRegistryGlobalRemove(wl.RegistryGlobalRemoveEvent) {}

// HandleDisplayError records the compositor's fatal protocol error.
func (w *waylandLocker) HandleDisplayError(ev wl.DisplayErrorEvent) {
	if w.protoErr == nil {
		w.protoErr = fmt.Errorf("compositor protocol error (code %d): %s", ev.Code, ev.Message)
	}
}

// ── Session lock lifecycle ──────────────────────────────────────────────────

func (w *waylandLocker) HandleSessionLockLocked(ext.SessionLockLockedEvent) { w.locked = true }

func (w *waylandLocker) HandleSessionLockFinished(ext.SessionLockFinishedEvent) { w.finished = true }

// ── Per-output lock surface ─────────────────────────────────────────────────

type waylandSurface struct {
	parent      *waylandLocker
	wlSurface   *wl.Surface
	lockSurface *ext.SessionLockSurface
	width       uint32
	height      uint32

	buf  *wl.Buffer // kept alive for the lock's lifetime
	data []byte     // mmap backing the buffer; kept mapped until exit
}

// HandleSessionLockSurfaceConfigure sizes and paints the surface. The compositor
// dictates the dimensions; we must ack and attach a matching buffer.
func (s *waylandSurface) HandleSessionLockSurfaceConfigure(ev ext.SessionLockSurfaceConfigureEvent) {
	s.width, s.height = ev.Width, ev.Height
	s.lockSurface.AckConfigure(ev.Serial)
	if err := s.paint(); err != nil {
		log.Printf("glocklock: wayland paint failed: %v", err)
	}
}

// paint fills the surface with the configured solid background colour.
func (s *waylandSurface) paint() error {
	width, height := int(s.width), int(s.height)
	if width <= 0 || height <= 0 {
		return nil
	}
	stride := width * 4
	size := stride * height

	fd, err := wlos.CreateAnonymousFile(int64(size))
	if err != nil {
		return err
	}
	defer fd.Close()
	data, err := wlos.Mmap(int(fd.Fd()), 0, size, wlos.ProtRead|wlos.ProtWrite, wlos.MapShared)
	if err != nil {
		return err
	}

	// XRGB8888 is little-endian, so bytes are laid out B, G, R, X.
	col := s.parent.cfg.BackgroundColor
	r, g, b := byte(col>>16), byte(col>>8), byte(col)
	for i := 0; i+3 < size; i += 4 {
		data[i], data[i+1], data[i+2], data[i+3] = b, g, r, 0xff
	}

	pool, err := s.parent.shm.CreatePool(fd.Fd(), int32(size))
	if err != nil {
		return err
	}
	buf, err := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), wl.ShmFormatXrgb8888)
	pool.Destroy()
	if err != nil {
		return err
	}
	s.buf, s.data = buf, data

	if err := s.wlSurface.Attach(buf, 0, 0); err != nil {
		return err
	}
	if err := s.wlSurface.DamageBuffer(0, 0, int32(width), int32(height)); err != nil {
		return err
	}
	return s.wlSurface.Commit()
}
