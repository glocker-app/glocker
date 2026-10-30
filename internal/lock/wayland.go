// Wayland backend for the screen locker, built on the ext-session-lock-v1
// protocol. The compositor guarantees the lock surface stays on top and that
// input is confined to it — so, unlike X11, there is no manual keyboard grab to
// defeat. Each output gets a full-screen surface showing the message and a live
// countdown (rendered in software, see render.go) over the background colour or
// image, and the lock ends automatically when the timer expires.
package lock

import (
	"fmt"
	"image"
	"log"
	"sync"
	"time"

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

	deadline time.Time
	bgImage  image.Image // optional; nil = solid BackgroundColor

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

	if w.cfg.BackgroundImage != "" {
		if img, err := loadBackgroundImage(w.cfg.BackgroundImage); err == nil {
			w.bgImage = img
		} else {
			log.Printf("glocklock: background image: %v (using solid colour)", err)
		}
	}

	// Request the lock and create a surface per output. The deadline is set first
	// so the first frame (painted on configure) already shows the countdown.
	w.deadline = time.Now().Add(w.cfg.Duration)
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
	// and processes incoming events (configure/locked/finished/buffer release);
	// between them we repaint any countdown that changed and check the deadline
	// and the stop signal.
	for {
		if err := wlclient.DisplayRoundtrip(display); err != nil {
			return w.fail("wayland roundtrip", err)
		}
		for _, s := range w.surfaces {
			s.tick()
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
		if !time.Now().Before(w.deadline) {
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
	width       int
	height      int

	renderer *frameRenderer
	bufs     [2]*shmBuffer // double-buffered: draw into one while the other is shown
	shown    string        // countdown text currently on screen
}

// HandleSessionLockSurfaceConfigure sizes and paints the surface. The compositor
// dictates the dimensions; we must ack and attach a matching buffer.
func (s *waylandSurface) HandleSessionLockSurfaceConfigure(ev ext.SessionLockSurfaceConfigureEvent) {
	s.lockSurface.AckConfigure(ev.Serial)
	if int(ev.Width) != s.width || int(ev.Height) != s.height {
		s.width, s.height = int(ev.Width), int(ev.Height)
		s.renderer = nil
		for i, b := range s.bufs {
			if b != nil {
				b.destroy()
				s.bufs[i] = nil
			}
		}
	}
	s.shown = "" // a configure must be answered with a fresh commit
	s.tick()
}

// tick repaints when the displayed countdown has changed (once a second).
func (s *waylandSurface) tick() {
	if s.width <= 0 || s.height <= 0 {
		return // not configured yet
	}
	if formatRemaining(time.Until(s.parent.deadline)) == s.shown {
		return
	}
	if err := s.paint(); err != nil {
		log.Printf("glocklock: wayland paint failed: %v", err)
	}
}

// paint renders the current frame into a free buffer and commits it. If both
// buffers are still held by the compositor it skips; the next tick retries.
func (s *waylandSurface) paint() error {
	if s.renderer == nil {
		r, err := newFrameRenderer(s.width, s.height, s.parent.cfg.BackgroundColor, s.parent.bgImage)
		if err != nil {
			return err
		}
		s.renderer = r
	}
	var b *shmBuffer
	for i := range s.bufs {
		if s.bufs[i] == nil {
			nb, err := s.newBuffer()
			if err != nil {
				return err
			}
			s.bufs[i] = nb
		}
		if !s.bufs[i].busy {
			b = s.bufs[i]
			break
		}
	}
	if b == nil {
		return nil
	}

	remaining := time.Until(s.parent.deadline)
	s.renderer.renderXRGB(b.data, s.width*4, s.parent.cfg.Message, remaining)
	if err := s.wlSurface.Attach(b.buf, 0, 0); err != nil {
		return err
	}
	if err := s.wlSurface.DamageBuffer(0, 0, int32(s.width), int32(s.height)); err != nil {
		return err
	}
	if err := s.wlSurface.Commit(); err != nil {
		return err
	}
	b.busy = true
	s.shown = formatRemaining(remaining)
	return nil
}

// newBuffer allocates one XRGB8888 shm buffer the size of the surface.
func (s *waylandSurface) newBuffer() (*shmBuffer, error) {
	return newSHMBuffer(s.parent.shm, s.width, s.height)
}
