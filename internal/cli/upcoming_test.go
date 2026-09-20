package cli

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"glocker/internal/schedule"
)

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// renderPlain runs renderUpcoming into a frame and strips the colour, so the
// assertions read like what lands in a non-TTY terminal.
func renderPlain(changes []schedule.Change, now time.Time) string {
	f := newFrame("test", "")
	renderUpcoming(f, changes, now)
	return ansi.ReplaceAllString(f.close(), "")
}

func TestRenderUpcomingNothingScheduled(t *testing.T) {
	out := renderPlain(nil, time.Now())
	if !strings.Contains(out, "none scheduled") {
		t.Errorf("want a 'none scheduled' line, got:\n%s", out)
	}
}

func TestRenderUpcomingLines(t *testing.T) {
	now := time.Date(2026, 9, 21, 10, 0, 40, 0, time.Local)
	out := renderPlain([]schedule.Change{
		{At: now.Add(6*time.Minute + 20*time.Second), Kind: schedule.KindDomain, Name: "youtube.com", Blocking: true},
		{At: now.Add(9 * time.Minute), Kind: schedule.KindProgram, Name: "steam", Blocking: false},
		{At: now.Add(12 * time.Minute), Kind: schedule.KindSudo, Blocking: true},
	}, now)

	for _, want := range []string{
		"changes        3",
		"block   youtube.com",
		"allow   steam",
		"lock    sudo",
		// 6m20s away must round UP, so the countdown never overstates the time left.
		"in 7m",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderUpcomingCapsTheList(t *testing.T) {
	now := time.Now()
	var changes []schedule.Change
	for i := 0; i < 9; i++ {
		changes = append(changes, schedule.Change{
			At: now.Add(time.Duration(i+1) * time.Minute), Kind: schedule.KindDomain, Name: "d.example", Blocking: true,
		})
	}
	out := renderPlain(changes, now)
	if !strings.Contains(out, "changes        9") {
		t.Errorf("want the full count in the summary row, got:\n%s", out)
	}
	if !strings.Contains(out, "… and 3 more") {
		t.Errorf("want the overflow line, got:\n%s", out)
	}
}
