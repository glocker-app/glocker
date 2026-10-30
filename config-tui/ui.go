package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/pmezard/go-difflib/difflib"
)

// The palette of `glocker -status` (internal/cli/style.go): dim grey for
// structure and labels, one muted amber accent for titles and headings, green
// and red only where they mean something.
var (
	cAccent = lipgloss.Color("#C8A055")
	cDim    = lipgloss.Color("#697382")
	cGood   = lipgloss.Color("#78B982")
	cAlert  = lipgloss.Color("#CD5F5F")
	cWarn   = lipgloss.Color("#E6BE50")
	cBlack  = lipgloss.Color("#000000")

	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	headerStyle = lipgloss.NewStyle().Foreground(cAccent)
	dimStyle    = lipgloss.NewStyle().Foreground(cDim)
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sideSel     = lipgloss.NewStyle().Bold(true).Foreground(cBlack).Background(cAccent)
	okStyle     = lipgloss.NewStyle().Foreground(cGood)
	errStyle    = lipgloss.NewStyle().Foreground(cAlert)
	addStyle    = lipgloss.NewStyle().Foreground(cGood)
	delStyle    = lipgloss.NewStyle().Foreground(cAlert)
	hunkStyle   = lipgloss.NewStyle().Foreground(cAccent)
)

// frameHead renders "<corner>─ title ────" across w, like -status's frame.
func frameHead(corner, title string, st lipgloss.Style, w int) string {
	rule := strings.Repeat("─", max(w-lipgloss.Width(title)-4, 2))
	return dimStyle.Render(corner+"─ ") + st.Render(title) + " " + dimStyle.Render(rule)
}

var gutter = dimStyle.Render("│ ")

// banner is the bar across the top: who can edit and the sudo window.
type banner struct {
	label, text string
	alert       bool // red read-only bar, else yellow edit bar
}

func (b banner) render(w int) string {
	bg := cWarn
	if b.alert {
		bg = cAlert
	}
	badge := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(cBlack).Render(" " + b.label + " ")
	rest := lipgloss.NewStyle().Foreground(cBlack).Background(bg)
	return lipgloss.NewStyle().Background(bg).Width(w).Render(
		rest.Render(" ") + badge + rest.Bold(true).Render(" "+truncate(b.text, max(w-lipgloss.Width(b.label)-5, 5))))
}

const sideW = 24

// screen is something shown in the content pane (or full screen for diffs).
type screen interface {
	Update(a *app, msg tea.Msg) tea.Cmd
	View(a *app, w, h int) string
	// Help returns a line describing the focused thing and the keys that apply.
	Help(a *app) (text, keys string)
}

type app struct {
	doc       *Doc
	suffix    string
	sections  []*section
	roots     []*listScreen
	sec       int
	focusSide bool
	stack     []screen // nested screens over the current section
	full      screen   // full-screen overlay (the diff)
	status    string
	statusErr bool
	quitArmed bool
	readOnly  bool    // not root: browse only
	banner    *banner // bar above everything; nil for none
	w, h      int
}

func newApp(d *Doc, suffix string) *app {
	a := &app{doc: d, suffix: suffix, w: 100, h: 30, focusSide: true}
	a.sections = sections()
	for _, s := range a.sections {
		a.roots = append(a.roots, a.sectionScreen(s))
	}
	return a
}

func (a *app) Init() tea.Cmd { return nil }

func (a *app) top() screen {
	if len(a.stack) > 0 {
		return a.stack[len(a.stack)-1]
	}
	return a.roots[a.sec]
}

func (a *app) push(s screen) tea.Cmd {
	a.stack = append(a.stack, s)
	a.focusSide = false
	if f, ok := s.(*formScreen); ok {
		f.form = f.form.WithWidth(min(a.contentW(), 60))
		return f.form.Init()
	}
	return nil
}

func (a *app) pop() {
	if len(a.stack) > 0 {
		a.stack = a.stack[:len(a.stack)-1]
	}
}

