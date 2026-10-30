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

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	tabStyle    = lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("8"))
	activeTab   = lipgloss.NewStyle().Padding(0, 1).Bold(true).Reverse(true)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	addStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	delStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	hunkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

type screen interface {
	Update(a *app, msg tea.Msg) tea.Cmd
	View(a *app) string
}

type app struct {
	doc       *Doc
	suffix    string
	tabNames  []string
	tabs      []*listScreen
	tab       int
	stack     []screen
	status    string
	statusErr bool
	quitArmed bool
	w, h      int
}

func newApp(d *Doc, suffix string) *app {
	a := &app{doc: d, suffix: suffix, w: 80, h: 24}
	a.tabNames = []string{"Domains", "Programs", "Keywords", "Unblocking", "Sudo", "General"}
	a.tabs = []*listScreen{a.domainsTab(), a.programsTab(), a.keywordsTab(), a.unblockingTab(), a.sudoTab(), a.generalTab()}
	return a
}

func (a *app) Init() tea.Cmd { return nil }

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		if top := a.top(); top != nil {
			return a, top.Update(a, msg)
		}
		return a, nil
	case tea.KeyMsg:
		if msg.String() != "q" && msg.String() != "ctrl+c" {
			a.quitArmed = false
		}
		if top := a.top(); top != nil {
			return a, top.Update(a, msg)
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return a, a.quit()
		case "tab", "right":
			a.tab = (a.tab + 1) % len(a.tabs)
			return a, nil
		case "shift+tab", "left":
			a.tab = (a.tab + len(a.tabs) - 1) % len(a.tabs)
			return a, nil
		case "1", "2", "3", "4", "5", "6":
			a.tab = int(msg.String()[0] - '1')
			return a, nil
		}
		return a, a.tabs[a.tab].Update(a, msg)
	}
	if top := a.top(); top != nil {
		return a, top.Update(a, msg)
	}
	return a, nil
}

func (a *app) View() string {
	var b strings.Builder
	if top := a.top(); top != nil {
		b.WriteString(top.View(a))
	} else {
		var tabs []string
		for i, n := range a.tabNames {
			s := tabStyle
			if i == a.tab {
				s = activeTab
			}
			tabs = append(tabs, s.Render(fmt.Sprintf("%d %s", i+1, n)))
		}
		b.WriteString(strings.Join(tabs, "") + "\n\n")
		b.WriteString(a.tabs[a.tab].View(a))
	}
	return b.String()
}

func (a *app) top() screen {
	if len(a.stack) == 0 {
		return nil
	}
	return a.stack[len(a.stack)-1]
}

func (a *app) push(s screen) tea.Cmd {
	a.stack = append(a.stack, s)
	if f, ok := s.(*formScreen); ok {
		f.form = f.form.WithWidth(min(a.w, 60))
		return f.form.Init()
	}
	return nil
}

func (a *app) pop() {
	if len(a.stack) > 0 {
		a.stack = a.stack[:len(a.stack)-1]
	}
}

func (a *app) setStatus(s string, isErr bool) { a.status, a.statusErr = s, isErr }

func (a *app) statusLine() string {
	if a.status == "" {
		return ""
	}
	if a.statusErr {
		return errStyle.Render(a.status)
	}
	return okStyle.Render(a.status)
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
	return a.push(newDiffScreen(a, ch))
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
	act   func(key string) tea.Cmd // nil: not actionable
}

func header(s string) row { return row{kind: rowHeader, label: s} }
func note(s string) row   { return row{kind: rowNote, label: s} }

type listScreen struct {
	title  string
	build  func() []row
	cursor int
	offset int
	nested bool // pushed on the stack: esc goes back
}

func (l *listScreen) selectable(rows []row, i int) bool {
	return i >= 0 && i < len(rows) && rows[i].kind == rowItem
}

func (l *listScreen) clamp(rows []row) {
	if l.selectable(rows, l.cursor) {
		return
	}
	for d := 1; d < len(rows); d++ {
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
	case "esc":
		if l.nested {
			a.pop()
		}
	case "q", "ctrl+c":
		if l.nested {
			a.pop()
			return nil
		}
		return a.quit()
	case "s":
		return a.save()
	default:
		if l.selectable(rows, l.cursor) && rows[l.cursor].act != nil {
			a.setStatus("", false)
			cmd := rows[l.cursor].act(k)
			l.clamp(l.build())
			return cmd
		}
	}
	return nil
}

func (l *listScreen) View(a *app) string {
	rows := l.build()
	l.clamp(rows)
	var b strings.Builder
	if l.title != "" {
		b.WriteString(titleStyle.Render(l.title) + "\n\n")
	}
	labelW := 0
	for _, r := range rows {
		if r.kind == rowItem && r.value != "" {
			labelW = max(labelW, lipgloss.Width(r.label))
		}
	}
	// Keep the cursor on screen.
	avail := max(a.h-8, 5)
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+avail {
		l.offset = l.cursor - avail + 1
	}
	if l.offset > 0 && l.offset < len(rows) && rows[l.offset-1].kind == rowHeader {
		l.offset--
	}
	end := min(len(rows), l.offset+avail)
	for i := l.offset; i < end; i++ {
		r := rows[i]
		switch r.kind {
		case rowHeader:
			if i > l.offset {
				b.WriteString("\n")
			}
			b.WriteString(headerStyle.Render(r.label) + "\n")
		case rowNote:
			b.WriteString("    " + dimStyle.Render(r.label) + "\n")
		default:
			mark := "  "
			label := r.label
			if i == l.cursor {
				mark = cursorStyle.Render("› ")
				label = cursorStyle.Render(label)
			}
			line := mark + label
			if r.value != "" {
				pad := labelW - lipgloss.Width(r.label) + 2
				line += strings.Repeat(" ", pad) + dimStyle.Render(r.value)
			}
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString("\n" + a.statusLine() + "\n")
	hint := "↑↓ move · enter edit · space toggle · d delete · s save · "
	if l.nested {
		hint += "esc back"
	} else {
		hint += "tab switch · q quit"
	}
	b.WriteString(dimStyle.Render(hint))
	return b.String()
}

// ── forms ────────────────────────────────────────────────────────────────────

type formScreen struct {
	form *huh.Form
	done func()
}

func newForm(done func(), fields ...huh.Field) *formScreen {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "cancel"))
	f := huh.NewForm(huh.NewGroup(fields...)).WithKeyMap(km).WithShowHelp(true)
	return &formScreen{form: f, done: done}
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

func (f *formScreen) View(a *app) string { return f.form.View() }

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
	vp := viewport.New(a.w, max(a.h-4, 5))
	vp.SetContent(b.String())
	return &diffScreen{changes: ch, vp: vp}
}

func (d *diffScreen) Update(a *app, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.vp.Width, d.vp.Height = msg.Width, max(msg.Height-4, 5)
	case tea.KeyMsg:
		switch msg.String() {
		case "y":
			written, err := a.doc.Write(d.changes, a.suffix)
			a.pop()
			if err != nil {
				a.setStatus("Write failed: "+err.Error(), true)
			} else {
				a.setStatus("Wrote "+strings.Join(written, ", "), false)
			}
			return nil
		case "n", "esc", "q":
			a.pop()
			return nil
		}
	}
	var cmd tea.Cmd
	d.vp, cmd = d.vp.Update(msg)
	return cmd
}

func (d *diffScreen) View(a *app) string {
	return titleStyle.Render("Review changes") + "\n\n" + d.vp.View() + "\n" +
		dimStyle.Render("↑↓ scroll · y write · n back")
}
