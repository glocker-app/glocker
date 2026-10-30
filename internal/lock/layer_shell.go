// wlr-layer-shell screen locker, the lock path on KDE/KWin. KWin does not
// advertise ext_session_lock_v1 (see wayland.go), so a true session lock is
// impossible there; but KWin does advertise zwlr_layer_shell_v1. This backend
// paints a fullscreen overlay on the Overlay layer with exclusive keyboard
// focus, auto-unlocking on the timer — the same deterrent-overlay model as the
// GNOME backend (not a compositor-enforced lock like ext-session-lock on
// wlroots). It is used only when ext-session-lock is unavailable.
package lock

import (
	"fmt"
	"image"
	"log"
	"time"

	"github.com/neurlang/wayland/wl"
	"github.com/neurlang/wayland/wlclient"

	"glocker/internal/lock/wlr"
)

// layerLocker is the wlr-layer-shell implementation of Backend.
type layerLocker struct {
	cfg Config

	display    *wl.Display
	registry   *wl.Registry
	compositor *wl.Compositor
	shm        *wl.Shm
	layerShell *wlr.ZwlrLayerShellV1
	outputs    []*wl.Output
	surfaces   []*layerSurface

	deadline time.Time
	bgImage  image.Image

	// Written only from the (single) dispatch goroutine via event handlers.
	closed   bool // a layer surface received the compositor's closed event
	protoErr error

	stop chan struct{}
}

func newLayerShell(cfg Config) (*layerLocker, error) {
	return &layerLocker{cfg: cfg, stop: make(chan struct{})}, nil
}

// Name identifies the backend in logs.
func (w *layerLocker) Name() string { return "wlr-layer-shell" }

// Stop ends the lock early; safe to call once from another goroutine.
func (w *layerLocker) Stop() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
}

// Lock paints a fullscreen overlay on every output and blocks until the
// configured duration elapses or Stop is called.
func (w *layerLocker) Lock() error {
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

	// Discover the globals (compositor, shm, outputs, layer shell).
	if err := wlclient.DisplayRoundtrip(display); err != nil {
		return w.fail("wayland registry roundtrip", err)
	}
	if w.compositor == nil || w.shm == nil {
		return fmt.Errorf("wayland compositor/shm not available")
	}
	if w.layerShell == nil {
		return fmt.Errorf("compositor does not support zwlr_layer_shell_v1")
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

	// Deadline set before the first frame so the countdown is already correct.
	w.deadline = time.Now().Add(w.cfg.Duration)
	for _, out := range w.outputs {
		if err := w.addSurface(out); err != nil {
			return w.fail("create layer surface", err)
		}
	}

	// Single-threaded event + timer loop, matching the ext-session-lock backend:
	// each roundtrip flushes requests and processes configure/closed/release
	// events; between them we repaint any changed countdown and check the timer.
	for {
		if err := wlclient.DisplayRoundtrip(display); err != nil {
			return w.fail("wayland roundtrip", err)
		}
		for _, s := range w.surfaces {
			s.tick()
		}
		if w.closed {
			return fmt.Errorf("layer surface closed by the compositor")
		}
		select {
		case <-w.stop:
			return w.teardown()
		default:
		}
		if !time.Now().Before(w.deadline) {
			return w.teardown()
		}
		time.Sleep(pollInterval)
	}
}

// teardown destroys the overlay surfaces and flushes to the compositor.
func (w *layerLocker) teardown() error {
	for _, s := range w.surfaces {
		s.layerSurface.Destroy()
		s.wlSurface.Destroy()
	}
	return wlclient.DisplayRoundtrip(w.display)
}

// fail prefers the compositor's protocol error (the real cause) over the
// transport error it leads to.
func (w *layerLocker) fail(step string, err error) error {
	if w.protoErr != nil {
		return fmt.Errorf("%s: %w", step, w.protoErr)
	}
	return fmt.Errorf("%s: %w", step, err)
}

// addSurface creates one fullscreen overlay surface for an output.
func (w *layerLocker) addSurface(out *wl.Output) error {
	surface, err := w.compositor.CreateSurface()
	if err != nil {
		return err
	}
	ls, err := w.layerShell.GetLayerSurface(surface, out, wlr.ZwlrLayerShellV1LayerOverlay, "glocker-lock")
	if err != nil {
		return err
	}
	// Anchor to all four edges so the compositor sizes it to the whole output;
	// exclusive zone -1 makes it cover panels too; exclusive keyboard grabs input.
	const anchorAll = wlr.ZwlrLayerSurfaceV1AnchorTop | wlr.ZwlrLayerSurfaceV1AnchorBottom |
		wlr.ZwlrLayerSurfaceV1AnchorLeft | wlr.ZwlrLayerSurfaceV1AnchorRight
	ls.SetAnchor(anchorAll)
	ls.SetExclusiveZone(-1)
	ls.SetKeyboardInteractivity(wlr.ZwlrLayerSurfaceV1KeyboardInteractivityExclusive)

	s := &layerSurface{parent: w, wlSurface: surface, layerSurface: ls}
	ls.AddConfigureHandler(s)
	ls.AddClosedHandler(s)
	// Commit with no buffer so the compositor replies with a configure carrying
	// the size to paint.
	if err := surface.Commit(); err != nil {
		return err
	}
	w.surfaces = append(w.surfaces, s)
	return nil
}

// ── Registry globals ────────────────────────────────────────────────────────

func (w *layerLocker) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case "wl_compositor":
		w.compositor = wlclient.RegistryBindCompositorInterface(w.registry, ev.Name, 4)
	case "wl_shm":
		w.shm = wlclient.RegistryBindShmInterface(w.registry, ev.Name, 1)
	case "wl_output":
		w.outputs = append(w.outputs, wlclient.RegistryBindOutputInterface(w.registry, ev.Name, 3))
	case "zwlr_layer_shell_v1":
		// Bind v1: glocker uses only v1 requests (anchor, exclusive zone,
		// exclusive keyboard, ack). Binding low avoids newer events we'd ignore.
		w.layerShell = wlr.BindLayerShell(w.registry, ev.Name, 1)
	}
}