func (a *app) contentW() int { return max(a.w-sideW-1, 30) }

// helpH is the height of the bottom panel: rule, help text, keys, status.
const helpH = 4

func (a *app) bodyH() int { return max(a.h-helpH-a.bannerH(), 6) }

// bannerH is the lines the read-only banner takes (bar + gap).
func (a *app) bannerH() int {
	if a.banner == nil {
		return 0
	}
	return 2
}

// refuse explains why an edit didn't happen.
func (a *app) refuse() {
	msg := "Read-only."
	if a.banner != nil {
		msg = "Read-only: " + a.banner.text + "."
	}
	a.setStatus(msg, true)
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		a.w, a.h = ws.Width, ws.Height
	}
	if a.full != nil {
		return a, a.full.Update(a, msg)
	}
	km, isKey := msg.(tea.KeyMsg)
	if !isKey {
		if !a.focusSide {
			return a, a.top().Update(a, msg)
		}
		return a, nil
	}
	k := km.String()
	if k != "q" && k != "ctrl+c" {
		a.quitArmed = false
	}
	if k == "ctrl+c" {
		return a, a.quit()
	}
	if !a.focusSide {
		return a, a.top().Update(a, msg)
	}
	switch k {
	case "up", "k":
		a.sec = (a.sec + len(a.sections) - 1) % len(a.sections)
		a.stack = nil
	case "down", "j":
		a.sec = (a.sec + 1) % len(a.sections)
		a.stack = nil
	case "enter", "right", "l", "tab":
		a.focusSide = false
	case "s":
		return a, a.save()
	case "q":
		return a, a.quit()
	}
	return a, nil
}

func (a *app) View() string {
	if a.full != nil {
		return a.full.View(a, a.w, a.h-1) + "\n" + a.statusLine()
	}
	bh := a.bodyH()
	side := a.sidebar(bh)
	content := lipgloss.NewStyle().Width(a.contentW()).MaxHeight(bh).Render(a.top().View(a, a.contentW(), bh))
	body := lipgloss.JoinHorizontal(lipgloss.Top, side, " ", content)

	var text, keys string
	if a.focusSide {
		s := a.sections[a.sec]
		text, keys = s.help, "↑↓ section · enter open · s save · q quit"
	} else {
		text, keys = a.top().Help(a)
	}
	top := ""
	if a.banner != nil {
		top = a.banner.render(a.w) + "\n\n"
	}
	rule := dimStyle.Render(strings.Repeat("─", a.w))
	helpLine := lipgloss.NewStyle().MaxWidth(a.w).Render(text)
	return top + lipgloss.NewStyle().Height(bh).Render(body) + "\n" + rule + "\n" + helpLine + "\n" +
		dimStyle.Render(truncate(keys, a.w)) + "\n" + a.statusLine()
}

func (a *app) sidebar(h int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("glocker") + dimStyle.Render(" · config") + "\n\n")
	for i, s := range a.sections {
		dot := " "
		if s.state != nil {
			switch s.state(a.secNode(s)) {
			case "on":
				dot = okStyle.Render("●")
			case "off":
				dot = dimStyle.Render("○")
			}
		}
		label := fmt.Sprintf("%-*s", sideW-3, s.title)
		switch {
		case i == a.sec && a.focusSide:
			label = sideSel.Render(label)
		case i == a.sec:
			label = titleStyle.Render(label)
		default:
			label = dimStyle.Render(label)
		}
		b.WriteString(dot + " " + label + "\n")
	}
	return lipgloss.NewStyle().Width(sideW).Height(h).Render(b.String())
}

func (a *app) setStatus(s string, isErr bool) { a.status, a.statusErr = s, isErr }

func (a *app) statusLine() string {
	if a.status == "" {
		return ""
	}
	s := truncate(a.status, a.w)
	if a.statusErr {
		return errStyle.Render(s)
	}
	return okStyle.Render(s)
}

