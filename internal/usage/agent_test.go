package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/user"
	"strconv"
	"strings"
	"testing"
	"time"
)

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func noLog(string, ...any) {}

// streamSamples should emit the captured sample as a JSON line and stop cleanly
// when the context is cancelled.
func TestStreamSamplesWritesThenStops(t *testing.T) {
	src := &mockSource{samples: []Sample{sampleActiveFirefox()}}
	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer
	w := writerFunc(func(p []byte) (int, error) {
		n, err := buf.Write(p)
		cancel() // one sample is enough; end the loop
		return n, err
	})

	if err := streamSamples(ctx, w, 10*time.Millisecond, src, noLog); err != nil {
		t.Fatalf("streamSamples returned %v, want nil on cancel", err)
	}

	var got Sample
	line := strings.TrimRight(buf.String(), "\n")
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("output is not one JSON sample: %q (%v)", buf.String(), err)
	}
	if a := got.Active(); a == nil || a.Class != "firefox" {
		t.Errorf("sample did not round-trip through the agent stream: %+v", got)
	}
}

// A broken output pipe must end the agent (that's how it notices the daemon is
// gone), surfaced as a non-nil error from streamSamples.
func TestStreamSamplesExitsOnWriteError(t *testing.T) {
	src := &mockSource{samples: []Sample{{}}}
	w := writerFunc(func([]byte) (int, error) { return 0, io.ErrClosedPipe })

	// A long interval so only the immediate capture runs: it must fail on write.
	err := streamSamples(context.Background(), w, time.Hour, src, noLog)
	if err == nil {
		t.Fatal("streamSamples returned nil, want an error when the pipe is closed")
	}
}

// pump decodes the agent's JSON lines into the sink and skips an unreadable line
// without dropping the good ones around it.
func TestSupervisorPumpSkipsBadLines(t *testing.T) {
	s := &Supervisor{logf: noLog}
	good, _ := json.Marshal(sampleActiveFirefox())
	in := string(good) + "\n" + "{ not json\n" + string(good) + "\n"

	var out bytes.Buffer
	s.pump(strings.NewReader(in), NewJSONLSink(&out))

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d samples through pump, want 2 (bad line skipped):\n%s", len(lines), out.String())
	}
}

func TestLookupCredential(t *testing.T) {
	if _, err := lookupCredential(""); err == nil {
		t.Error("empty user should error (no session to capture)")
	}
	if _, err := lookupCredential("definitely-not-a-user-xyzzy"); err == nil {
		t.Error("unknown user should error")
	}

	me, err := user.Current()
	if err != nil {
		t.Skipf("cannot determine current user: %v", err)
	}
	cred, err := lookupCredential(me.Username)
	if err != nil {
		t.Fatalf("lookupCredential(%q): %v", me.Username, err)
	}
	if got := strconv.Itoa(int(cred.Uid)); got != me.Uid {
		t.Errorf("uid = %s, want %s", got, me.Uid)
	}
	gid, _ := strconv.Atoi(me.Gid)
	found := false
	for _, g := range cred.Groups {
		if int(g) == gid {
			found = true
		}
	}
	if !found {
		t.Errorf("primary gid %d not in supplementary groups %v", gid, cred.Groups)
	}
}
