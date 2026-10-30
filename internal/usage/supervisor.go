package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

// AgentFlag is the hidden flag the daemon passes to re-exec itself as the
// capture agent (see RunAgent). Defined here so the daemon's flag wiring and the
// supervisor can't drift.
const AgentFlag = "-usage-agent"

// supervisor backoff bounds for respawning a crashed agent.
const (
	minRespawnDelay = 1 * time.Second
	maxRespawnDelay = 30 * time.Second
	// stableFor is how long an agent must stay up before we treat it as healthy
	// and reset the backoff — so a crash loop backs off but a one-off crash
	// doesn't penalise the next, legitimate, start.
	stableFor = 60 * time.Second
)

// Supervisor keeps a single uid-dropped capture agent alive for the daemon's
// lifetime and writes the samples it streams back into the usage log. The daemon
// owns the log (root); the agent only produces samples (see RunAgent for why
// capture can't happen in the root daemon itself).
type Supervisor struct {
	selfPath string // the glocker binary to re-exec (os.Executable)
	user     string // desktop user to drop to (cfg.User)
	logFile  string
	interval time.Duration
	env      []string // environment handed to the agent (session reach)
	logf     func(string, ...any)
}

// NewSupervisor builds a Supervisor. env should carry the desktop session's
// reach (XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS, and DISPLAY/XAUTHORITY for
// X11); the daemon assembles it since it has none of the session's own
// environment.
func NewSupervisor(selfPath, usr, logFile string, interval time.Duration, env []string, logf func(string, ...any)) *Supervisor {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Supervisor{selfPath: selfPath, user: usr, logFile: logFile, interval: interval, env: env, logf: logf}
}

// Run supervises the agent until ctx is cancelled. It opens the log once (the
// daemon's handle, kept across respawns) and then spawns the agent, restarting
// it with a capped backoff if it exits. A crash while nobody is logged in is not
// special: the agent stays up and simply produces no samples until a session
// appears, so Run only respawns on an actual exit.
func (s *Supervisor) Run(ctx context.Context) {
	cred, err := lookupCredential(s.user)
	if err != nil {
		s.logf("usage monitor: %v; usage tracking disabled", err)
		return
	}

	sink, err := NewJSONLFileSink(s.logFile)
	if err != nil {
		s.logf("usage monitor: cannot open log %s: %v", s.logFile, err)
		return
	}
	defer sink.Close()

	s.logf("usage monitor: supervising capture agent (user %s) → %s, every %s", s.user, s.logFile, s.interval)

	delay := minRespawnDelay
	for {
		started := time.Now()
		runErr := s.runAgent(ctx, cred, sink)
		if ctx.Err() != nil {
			return // daemon shutting down
		}
		// Reset the backoff if the agent had been up long enough to count as
		// healthy; otherwise grow it so a crash loop doesn't spin.
		if time.Since(started) >= stableFor {
			delay = minRespawnDelay
		}
		s.logf("usage monitor: capture agent exited (%v); restarting in %s", runErr, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > maxRespawnDelay {
			delay = maxRespawnDelay
		}
	}
}

// runAgent spawns one agent and pumps its stdout (JSON samples) into the sink
// until it exits or ctx is cancelled. Its stderr is relayed to the daemon log so
// backend-selection messages are visible.
func (s *Supervisor) runAgent(ctx context.Context, cred *syscall.Credential, sink Sink) error {
	cmd := exec.CommandContext(ctx, s.selfPath, AgentFlag)
	cmd.Env = s.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	go s.relayStderr(stderr)
	s.pump(stdout, sink) // returns when the agent closes stdout (i.e. exits)
	return cmd.Wait()
}

// pump decodes one JSON sample per line from the agent's stdout and writes each
// to the daemon-owned sink, until stdout closes. An unreadable line is logged
// and skipped (never fatal). Split out so it can be tested without spawning a
// process.
func (s *Supervisor) pump(stdout io.Reader, sink Sink) {
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scan.Scan() {
		var sample Sample
		if err := json.Unmarshal(scan.Bytes(), &sample); err != nil {
			s.logf("usage monitor: skipping unreadable sample from agent: %v", err)
			continue
		}
		if err := sink.Write(sample); err != nil {
			s.logf("usage monitor: writing sample: %v", err)
		}
	}
}

// relayStderr forwards the agent's stderr (its log output) to the daemon log.
func (s *Supervisor) relayStderr(r io.Reader) {
	scan := bufio.NewScanner(r)
	for scan.Scan() {
		s.logf("usage agent: %s", scan.Text())
	}
}

// lookupCredential resolves the desktop user to the uid/gid/supplementary groups
// the agent is dropped to. An empty or unknown user is an error — without a user
// there's no session to capture.
func lookupCredential(username string) (*syscall.Credential, error) {
	if username == "" {
		return nil, fmt.Errorf("no desktop user configured (config `user`)")
	}
	u, err := user.Lookup(username)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve user %q: %w", username, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("bad uid for %q: %w", username, err)
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("bad gid for %q: %w", username, err)
	}
	groups := []uint32{uint32(gid)}
	if ids, err := u.GroupIds(); err == nil {
		for _, g := range ids {
			if n, err := strconv.ParseUint(g, 10, 32); err == nil && uint32(n) != uint32(gid) {
				groups = append(groups, uint32(n))
			}
		}
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}, nil
}
