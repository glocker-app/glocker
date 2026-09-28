package utils

import (
	"net"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LiveWaylandSocket returns the name (e.g. "wayland-1") of the first wayland-N
// socket in runtimeDir that accepts a connection, or "". Dialing weeds out
// stale sockets left by a compositor that has exited. Used to find the user's
// session from processes that don't inherit its environment (the root daemon,
// or anything started via `sudo -u`).
func LiveWaylandSocket(runtimeDir string) string {
	if runtimeDir == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(runtimeDir, "wayland-*"))
	sort.Strings(matches)
	for _, path := range matches {
		if strings.HasSuffix(path, ".lock") {
			continue
		}
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err != nil {
			continue
		}
		conn.Close()
		return filepath.Base(path)
	}
	return ""
}