func (a *app) quit() tea.Cmd {
	if ch, _ := a.doc.Changes(); len(ch) > 0 && !a.quitArmed {
		a.quitArmed = true
		a.setStatus("Unsaved changes. Press q again to quit.", true)
		return nil
	}
	return tea.Quit
}

func (a *app) save() tea.Cmd {
	if a.readOnly {
		a.refuse()
		return nil
	}
	if err := a.doc.Validate(); err != nil {
		a.setStatus("Not saved: "+err.Error(), true)
		return nil
	}
	ch, err := a.doc.Changes()
	if err != nil {
		a.setStatus(err.Error(), true)
		return nil
	}
	if len(ch) == 0 {
		a.setStatus("No changes.", false)
		return nil
	}
	a.full = newDiffScreen(a, ch)
	return nil
}

func truncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// ── list screens ─────────────────────────────────────────────────────────────

type rowKind int

const (
	rowItem rowKind = iota
	rowHeader
	rowNote
)

type row struct {
	kind  rowKind
	label string
	value string
	help  string
	keys  string
	nav   bool                     // act only opens another screen, allowed read-only
	act   func(key string) tea.Cmd // nil: not actionable
}

func header(s string) row { return row{kind: rowHeader, label: s} }
func note(s string) row   { return row{kind: rowNote, label: s} }

type listScreen struct {
	title  string
	build  func() []row
	cursor int
	offset int
}

func (l *listScreen) selectable(rows []row, i int) bool {
	return i >= 0 && i < len(rows) && rows[i].kind == rowItem
}

func (l *listScreen) clamp(rows []row) {
	if l.selectable(rows, l.cursor) {
		return
	}
	for d := 1; d < len(rows)+l.cursor; d++ {
		if l.selectable(rows, l.cursor-d) {
			l.cursor -= d
			return
		}
		if l.selectable(rows, l.cursor+d) {
			l.cursor += d
			return
		}
	}
}

func (l *listScreen) move(rows []row, dir int) {
	for i := l.cursor + dir; i >= 0 && i < len(rows); i += dir {
		if l.selectable(rows, i) {
			l.cursor = i
			return
		}
	}
}

func (l *listScreen) Update(a *app, msg tea.Msg) tea.Cmd {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	rows := l.build()
	l.clamp(rows)
	switch k := km.String(); k {
	case "up", "k":
		l.move(rows, -1)
	case "down", "j":
		l.move(rows, 1)
	case "esc", "left", "h", "shift+tab":
		if len(a.stack) > 0 {
			a.pop()
		} else {
			a.focusSide = true
		}
	case "q":
		return a.quit()
	case "s":
		return a.save()
	default:
		if l.selectable(rows, l.cursor) && rows[l.cursor].act != nil {
			if a.readOnly && !(rows[l.cursor].nav && k == "enter") {
				a.refuse()
				return nil
			}
			a.setStatus("", false)
			cmd := rows[l.cursor].act(k)
			l.clamp(l.build())
			return cmd
		}
	}
	return nil
}

func (l *listScreen) Help(a *app) (string, string) {
	rows := l.build()
	l.clamp(rows)
	back := "esc sections"
	if len(a.stack) > 0 {
		back = "esc back"
	}
	if !l.selectable(rows, l.cursor) {
		return "", back
	}
	r := rows[l.cursor]
	keys := r.keys
	if keys != "" {
		keys += " · "
	}
	return r.help, keys + "s save · " + back
}

