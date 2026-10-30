package main

// picker edits one time window inline: from/to times and day toggles on two
// lines, with a week grid underneath that shows the result (and the list's
// other windows) using glocker's own window evaluation, so overnight windows
// render exactly as they're enforced.

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"glocker/internal/config"
	"glocker/internal/utils"
)

var (
	focusStyle = lipgloss.NewStyle().Reverse(true).Bold(true)
	dayOn      = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	gridCur    = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	gridOther  = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
)

// Focus positions: the four time parts, then the seven days.
const (
	fStartH = iota
	fStartM
	fEndH
	fEndM
	fDay0
	fLast = fDay0 + 6
)

type picker struct {
	title  string
	t      [4]int // start h, start m, end h, end m
	days   [7]bool
	focus  int
	buf    string // digits typed into the focused time part
	others []config.TimeWindow
	save   func(config.TimeWindow)
	err    string
}

func newPicker(title string, w config.TimeWindow, others []config.TimeWindow, save func(config.TimeWindow)) *picker {
	p := &picker{title: title, others: others, save: save}
	if w.Start == "" {
		w = config.TimeWindow{Start: "09:00", End: "17:00", Days: weekDays[:5]}
	}
	fmt.Sscanf(w.Start, "%d:%d", &p.t[0], &p.t[1])
	fmt.Sscanf(w.End, "%d:%d", &p.t[2], &p.t[3])
	for i, d := range weekDays {
		p.days[i] = slices.Contains(w.Days, d)
	}
	return p
}

func (p *picker) window() config.TimeWindow {
	w := config.TimeWindow{
		Start: fmt.Sprintf("%02d:%02d", p.t[0], p.t[1]),
		End:   fmt.Sprintf("%02d:%02d", p.t[2], p.t[3]),
	}
	for i, on := range p.days {
		if on {
			w.Days = append(w.Days, weekDays[i])
		}
	}
	return w
}

func (p *picker) setDays(on ...int) {
	p.days = [7]bool{}
	for _, i := range on {
		p.days[i] = true
	}
}

func (p *picker) adjust(dir int) {
	switch p.focus {
	case fStartH, fEndH:
		p.t[p.focus] = (p.t[p.focus] + dir + 24) % 24
	case fStartM, fEndM:
		m := p.t[p.focus]
		if dir > 0 {
			m = (m/15 + 1) * 15
		} else {
			m = ((m+14)/15 - 1) * 15
		}
		p.t[p.focus] = (m + 60) % 60
	default:
		p.days[p.focus-fDay0] = !p.days[p.focus-fDay0]
	}
}

func (p *picker) typeDigit(d int) {
	if p.focus > fEndM {
		return
	}
	limit := 23
	if p.focus == fStartM || p.focus == fEndM {
		limit = 59
	}
	p.buf += fmt.Sprint(d)
	v := 0
	fmt.Sscanf(p.buf, "%d", &v)
	p.t[p.focus] = min(v, limit)
	if len(p.buf) == 2 {
		p.buf = ""
		p.focus++
	}
}

func (p *picker) Update(a *app, msg tea.Msg) tea.Cmd {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	// Fast typing or a paste arrives as one multi-rune message.
	if km.Type == tea.KeyRunes && len(km.Runes) > 1 {
		for _, r := range km.Runes {
			p.key(a, string(r))
		}
		return nil
	}
	p.key(a, km.String())
	return nil
}

func (p *picker) key(a *app, k string) {
	if k != "enter" {
		p.err = ""
	}
	if len(k) == 1 && k[0] >= '0' && k[0] <= '9' {
		p.typeDigit(int(k[0] - '0'))
		return
	}
	p.buf = ""
	switch k {
	case "right", "l", "tab", ":":
		p.focus = min(p.focus+1, fLast)
	case "left", "h", "shift+tab":
		p.focus = max(p.focus-1, 0)
	case "up", "k", "+":
		p.adjust(1)
	case "down", "j", "-":
		p.adjust(-1)
	case " ", "x":
		if p.focus >= fDay0 {
			p.adjust(1)
		}
	case "w":
		p.setDays(0, 1, 2, 3, 4)
	case "e":
		p.setDays(5, 6)
	case "a":
		p.setDays(0, 1, 2, 3, 4, 5, 6)
	case "enter":
		w := p.window()
		if len(w.Days) == 0 {
			p.err = "Pick at least one day."
			return
		}
		if w.Start == w.End {
			p.err = "From and to are the same."
			return
		}
		a.pop()
		p.save(w)
	case "esc":
		a.pop()
	}
}

func (p *picker) part(i int, s string) string {
	if p.focus == i {
		return focusStyle.Render(s)
	}
	return s
}

func (p *picker) View(a *app, w, h int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(p.title) + "\n\n")

	t := func(i int) string { return p.part(i, fmt.Sprintf("%02d", p.t[i])) }
	b.WriteString(fmt.Sprintf("  From  %s:%s     To  %s:%s", t(fStartH), t(fStartM), t(fEndH), t(fEndM)))
	if win := p.window(); win.End < win.Start {
		b.WriteString(dimStyle.Render("   ends next day"))
	}
	b.WriteString("\n\n  Days  ")
	for i, d := range weekDays {
		s := dimStyle.Render(d)
		if p.days[i] {
			s = dayOn.Render(d)
		}
		if p.focus == fDay0+i {
			s = focusStyle.Render(d)
		}
		b.WriteString(s + " ")
	}
	b.WriteString("\n\n")
	b.WriteString(p.grid(w))
	if p.err != "" {
		b.WriteString("\n" + errStyle.Render(p.err))
	}
	return b.String()
}

// grid draws the week, one row per day, one cell per half hour (or per hour
// when narrow). A cell is filled if its midpoint falls inside the window.
func (p *picker) grid(w int) string {
	cells, step := 48, 30*time.Minute
	if w < 4+48+2 {
		cells, step = 24, time.Hour
	}
	perHour := cells / 24
	axis := []rune(strings.Repeat(" ", cells+1))
	for hr := 0; hr <= 24; hr += 6 {
		label := []rune(fmt.Sprint(hr))
		pos := min(hr*perHour, cells+1-len(label))
		copy(axis[pos:], label)
	}
	var b strings.Builder
	b.WriteString("      " + dimStyle.Render(string(axis)) + "\n")
	cur := []config.TimeWindow{p.window()}
	// 2024-01-01 is a Monday; any Monday works.
	monday := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for d, name := range weekDays {
		b.WriteString("  " + dimStyle.Render(name) + " ")
		for c := 0; c < cells; c++ {
			at := monday.AddDate(0, 0, d).Add(time.Duration(c)*step + step/2)
			switch {
			case utils.InAnyWindow(cur, at):
				b.WriteString(gridCur.Render("█"))
			case utils.InAnyWindow(p.others, at):
				b.WriteString(gridOther.Render("▒"))
			default:
				b.WriteString(dimStyle.Render("·"))
			}
		}
		b.WriteString("\n")
	}
	if len(p.others) > 0 {
		b.WriteString("      " + gridCur.Render("█") + dimStyle.Render(" this window  ") + gridOther.Render("▒") + dimStyle.Render(" other windows") + "\n")
	}
	return b.String()
}

func (p *picker) Help(a *app) (string, string) {
	return "Type digits or use ↑↓ to set times. w weekdays · e weekends · a every day.",
		"←→ move · ↑↓ change · space toggle day · enter save · esc cancel"
}
