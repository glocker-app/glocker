package ipc

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

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

// The uninstall handshake must deliver COMPLETED on the same connection AFTER the
// initial OK — one command per connection means HandleConnection closes the socket
// on return, so if the work runs in a background goroutine the client sees EOF
// instead of COMPLETED (and skips its systemd teardown). This guards that.
func TestUninstallDeliversCompletionBeforeClose(t *testing.T) {
	origRestore, origExit := uninstallRestore, uninstallExit
	uninstallRestore = func(*config.Config) error { return nil } // don't touch the real system
	exited := make(chan int, 1)
	uninstallExit = func(code int) { exited <- code } // observe exit, don't kill the test
	origPeer := peerUIDFn
	peerUIDFn = func(net.Conn) (uint32, error) { return 0, nil } // net.Pipe has no SO_PEERCRED; act as root
	t.Cleanup(func() { uninstallRestore, uninstallExit, peerUIDFn = origRestore, origExit, origPeer })

	cfg := &config.Config{}
	cfg.Lifecycle.Reasons = []string{"work"} // make "work" a valid uninstall reason

	client, server := net.Pipe()
	go HandleConnection(cfg, server)
	go func() { fmt.Fprint(client, "uninstall:work\n") }()

	r := bufio.NewReader(client)
	ack, err := r.ReadString('\n')
	if err != nil || !strings.Contains(ack, "OK: Uninstall request received") {
		t.Fatalf("ack = %q, err = %v", ack, err)
	}
	done, err := r.ReadString('\n')
	if err != nil { // the regression: EOF here because the socket closed first
		t.Fatalf("reading completion failed: %v (client would have aborted on EOF)", err)
	}
	if !strings.Contains(done, "COMPLETED") {
		t.Fatalf("completion = %q, want COMPLETED", done)
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("uninstall exit code = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("uninstall never reached exit")
	}
}

// peerUID over a real Unix socket returns the connecting process's uid (here, the
// test process itself) — the basis for gating privileged commands.
func TestPeerUID_Self(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/peer.sock"
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	srv := <-accepted
	defer srv.Close()

	uid, err := peerUID(srv)
	if err != nil {
		t.Fatalf("peerUID: %v", err)
	}
	if uid != uint32(os.Getuid()) {
		t.Errorf("peerUID = %d, want %d (this process)", uid, os.Getuid())
	}
}

// A privileged command from a non-root peer is refused before it runs.
func TestPrivilegedCommandRejectedFromNonRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — the peer uid would be 0 and the command would execute")
	}
	dir := t.TempDir()
	sock := dir + "/gate.sock"
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		if c, err := l.Accept(); err == nil {
			HandleConnection(&config.Config{}, c)
		}
	}()
	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = client.Write([]byte("uninstall:testing\n"))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	n, _ := client.Read(buf)
	if !strings.Contains(string(buf[:n]), "requires root") {
		t.Errorf("expected a root-required rejection, got %q", string(buf[:n]))
	}
}
