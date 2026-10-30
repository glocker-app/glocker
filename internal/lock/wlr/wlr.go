// Package wlr wraps the wlr-layer-shell-unstable-v1 protocol (the generated
// bindings in layer_shell_protocol.go) for the glocker screen locker. KWin and
// wlroots advertise zwlr_layer_shell_v1; KWin does NOT advertise
// ext_session_lock_v1, so this is glocker's lock path on KDE/Wayland.
package wlr

import (
	"github.com/neurlang/wayland/wl"
	"github.com/neurlang/wayland/xdg"
)

// Type aliases so the generated protocol code (which refers to these bare
// names) binds to the neurlang/wayland types, matching the pattern the xdg and
// ext_session_lock packages use.
type (
	BaseProxy = wl.BaseProxy
	Event     = wl.Event
	Context   = wl.Context
	Proxy     = wl.Proxy
	Surface   = wl.Surface
	Output    = wl.Output
	XdgPopup  = xdg.Popup // only referenced by GetPopup, which glocker doesn't use
)

// BindLayerShell binds the zwlr_layer_shell_v1 global advertised by the registry
// (mirrors ext_session_lock's BindSessionLockManager).
func BindLayerShell(r *wl.Registry, name uint32, version uint32) *ZwlrLayerShellV1 {
	ctx, _ := wl.GetUserData[wl.Context](r)
	shell := NewZwlrLayerShellV1(ctx)
	_ = r.Bind(name, "zwlr_layer_shell_v1", version, shell)
	return shell
}