func (l *listScreen) View(a *app, w, h int) string {
	rows := l.build()
	l.clamp(rows)
	var b strings.Builder
	used := 0
	if l.title != "" {
		b.WriteString(frameHead("╭", l.title, titleStyle, w) + "\n" + dimStyle.Render("│") + "\n")
		used = 2
	}
	labelW := 0
	for _, r := range rows {
		if r.kind == rowItem && r.value != "" {
			labelW = max(labelW, lipgloss.Width(r.label))
		}
	}
	labelW = min(labelW, w/2)
	avail := max(h-used, 3)
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+avail {
		l.offset = l.cursor - avail + 1
	}
	if l.offset > 0 && l.offset < len(rows) && rows[l.offset-1].kind == rowHeader {
		l.offset--
	}
	focused := !a.focusSide
	end := min(len(rows), l.offset+avail)
	for i := l.offset; i < end; i++ {
		r := rows[i]
		switch r.kind {
		case rowHeader:
			if i > l.offset {
				b.WriteString(dimStyle.Render("│") + "\n")
			}
			b.WriteString(frameHead("├", r.label, headerStyle, w) + "\n")
		case rowNote:
			b.WriteString(gutter + "  " + dimStyle.Italic(true).Render(truncate(r.label, w-4)) + "\n")
		default:
			plain := truncate(r.label, max(labelW, w/2))
			mark, label := "  ", dimStyle.Render(plain)
			if i == l.cursor && focused {
				mark, label = cursorStyle.Render("› "), cursorStyle.Render(plain)
			}
			line := gutter + mark + label
			if r.value != "" {
				pad := max(labelW-lipgloss.Width(r.label), 0) + 2
				line += strings.Repeat(" ", pad) + truncate(r.value, max(w-labelW-8, 8))
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// ── text forms ───────────────────────────────────────────────────────────────

type formScreen struct {
	form *huh.Form
	done func()
	help string
}

func newForm(help string, done func(), fields ...huh.Field) *formScreen {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "cancel"))
	f := huh.NewForm(huh.NewGroup(fields...)).WithKeyMap(km).WithShowHelp(false)
	return &formScreen{form: f, done: done, help: help}
}

func (f *formScreen) Update(a *app, msg tea.Msg) tea.Cmd {
	m, cmd := f.form.Update(msg)
	f.form = m.(*huh.Form)
	switch f.form.State {
	case huh.StateCompleted:
		a.pop()
		f.done()
		return nil
	case huh.StateAborted:
		a.pop()
		return nil
	}
	return cmd
}

func (f *formScreen) View(a *app, w, h int) string { return f.form.View() }

func (f *formScreen) Help(a *app) (string, string) { return f.help, "enter confirm · esc cancel" }

// ── diff + save ──────────────────────────────────────────────────────────────

type diffScreen struct {
	changes []Change
	vp      viewport.Model
}

func newDiffScreen(a *app, ch []Change) *diffScreen {
	var b strings.Builder
	for _, c := range ch {
		dst := c.File.Path + a.suffix
		text, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: difflib.SplitLines(string(c.File.orig)), B: difflib.SplitLines(string(c.New)),
			FromFile: c.File.Path, ToFile: dst, Context: 2,
		})
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
				line = titleStyle.Render(line)
			case strings.HasPrefix(line, "+"):
				line = addStyle.Render(line)
			case strings.HasPrefix(line, "-"):
				line = delStyle.Render(line)
			case strings.HasPrefix(line, "@@"):
				line = hunkStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	vp := viewport.New(a.w, max(a.h-5, 5))
	vp.SetContent(b.String())
	return &diffScreen{changes: ch, vp: vp}
}

func (d *diffScreen) Update(a *app, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.vp.Width, d.vp.Height = msg.Width, max(msg.Height-5, 5)
	case tea.KeyMsg:
		switch msg.String() {
		case "y":
			written, err := a.doc.Write(d.changes, a.suffix)
			a.full = nil
			if err != nil {
				a.setStatus("Write failed: "+err.Error(), true)
			} else {
				a.setStatus("Wrote "+strings.Join(written, ", "), false)
			}
			return nil
		case "n", "esc", "q":
			a.full = nil
			return nil
		}
	}
	var cmd tea.Cmd
	d.vp, cmd = d.vp.Update(msg)
	return cmd
}

func (d *diffScreen) View(a *app, w, h int) string {
	return frameHead("╭", "Review changes", titleStyle, w) + "\n\n" + d.vp.View() + "\n" +
		dimStyle.Render("↑↓ scroll · y write · n back")
}

func (d *diffScreen) Help(a *app) (string, string) { return "", "" }
