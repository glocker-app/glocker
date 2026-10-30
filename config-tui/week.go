package main

// A week is the schedule as 7×48 half-hour cells. The editor paints cells and
// converts back to time windows on save; that's lossless for windows on
// half-hour boundaries, and anything finer snaps to 30 minutes, but only if
// the user actually changes the grid.

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/utils"
)

const slots = 48 // half hours per day

type week [7][slots]bool

// 2024-01-01 is a Monday.
var refMonday = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// slotMid is the midpoint of a cell; a cell is on if its midpoint is.
func slotMid(day, slot int) time.Time {
	return refMonday.AddDate(0, 0, day).Add(time.Duration(slot)*30*time.Minute + 15*time.Minute)
}

func weekOf(on func(t time.Time) bool) week {
	var w week
	for d := range 7 {
		for s := range slots {
			w[d][s] = on(slotMid(d, s))
		}
	}
	return w
}

func weekFromWindows(ws []config.TimeWindow) week {
	return weekOf(func(t time.Time) bool { return utils.InAnyWindow(ws, t) })
}

// programKilled mirrors the forbidden-program precedence: in a kill window →
// killed; else allow windows set → killed outside them; else no windows →
// always killed.
func programKilled(kill, allow []config.TimeWindow) week {
	return weekOf(func(t time.Time) bool {
		switch {
		case utils.InAnyWindow(kill, t):
			return true
		case len(allow) > 0:
			return !utils.InAnyWindow(allow, t)
		default:
			return len(kill) == 0
		}
	})
}

func (w week) full() bool  { return w.count() == 7*slots }
func (w week) empty() bool { return w.count() == 0 }

func (w week) count() int {
	n := 0
	for d := range 7 {
		for s := range slots {
			if w[d][s] {
				n++
			}
		}
	}
	return n
}

func slotTime(s int) string {
	if s >= slots {
		return "23:59" // ends are inclusive, so 23:59 reaches midnight
	}
	return fmt.Sprintf("%02d:%02d", s/2, s%2*30)
}

type run struct{ day, start, end int } // end exclusive, in slots

// windows converts the grid to time windows: one window per run of cells per
// day, with runs of the same times grouped across days. Joining a run that
// reaches midnight with the next day's run from midnight gives an overnight
// window; that's only used when it makes the result shorter (it doesn't for
// "00:00–09:00 and 10:00–23:59 every day").
func (w week) windows() []config.TimeWindow {
	split, joined := w.windowsJoin(false), w.windowsJoin(true)
	if len(joined) < len(split) {
		return joined
	}
	return split
}

func (w week) windowsJoin(join bool) []config.TimeWindow {
	var runs []run
	for d := range 7 {
		for s := 0; s < slots; {
			if !w[d][s] {
				s++
				continue
			}
			e := s
			for e < slots && w[d][e] {
				e++
			}
			runs = append(runs, run{d, s, e})
			s = e
		}
	}
	used := make([]bool, len(runs))
	type span struct{ start, end string }
	var order []span
	days := map[span][]int{}
	add := func(sp span, d int) {
		if _, ok := days[sp]; !ok {
			order = append(order, sp)
		}
		days[sp] = append(days[sp], d)
	}
	for i, r := range runs {
		if used[i] {
			continue
		}
		used[i] = true
		end := r.end
		// Overnight: this run reaches midnight, the next day's starts at it.
		if join && r.end == slots && r.start > 0 {
			next := (r.day + 1) % 7
			for j, q := range runs {
				if !used[j] && q.day == next && q.start == 0 && q.end < slots {
					used[j] = true
					end = q.end
					break
				}
			}
		}
		add(span{slotTime(r.start), slotTime(end)}, r.day)
	}
	slices.SortStableFunc(order, func(a, b span) int {
		if c := days[a][0] - days[b][0]; c != 0 {
			return c
		}
		return strings.Compare(a.start, b.start)
	})
	out := make([]config.TimeWindow, 0, len(order))
	for _, sp := range order {
		var names []string
		for _, d := range days[sp] {
			names = append(names, weekDays[d])
		}
		out = append(out, config.TimeWindow{Start: sp.start, End: sp.end, Days: names})
	}
	return out
}
