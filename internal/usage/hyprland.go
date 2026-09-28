package usage

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HyprlandSource reads window state on Hyprland. The window list and focus come
// from Hyprland's IPC socket ($XDG_RUNTIME_DIR/hypr/<instance>/.socket.sock,
// the same one hyprctl uses); idle time comes from the ext-idle-notify-v1
// Wayland protocol (see idle_wayland.go), since the IPC doesn't expose it.
//
// Both sockets are addressed by path under the user's runtime dir, so this works
// from the root daemon, which has none of the session's environment.
type HyprlandSource struct {
	ipcSock string
	idle    *waylandIdle // nil if the idle protocol is unavailable → IdleMS -1
}

// hyprInstance is a live Hyprland instance found under a runtime dir.
type hyprInstance struct {
	ipcSock     string // .../hypr/<sig>/.socket.sock
	waylandSock string // e.g. "wayland-1" (from hyprland.lock), may be ""
}

// findHyprland returns the most recently started Hyprland instance under
// runtimeDir whose IPC socket accepts connections, or false. Stale instance
// directories (a crashed or logged-out session) are skipped by dialing.
func findHyprland(runtimeDir string) (hyprInstance, bool) {
	if runtimeDir == "" {
		return hyprInstance{}, false
	}
	dirs, _ := filepath.Glob(filepath.Join(runtimeDir, "hypr", "*"))
	// Newest first, so a fresh login wins over a leftover directory.
	sort.Slice(dirs, func(i, j int) bool { return modTime(dirs[i]).After(modTime(dirs[j])) })
	for _, dir := range dirs {
		sock := filepath.Join(dir, ".socket.sock")
		conn, err := net.DialTimeout("unix", sock, time.Second)
		if err != nil {
			continue
		}
		conn.Close()
		inst := hyprInstance{ipcSock: sock}
		// hyprland.lock is "<pid>\n<wayland socket name>\n".
		if b, err := os.ReadFile(filepath.Join(dir, "hyprland.lock")); err == nil {
			if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) >= 2 {
				inst.waylandSock = strings.TrimSpace(lines[1])
			}
		}
		return inst, true
	}
	return hyprInstance{}, false
}

func modTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// NewHyprlandSource builds a source for a Hyprland instance. Idle tracking is
// best-effort: if the Wayland connection or the idle protocol isn't available,
// samples still carry windows, with IdleMS -1.
func newHyprlandSource(runtimeDir string, inst hyprInstance) *HyprlandSource {
	s := &HyprlandSource{ipcSock: inst.ipcSock}
	if inst.waylandSock != "" {
		if idle, err := newWaylandIdle(runtimeDir, inst.waylandSock); err == nil {
			s.idle = idle
		}
	}
	return s
}

// Close releases the idle watcher's Wayland connection.
func (s *HyprlandSource) Close() error {
	if s.idle != nil {
		s.idle.Close()
	}
	return nil
}

// hyprClient is the subset of `hyprctl -j clients` / `activewindow` we use.
type hyprClient struct {
	Address      string `json:"address"`
	Mapped       bool   `json:"mapped"`
	Class        string `json:"class"`
	InitialClass string `json:"initialClass"`
	Title        string `json:"title"`
}

func (s *HyprlandSource) Capture() (Sample, error) {
	raw, err := s.request("j/clients")
	if err != nil {
		return Sample{}, err
	}
	var clients []hyprClient
	if err := json.Unmarshal(raw, &clients); err != nil {
		return Sample{}, fmt.Errorf("hyprland: bad clients JSON: %w", err)
	}
	// activewindow is "{}" when nothing is focused (e.g. an empty workspace).
	var active hyprClient
	if raw, err := s.request("j/activewindow"); err == nil {
		_ = json.Unmarshal(raw, &active)
	}

	sample := Sample{Timestamp: time.Now(), IdleMS: -1, Windows: hyprWindows(clients, active.Address)}
	if s.idle != nil {
		sample.IdleMS = s.idle.IdleMS()
	}
	return sample, nil
}

// hyprWindows maps Hyprland clients onto Windows, marking activeAddr focused.
// Unmapped clients (not shown anywhere) are skipped.
func hyprWindows(clients []hyprClient, activeAddr string) []Window {
	var out []Window
	for _, c := range clients {
		if !c.Mapped {
			continue
		}
		class := c.Class
		if class == "" {
			class = c.InitialClass
		}
		out = append(out, Window{
			Active: activeAddr != "" && c.Address == activeAddr,
			Class:  class,
			Title:  c.Title,
		})
	}
	return out
}

// request sends one hyprctl-style command and returns the full reply.
func (s *HyprlandSource) request(cmd string) ([]byte, error) {
	conn, err := net.DialTimeout("unix", s.ipcSock, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("hyprland: IPC unavailable: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil, fmt.Errorf("hyprland: IPC write: %w", err)
	}
	b, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("hyprland: IPC read: %w", err)
	}
	return b, nil
}