// HandleDisplayError records the compositor's fatal protocol error.
func (w *layerLocker) HandleDisplayError(ev wl.DisplayErrorEvent) {
	if w.protoErr == nil {
		w.protoErr = fmt.Errorf("compositor protocol error (code %d): %s", ev.Code, ev.Message)
	}
}

// ── Per-output overlay surface ──────────────────────────────────────────────

type layerSurface struct {
	parent       *layerLocker
	wlSurface    *wl.Surface
	layerSurface *wlr.ZwlrLayerSurfaceV1
	width        int
	height       int

	renderer *frameRenderer
	bufs     [2]*shmBuffer // double-buffered
	shown    string        // countdown text currently on screen
}

// HandleZwlrLayerSurfaceV1Configure sizes and paints the surface. The compositor
// dictates the dimensions; we ack and attach a matching buffer.
func (s *layerSurface) HandleZwlrLayerSurfaceV1Configure(ev wlr.ZwlrLayerSurfaceV1ConfigureEvent) {
	s.layerSurface.AckConfigure(ev.Serial)
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

// HandleZwlrLayerSurfaceV1Closed marks the lock ended (compositor denied/closed it).
func (s *layerSurface) HandleZwlrLayerSurfaceV1Closed(wlr.ZwlrLayerSurfaceV1ClosedEvent) {
	s.parent.closed = true
}

// tick repaints when the displayed countdown has changed (once a second).
func (s *layerSurface) tick() {
	if s.width <= 0 || s.height <= 0 {
		return // not configured yet
	}
	if formatRemaining(time.Until(s.parent.deadline)) == s.shown {
		return
	}
	if err := s.paint(); err != nil {
		log.Printf("glocklock: layer-shell paint failed: %v", err)
	}
}

// paint renders the current frame into a free buffer and commits it. If both
// buffers are still held by the compositor it skips; the next tick retries.
func (s *layerSurface) paint() error {
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
			nb, err := newSHMBuffer(s.parent.shm, s.width, s.height)
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
