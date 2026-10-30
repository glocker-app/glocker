package usage

import (
	"context"
	"io"
	"os"
	"time"
)

// RunAgent is the capture half of the tracker, run in a child process that the
// daemon has dropped to the desktop user's uid (see Supervisor). It selects a
// backend for the user's session and streams each Sample as a JSON line to w
// (the daemon reads it on the other end of the pipe and owns the log file).
//
// Why a child at all: the root daemon can't capture the desktop itself. On
// dbus-broker (the default on Fedora/Bazzite and KDE Plasma) the user's session
// bus refuses a connection from root on a uid mismatch, and a KWin script can
// only call back onto that same session bus. Running capture as the user makes
// the bus connection authorised; the daemon keeps ownership of usage.jsonl so a
// blocked user (who is in the glocker group) can't forge or truncate their own
// record.
//
// RunAgent returns nil on a clean shutdown (ctx cancelled, e.g. SIGTERM from
// systemd) and a non-nil error when the output pipe breaks — which is how it
// notices the daemon died without tearing down its cgroup. A capture error is
// never fatal: the backend re-selects and the loop keeps running (a session
// that isn't up yet, or a compositor restart, just yields no samples until it
// returns).
func RunAgent(ctx context.Context, w io.Writer, interval time.Duration, opts Options, logf func(string, ...any)) error {
	if interval <= 0 {
		interval = DefaultInterval
	}
	source := NewAutoSource(opts, logf)
	defer source.Close()
	return streamSamples(ctx, w, interval, source, logf)
}

// streamSamples captures from source and writes each sample to w as a JSON line,
// immediately and then once per interval, until ctx is cancelled (→ nil) or a
// write fails (→ the error, meaning the reader has gone). Split out from RunAgent
// so the loop can be tested with a mock source and an in-memory writer.
func streamSamples(ctx context.Context, w io.Writer, interval time.Duration, source Source, logf func(string, ...any)) error {
	sink := NewJSONLSink(w)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := captureOnce(source, sink, logf); err != nil {
			return err // the pipe to the daemon is gone
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// captureOnce samples once and writes it. A capture error is logged (deduped by
// AutoSource) and swallowed so the loop keeps running; only a write error is
// returned, since it means the daemon's end of the pipe has closed.
func captureOnce(source Source, sink Sink, logf func(string, ...any)) error {
	sample, err := source.Capture()
	if err != nil {
		return nil // AutoSource already logged it and will re-select
	}
	if err := sink.Write(sample); err != nil {
		logf("usage agent: output pipe closed (%v); exiting", err)
		return err
	}
	return nil
}

// AgentOptionsFromEnv builds capture Options from the environment the daemon set
// when spawning the agent (XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS, and, for
// X11, DISPLAY/XAUTHORITY). The agent runs as the user but not inside their
// login session, so it has only the environment the daemon passed, not the
// session's own.
func AgentOptionsFromEnv() Options {
	return Options{
		Display:     os.Getenv("DISPLAY"),
		XAuthority:  os.Getenv("XAUTHORITY"),
		DBusAddress: os.Getenv("DBUS_SESSION_BUS_ADDRESS"),
		RuntimeDir:  os.Getenv("XDG_RUNTIME_DIR"),
	}
}
