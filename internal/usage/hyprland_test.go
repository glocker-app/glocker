package usage

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHyprland serves canned replies on <runtimeDir>/hypr/<sig>/.socket.sock,
// one command per connection like Hyprland's IPC.
func fakeHyprland(t *testing.T, runtimeDir string, replies map[string]string) {
	t.Helper()
	dir := filepath.Join(runtimeDir, "hypr", "sig")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "hyprland.lock"), []byte("123\nwayland-nope\n"), 0o600)
	l, err := net.Listen("unix", filepath.Join(dir, ".socket.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 256)
			n, _ := c.Read(buf)
			c.Write([]byte(replies[string(buf[:n])]))
			c.Close()
		}
	}()
}

func TestFindHyprland(t *testing.T) {
	if _, ok := findHyprland(t.TempDir()); ok {
		t.Fatal("found Hyprland in an empty dir")
	}
	// A leftover instance dir with no live socket is skipped.
	rd := t.TempDir()
	os.MkdirAll(filepath.Join(rd, "hypr", "stale"), 0o700)
	os.WriteFile(filepath.Join(rd, "hypr", "stale", ".socket.sock"), nil, 0o600)
	if _, ok := findHyprland(rd); ok {
		t.Fatal("treated a stale instance as live")
	}
	fakeHyprland(t, rd, nil)
	inst, ok := findHyprland(rd)
	if !ok || inst.waylandSock != "wayland-nope" || !strings.HasSuffix(inst.ipcSock, "sig/.socket.sock") {
		t.Fatalf("findHyprland = %+v, %v", inst, ok)
	}
}

func TestHyprlandCapture(t *testing.T) {
	rd := t.TempDir()
	fakeHyprland(t, rd, map[string]string{
		"j/clients": `[
			{"address":"0x1","mapped":true,"class":"firefox","title":"News"},
			{"address":"0x2","mapped":true,"class":"","initialClass":"foot","title":"~"},
			{"address":"0x3","mapped":false,"class":"ghost","title":"hidden"}]`,
		"j/activewindow": `{"address":"0x2"}`,
	})
	inst, _ := findHyprland(rd)
	src := newHyprlandSource(rd, inst) // wayland socket doesn't exist → no idle
	defer src.Close()

	s, err := src.Capture()
	if err != nil {
		t.Fatal(err)
	}
	if s.IdleMS != -1 {
		t.Errorf("IdleMS = %d, want -1 without the idle protocol", s.IdleMS)
	}
	if len(s.Windows) != 2 {
		t.Fatalf("windows = %+v, want the 2 mapped ones", s.Windows)
	}
	if a := s.Active(); a == nil || a.Class != "foot" {
		t.Errorf("active = %+v, want foot (initialClass fallback)", a)
	}
}

func TestHyprlandNoFocus(t *testing.T) {
	ws := hyprWindows([]hyprClient{{Address: "0x1", Mapped: true, Class: "a"}}, "")
	if len(ws) != 1 || ws[0].Active {
		t.Fatalf("windows = %+v, want one inactive", ws)
	}
}

type stubSource struct {
	err    error
	closed bool
}

func (s *stubSource) Capture() (Sample, error) { return Sample{IdleMS: 1}, s.err }
func (s *stubSource) Close() error             { s.closed = true; return nil }

func TestAutoSourceRetriesAndReselects(t *testing.T) {
	var logs []string
	a := NewAutoSource(Options{}, func(f string, args ...any) { logs = append(logs, f) })

	// No session yet: error, logged once across repeated attempts.
	a.newSource = func(Options) (Source, string, error) { return nil, "", ErrUnsupportedSession }
	a.Capture()
	a.Capture()
	if len(logs) != 1 {
		t.Fatalf("logs = %v, want the no-backend message once", logs)
	}

	// Session appears: selected and sampled.
	first := &stubSource{}
	a.newSource = func(Options) (Source, string, error) { return first, "stub", nil }
	if s, err := a.Capture(); err != nil || s.IdleMS != 1 {
		t.Fatalf("Capture = %+v, %v", s, err)
	}

	// Backend breaks (logout/compositor restart): closed, then replaced.
	first.err = errors.New("gone")
	if _, err := a.Capture(); err == nil || !first.closed {
		t.Fatalf("broken backend: err=%v closed=%v", err, first.closed)
	}
	second := &stubSource{}
	a.newSource = func(Options) (Source, string, error) { return second, "stub2", nil }
	if _, err := a.Capture(); err != nil {
		t.Fatalf("re-selected Capture: %v", err)
	}
}
