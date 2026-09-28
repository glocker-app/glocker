package lock

import (
	"net"

	"glocker/internal/utils"
	"os"
	"path/filepath"
	"testing"
)

func listen(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
}

func TestLiveWaylandSocketSkipsStaleAndLock(t *testing.T) {
	dir := t.TempDir()
	// wayland-0: stale socket file (listener closed, file left behind).
	stale, err := net.Listen("unix", filepath.Join(dir, "wayland-0"))
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	os.WriteFile(filepath.Join(dir, "wayland-1.lock"), nil, 0o600)
	listen(t, filepath.Join(dir, "wayland-1"))

	if got := utils.LiveWaylandSocket(dir); got != "wayland-1" {
		t.Fatalf("liveWaylandSocket = %q, want wayland-1", got)
	}
	if got := utils.LiveWaylandSocket(t.TempDir()); got != "" {
		t.Fatalf("empty dir: got %q", got)
	}
}

func TestAdoptSessionEnvPrefersWaylandOverDisplay(t *testing.T) {
	dir := t.TempDir()
	listen(t, filepath.Join(dir, "wayland-3"))
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0") // the old violation command forced this

	AdoptSessionEnv()
	if got := os.Getenv("WAYLAND_DISPLAY"); got != "wayland-3" {
		t.Fatalf("WAYLAND_DISPLAY = %q, want wayland-3", got)
	}
	if DetectSession() != SessionWayland {
		t.Fatal("expected wayland session")
	}
}

func TestAdoptSessionEnvKeepsExplicitWayland(t *testing.T) {
	dir := t.TempDir()
	listen(t, filepath.Join(dir, "wayland-3"))
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-9")

	AdoptSessionEnv()
	if got := os.Getenv("WAYLAND_DISPLAY"); got != "wayland-9" {
		t.Fatalf("WAYLAND_DISPLAY overridden to %q", got)
	}
}

func TestLocalXDisplay(t *testing.T) {
	dir := t.TempDir()
	if got := localXDisplay(dir); got != "" {
		t.Fatalf("empty: %q", got)
	}
	os.WriteFile(filepath.Join(dir, "X1"), nil, 0o600)
	if got := localXDisplay(dir); got != ":1" {
		t.Fatalf("got %q, want :1", got)
	}
}
