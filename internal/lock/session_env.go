package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"glocker/internal/utils"
)

// AdoptSessionEnv fills in the display environment when glocklock is started
// outside the desktop session — typically by the root daemon's violation
// command via `sudo -u <user> glocklock`, which strips WAYLAND_DISPLAY and
// XDG_RUNTIME_DIR. Without them DetectSession would fall through to X11 (or to
// nothing), and an X11 lock under XWayland doesn't cover native Wayland
// surfaces, so it's bypassable.
//
// It only fills gaps, never overrides: XDG_RUNTIME_DIR defaults to
// /run/user/<uid>; WAYLAND_DISPLAY is set to the first live wayland-N socket
// there (even if DISPLAY is set, since Wayland must win when both exist); and
// DISPLAY falls back to a local X server only when there's no Wayland
// session at all.
func AdoptSessionEnv() {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		dir := fmt.Sprintf("/run/user/%d", os.Getuid())
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			runtimeDir = dir
			os.Setenv("XDG_RUNTIME_DIR", dir)
		}
	}

	if os.Getenv("WAYLAND_DISPLAY") == "" && runtimeDir != "" {
		if name := utils.LiveWaylandSocket(runtimeDir); name != "" {
			os.Setenv("WAYLAND_DISPLAY", name)
			return
		}
	}

	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		if d := localXDisplay("/tmp/.X11-unix"); d != "" {
			os.Setenv("DISPLAY", d)
		}
	}
}

// localXDisplay returns ":N" for the first X server socket in dir (by name).
func localXDisplay(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "X[0-9]*"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return ":" + strings.TrimPrefix(filepath.Base(matches[0]), "X")
}
