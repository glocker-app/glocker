package main

// scheduleScreen edits a whole weekly schedule on one grid: move a cursor over
// half-hour cells and paint them, select rectangles, copy a day onto others,
// or click and drag with the mouse.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	cellOn     = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	cellCursor = lipgloss.NewStyle().Reverse(true).Foreground(lipgloss.Color("3"))
)

// Where the grid sits on screen, for mouse hits: the content pane starts after
// the sidebar and separator; the grid after the title, blank and axis lines
// and the day label.
const (
	gridX = sideW + 3 + 4
	gridY = 3
)

type scheduleScreen struct {
	title    string
	on, off  string // legend: what a filled / empty cell means
	w, orig  week
	d, s     int  // cursor: day, slot
	sel      bool // rectangle selection from (ad, as) to the cursor
	ad, as   int
	clip     *[slots]bool
	dragging bool
	paint    bool // value a mouse drag paints
	save     func(week) string
	err      string
}

func newSchedule(title, on, off string, w week, save func(week) string) *scheduleScreen {
	return &scheduleScreen{title: title, on: on, off: off, w: w, orig: w, s: 18, save: save}
}

func (sc *scheduleScreen) rect() (d0, d1, s0, s1 int) {
	if !sc.sel {
		return sc.d, sc.d, sc.s, sc.s
	}
	return min(sc.ad, sc.d), max(sc.ad, sc.d), min(sc.as, sc.s), max(sc.as, sc.s)
}

func (sc *scheduleScreen) fill(v bool) {
	d0, d1, s0, s1 := sc.rect()
	for d := d0; d <= d1; d++ {
		for s := s0; s <= s1; s++ {
			sc.w[d][s] = v
		}
	}
	sc.sel = false
}

func (sc *scheduleScreen) move(dd, ds int) {
	sc.d = (sc.d + dd + 7) % 7
	sc.s = min(max(sc.s+ds, 0), slots-1)
}

func (sc *scheduleScreen) Update(a *app, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		if !a.readOnly {
			sc.mouse(msg, a.bannerH())
		}
	case tea.KeyMsg:
		sc.err = ""
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
			for _, r := range msg.Runes {
				sc.key(a, string(r))
			}
			return nil
		}
		sc.key(a, msg.String())
	}
	return nil
}

func (sc *scheduleScreen) mouse(m tea.MouseMsg, top int) {
	d, s := m.Y-gridY-top, m.X-gridX
	inside := d >= 0 && d < 7 && s >= 0 && s < slots
	switch {
	case m.Action == tea.MouseActionPress && m.Button == tea.MouseButtonLeft && inside:
		sc.sel, sc.dragging = false, true
		sc.paint = !sc.w[d][s]
		sc.d, sc.s = d, s
		sc.w[d][s] = sc.paint
	case m.Action == tea.MouseActionMotion && sc.dragging && inside:
		sc.d, sc.s = d, s
		sc.w[d][s] = sc.paint
	case m.Action == tea.MouseActionRelease:
		sc.dragging = false
	}
}

// viewKeys are allowed read-only.
var viewKeys = map[string]bool{"left": true, "h": true, "right": true, "l": true, "up": true, "k": true,
	"down": true, "j": true, "shift+left": true, "H": true, "shift+right": true, "L": true,
	"home": true, "0": true, "end": true, "$": true, "esc": true, "enter": true}

func (sc *scheduleScreen) key(a *app, k string) {
	if a.readOnly && !viewKeys[k] {
		a.refuse()
		return
	}
	switch k {
	case "left", "h":
		sc.move(0, -1)
	case "right", "l":
		sc.move(0, 1)
	case "shift+left", "H":
		sc.move(0, -2)
	case "shift+right", "L":
		sc.move(0, 2)
	case "home", "0":
		sc.s = 0
	case "end", "$":
		sc.s = slots - 1
	case "up", "k":
		sc.move(-1, 0)
	case "down", "j":
		sc.move(1, 0)
	case "v":
		sc.sel = !sc.sel
		sc.ad, sc.as = sc.d, sc.s
	case " ":
		if sc.sel {
			sc.fill(!sc.w[sc.ad][sc.as])
		} else {
			sc.w[sc.d][sc.s] = !sc.w[sc.d][sc.s]
			sc.move(0, 1)
		}
	case "f":
		sc.fill(true)
	case "x":
		sc.fill(false)
	case "y":
		row := sc.w[sc.d]
		sc.clip = &row
	case "p":
		if sc.clip != nil {
			d0, d1, _, _ := sc.rect()
			for d := d0; d <= d1; d++ {
				sc.w[d] = *sc.clip
			}
			sc.sel = false
		}
	case "c":
		sc.w = week{}
	case "A":
		for d := range 7 {
			for s := range slots {
				sc.w[d][s] = true
			}
		}
	case "u":
		sc.w = sc.orig
	case "enter":
		if sc.w != sc.orig {
			if msg := sc.save(sc.w); msg != "" {
				sc.err = msg
				return
			}
		}
		a.pop()
	case "esc":
		if sc.sel {
			sc.sel = false
			return
		}
		a.pop()
	}
}

func (sc *scheduleScreen) View(a *app, w, h int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(sc.title) + "\n\n")
	axis := []rune(strings.Repeat(" ", slots+2))
	for hr := 0; hr <= 24; hr += 6 {
		copy(axis[min(hr*2, slots):], []rune(fmt.Sprint(hr)))
	}
	b.WriteString("    " + dimStyle.Render(string(axis)) + "\n")
	d0, d1, s0, s1 := sc.rect()
	for d, name := range weekDays {
		label := dimStyle.Render(name)
		if d == sc.d {
			label = titleStyle.Render(name)
		}
		b.WriteString(label + " ")
		for s := range slots {
			ch, st := "·", dimStyle
			if sc.w[d][s] {
				ch, st = "█", cellOn
			}
			switch {
			case d == sc.d && s == sc.s:
				st = cellCursor
			case sc.sel && d >= d0 && d <= d1 && s >= s0 && s <= s1:
				st = st.Background(lipgloss.Color("238"))
			}
			b.WriteString(st.Render(ch))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + cellOn.Render("█") + " " + sc.on + "   " + dimStyle.Render("·") + " " + sc.off +
		dimStyle.Render(fmt.Sprintf("      %s %s–%s", weekDays[sc.d], slotTime(sc.s), slotTime(sc.s+1))) + "\n\n")
	b.WriteString(lipgloss.NewStyle().Width(w).Render(sc.summary()) + "\n")
	if sc.w != sc.orig {
		b.WriteString(dimStyle.Render("modified") + "\n")
	}
	if sc.err != "" {
		b.WriteString(errStyle.Render(sc.err) + "\n")
	}
	return b.String()
}

func (sc *scheduleScreen) summary() string {
	switch {
	case sc.w.full():
		return sc.on + " always"
	case sc.w.empty():
		return sc.on + " never"
	}
	return sc.on + ": " + fmtWindows(sc.w.windows())
}

func (sc *scheduleScreen) Help(a *app) (string, string) {
	if sc.sel {
		return "Selecting: move to stretch the box, then space to toggle it, f fill, x clear, p paste the copied day.",
			"arrows stretch · space toggle · f fill · x clear · p paste · esc cancel selection"
	}
	return "Paint half-hour cells, or click and drag. v selects a box. y copies a day, p pastes it.",
		"arrows move · space paint · v select · y copy day · p paste · c clear · A all · u undo · enter done · esc cancel"
}
