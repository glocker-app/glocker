package ipc

import (
	"strings"
	"testing"

	"glocker/internal/config"
)

func TestSocketPath(t *testing.T) {
	// The socket path is a single source of truth in config; ipc must track it.
	if SocketPath != config.GlockerSock {
		t.Errorf("SocketPath = %s, want config.GlockerSock = %s", SocketPath, config.GlockerSock)
	}
	// It must NOT live in world-writable /tmp — the dedicated root-owned dir is
	// the access control, so a stray move back to /tmp should fail the build's tests.
	if strings.HasPrefix(SocketPath, "/tmp/") {
		t.Errorf("SocketPath %s is under world-writable /tmp; use a root-owned dir", SocketPath)
	}
}

// Note: Full socket testing requires running server, which is tested during integration testing.
