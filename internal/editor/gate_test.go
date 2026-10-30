package editor

import (
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
	"time"

	"glocker/internal/config"
)

func TestNextFlip(t *testing.T) {
	ws := []config.TimeWindow{{Start: "18:00", End: "20:00", Days: weekDays[:5]}}
	// Monday 2024-01-01 10:00: blocked until 18:00 the same day.
	mon10 := time.Date(2024, 1, 1, 10, 0, 0, 0, time.Local)
	allowed, at, ok := nextFlip(ws, mon10)
	if allowed || !ok || at.Format("Mon 15:04") != "Mon 18:00" {
		t.Fatalf("got %v %v %v", allowed, at, ok)
	}
	if got := fmtWhen(at, mon10); got != "18:00 (in 8h 00m)" {
		t.Errorf("fmtWhen = %q", got)
	}
	// Saturday: next window is Monday.
	sat := time.Date(2024, 1, 6, 12, 0, 0, 0, time.Local)
	_, at, _ = nextFlip(ws, sat)
	if !strings.HasPrefix(fmtWhen(at, sat), "Mon 18:00") {
		t.Errorf("got %s", fmtWhen(at, sat))
	}
	d := &Doc{Main: &File{Root: mustParse(t, "sudoers:\n  enabled: true\n  allow_windows:\n    - {start: \"18:00\", end: \"20:00\", days: [Mon]}\n")}}
	if got := gateBanner(d, mon10.Add(9*time.Hour), true); !got.alert || !strings.Contains(got.text, "open until 20:00 (in 1h 00m)") {
		t.Errorf("banner = %+v", got)
	}
	if got := gateBanner(d, mon10, false); got.alert || !strings.Contains(got.text, "blocked until 18:00") {
		t.Errorf("banner = %+v", got)
	}
	if _, _, ok := nextFlip(nil, mon10); ok {
		t.Error("no windows never flips")
	}
}

func mustParse(t *testing.T, s string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	must(t, yaml.Unmarshal([]byte(s), &n))
	return &n
}
