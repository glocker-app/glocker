package editor

import (
	"reflect"
	"testing"

	"glocker/internal/config"
)

func roundTrip(t *testing.T, in []config.TimeWindow) []config.TimeWindow {
	t.Helper()
	out := weekFromWindows(in).windows()
	if weekFromWindows(out) != weekFromWindows(in) {
		t.Errorf("schedule changed: %v -> %v", in, out)
	}
	return out
}

func TestWeekRoundTrip(t *testing.T) {
	wd := []string{"Mon", "Tue", "Wed", "Thu", "Fri"}
	all := append(append([]string{}, wd...), "Sat", "Sun")
	cases := [][]config.TimeWindow{
		{{Start: "09:00", End: "17:00", Days: wd}},
		{{Start: "00:00", End: "09:00", Days: all}, {Start: "10:00", End: "23:59", Days: all}},
		{{Start: "08:00", End: "19:00", Days: wd}, {Start: "19:00", End: "21:00", Days: wd}},
	}
	for _, in := range cases {
		roundTrip(t, in)
	}
	// Same windows come back for the simple cases.
	if out := roundTrip(t, cases[0]); !reflect.DeepEqual(out, cases[0]) {
		t.Errorf("got %v", out)
	}
	if out := roundTrip(t, cases[1]); !reflect.DeepEqual(out, cases[1]) {
		t.Errorf("got %v", out)
	}
}

func TestWeekOvernight(t *testing.T) {
	in := []config.TimeWindow{{Start: "22:00", End: "06:00", Days: []string{"Mon", "Tue", "Wed", "Thu", "Fri"}}}
	out := roundTrip(t, in)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("got %v", out)
	}
}

func TestProgramKilled(t *testing.T) {
	allow := []config.TimeWindow{{Start: "18:00", End: "23:59", Days: weekDays}}
	k := programKilled(nil, allow)
	if !k[0][10] || k[0][40] {
		t.Error("allow windows should invert")
	}
	if !programKilled(nil, nil).full() {
		t.Error("no windows means always killed")
	}
}
