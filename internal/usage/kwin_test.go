package usage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// kwinBridge decodes good JSON, rejects bad JSON without clobbering state, and
// snapshot returns a copy.
func TestKWinBridge(t *testing.T) {
	b := &kwinBridge{}
	if err := b.Push(`[{"class":"konsole","instance":"konsole","title":"Shell","active":true}]`); err != nil {
		t.Fatalf("push: %v", err)
	}
	snap := b.snapshot()
	if len(snap) != 1 || snap[0].Class != "konsole" || !snap[0].Active {
		t.Fatalf("snapshot = %+v", snap)
	}
	// Bad JSON → error, state unchanged.
	if err := b.Push("not json"); err == nil {
		t.Error("expected an error on malformed JSON")
	}
	if len(b.snapshot()) != 1 {
		t.Error("bad push clobbered the last good state")
	}
	// snapshot is a copy, not the backing slice.
	snap[0].Class = "mutated"
	if b.snapshot()[0].Class != "konsole" {
		t.Error("snapshot aliased the internal slice")
	}

	// Capture without an idle watcher reports IdleMS -1 and the last windows.
	s, err := (&KWinSource{bridge: b}).Capture()
	if err != nil || s.IdleMS != -1 || len(s.Windows) != 1 {
		t.Fatalf("capture = %+v err=%v", s, err)
	}
}

// End to end over a private session bus: NewKWinSource claims the name and
// exports Push; a second connection (standing in for the KWin script) calls it,
// and Capture reflects the push. Exercises the real D-Bus wiring.
func TestKWinSourceRoundTrip(t *testing.T) {
	addr := privateBus(t)

	daemonConn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("daemon connect: %v", err)
	}
	src, err := NewKWinSource(daemonConn, "", "") // no wayland socket → IdleMS -1
	if err != nil {
		t.Fatalf("NewKWinSource: %v", err)
	}
	defer src.Close()

	script, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("script connect: %v", err)
	}
	defer script.Close()

	raw := `[{"class":"org.kde.konsole","instance":"konsole","title":"Shell","active":true},` +
		`{"class":"org.mozilla.firefox","instance":"Navigator","title":"News","active":false}]`
	if call := script.Object(kwinBridgeDest, kwinBridgePath).Call(kwinBridgeIface+".Push", 0, raw); call.Err != nil {
		t.Fatalf("script Push: %v", call.Err)
	}

	s, err := src.Capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(s.Windows) != 2 || s.Windows[0].Class != "org.kde.konsole" {
		t.Fatalf("windows = %+v", s.Windows)
	}
	if a := s.Active(); a == nil || a.Class != "org.kde.konsole" {
		t.Errorf("active = %+v", a)
	}

	// A second source on the same bus can't steal the name (DoNotQueue).
	other, _ := dbus.Connect(addr)
	if _, err := NewKWinSource(other, "", ""); err == nil {
		t.Error("expected name-already-owned error for a second source")
	}
}

// privateBus starts a throwaway session dbus-daemon and returns its address,
// skipping the test if dbus-daemon isn't installed.
func privateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not available")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "bus")
	addr := "unix:path=" + sock
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--address="+addr)
	if err := cmd.Start(); err != nil {
		t.Skipf("can't start dbus-daemon: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for range 100 {
		if _, err := os.Stat(sock); err == nil {
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("dbus-daemon socket never appeared")
	return ""
}
