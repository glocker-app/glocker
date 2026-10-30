package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"gopkg.in/yaml.v3"

	"glocker/internal/config"
)

// ── formatting ───────────────────────────────────────────────────────────────

func fmtDays(days []string) string {
	switch strings.Join(days, ",") {
	case "Mon,Tue,Wed,Thu,Fri":
		return "Weekdays"
	case "Sat,Sun":
		return "Weekends"
	case "Mon,Tue,Wed,Thu,Fri,Sat,Sun":
		return "Every day"
	}
	return strings.Join(days, " ")
}

func fmtWindow(w config.TimeWindow) string {
	s := fmt.Sprintf("%s %s–%s", fmtDays(w.Days), w.Start, w.End)
	if w.End < w.Start {
		s += " (overnight)"
	}
	return s
}

func fmtWindows(ws []config.TimeWindow) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = fmtWindow(w)
	}
	return strings.Join(parts, ", ")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func isToggle(k string) bool { return k == "enter" || k == " " }

func isDelete(k string) bool { return k == "d" || k == "delete" || k == "backspace" }

// ── forms ────────────────────────────────────────────────────────────────────

func validInt(s string) error {
	if n, err := strconv.Atoi(s); err != nil || n < 0 {
		return errors.New("enter a whole number")
	}
	return nil
}

func notEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("required")
	}
	return nil
}

func (a *app) textForm(title, initial, help string, validate func(string) error, save func(string)) tea.Cmd {
	if validate == nil {
		validate = func(string) error { return nil }
	}
	v := initial
	return a.push(newForm(help, func() { save(strings.TrimSpace(v)) },
		huh.NewInput().Title(title).Value(&v).Validate(validate)))
}

// windowRows lists the windows under get()[leaf] with edit/delete, plus an add
// row. get reads without creating; mk creates the parent for writes.
func (a *app) windowRows(get, mk func() *yaml.Node, leaf, title, empty, help string) []row {
	var rows []row
	ws := windows(get(), leaf)
	if len(ws) == 0 && empty != "" {
		rows = append(rows, note(empty))
	}
	for i, w := range ws {
		others := append(append([]config.TimeWindow{}, ws[:i]...), ws[i+1:]...)
		rows = append(rows, row{label: fmtWindow(w), help: help, keys: "enter edit · d delete", act: func(k string) tea.Cmd {
			switch {
			case k == "enter":
				return a.push(newPicker(title, w, others, func(nw config.TimeWindow) { setWindow(mk(), leaf, i, nw) }))
			case isDelete(k):
				delWindow(mk(), leaf, i)
			}
			return nil
		}})
	}
	return append(rows, row{label: "+ Add window", help: help, keys: "enter add", act: func(k string) tea.Cmd {
		if k == "enter" {
			return a.push(newPicker(title, config.TimeWindow{}, ws, func(nw config.TimeWindow) { setWindow(mk(), leaf, -1, nw) }))
		}
		return nil
	}})
}

func flagRow(label, help string, m *yaml.Node, key string) row {
	return row{label: label, value: yesNo(getBool(m, key)), help: help, keys: "space toggle", act: func(k string) tea.Cmd {
		if isToggle(k) {
			setFlag(m, key, !getBool(m, key))
		}
		return nil
	}}
}

func (a *app) nameRow(m *yaml.Node, title, help string, validate func(string) error) row {
	return row{label: "Name", value: getStr(m, "name"), help: help, keys: "enter edit", act: func(k string) tea.Cmd {
		if k != "enter" {
			return nil
		}
		return a.textForm(title, getStr(m, "name"), help, validate, func(v string) {
			if n := mapGet(m, "name"); n != nil {
				n.Value = v
			} else {
				mapSet(m, "name", strNode(v))
			}
		})
	}}
}

func self(m *yaml.Node) func() *yaml.Node { return func() *yaml.Node { return m } }

// ── domains ──────────────────────────────────────────────────────────────────

func domainSummary(m *yaml.Node) string {
	s := "always"
	if ws := windows(m, "block_windows"); len(ws) > 0 {
		s = fmtWindows(ws)
	}
	if getBool(m, "unblockable") {
		s += " · unblockable"
	}
	return s
}

func (a *app) domainRows() []row {
	rows := []row{header("Your domains")}
	for _, e := range a.doc.Domains() {
		rows = append(rows, row{label: getStr(e.Node, "name"), value: domainSummary(e.Node),
			help: "In " + filepath.Base(e.File.Path) + ".", keys: "enter open · d delete",
			act: func(k string) tea.Cmd {
				switch {
				case k == "enter":
					return a.push(a.domainDetail(e))
				case isDelete(k):
					e.Delete()
				}
				return nil
			}})
	}
	rows = append(rows, row{label: "+ Add domain", help: "Subdomains are blocked too.", keys: "enter add", act: func(k string) tea.Cmd {
		if k != "enter" {
			return nil
		}
		return a.textForm("Domain", "", "e.g. youtube.com", config.ValidateDomainName, func(v string) {
			a.push(a.domainDetail(a.doc.AddDomain(v)))
		})
	}})
	if len(a.doc.Packs) > 0 {
		rows = append(rows, header("Blocklists"))
	}
	for _, p := range a.doc.Packs {
		box := "[ ]"
		if p.Enabled {
			box = "[x]"
		}
		rows = append(rows, row{label: box + " " + p.Name, value: commas(p.Count) + " domains",
			help: "Generated list in " + p.Rel + ", blocked 24/7.", keys: "space toggle",
			act: func(k string) tea.Cmd {
				if isToggle(k) {
					a.doc.TogglePack(p)
				}
				return nil
			}})
	}
	return rows
}

func (a *app) domainDetail(e DomainEntry) *listScreen {
	return &listScreen{title: "Domain", build: func() []row {
		n := e.Node
		blocked := "always"
		if len(windows(n, "block_windows")) > 0 {
			blocked = "during the windows below"
		}
		rows := []row{
			a.nameRow(n, "Domain", "In "+filepath.Base(e.File.Path)+".", config.ValidateDomainName),
			flagRow("Unblockable", "Allow temporary passes with `glocker -unblock`.", n, "unblockable"),
			flagRow("Log blocking", "Log every blocked visit to this domain.", n, "log_blocking"),
			{kind: rowNote, label: "Blocked " + blocked},
			header("Windows"),
		}
		return append(rows, a.windowRows(self(n), self(n), "block_windows", "Blocked during", "",
			"Blocked only during these windows. No windows means always blocked.")...)
	}}
}

// ── programs ─────────────────────────────────────────────────────────────────

func programSummary(m *yaml.Node) string {
	var s string
	switch kw, aw := windows(m, "kill_windows"), windows(m, "allow_windows"); {
	case len(kw) > 0:
		s = "killed " + fmtWindows(kw)
	case len(aw) > 0:
		s = "allowed " + fmtWindows(aw)
	default:
		s = "killed always"
	}
	if getBool(m, "extendible") {
		s += " · extendible"
	}
	return s
}

func (a *app) programRows() []row {
	rows := []row{header("Programs")}
	list := path(a.doc.cfg(), "programs", "list")
	if list != nil {
		for _, n := range list.Content {
			rows = append(rows, row{label: getStr(n, "name"), value: programSummary(n), keys: "enter open · d delete",
				act: func(k string) tea.Cmd {
					switch {
					case k == "enter":
						return a.push(a.programDetail(n))
					case isDelete(k):
						if i := indexOf(list, n); i >= 0 {
							removeAt(list, i)
						}
					}
					return nil
				}})
		}
	}
	return append(rows, row{label: "+ Add program", help: "Matched against the process name.", keys: "enter add", act: func(k string) tea.Cmd {
		if k != "enter" {
			return nil
		}
		return a.textForm("Program", "", "Process name, e.g. steam.", config.ValidateProgramName, func(v string) {
			n := &yaml.Node{Kind: yaml.MappingNode}
			mapSet(n, "name", strNode(v))
			l := ensure(ensurePath(a.doc.cfg(), "programs"), "list", yaml.SequenceNode)
			l.Style = 0
			l.Content = append(l.Content, n)
			a.push(a.programDetail(n))
		})
	}})
}

// programDetail shows one list of windows plus a mode, rather than the
// config's separate kill_windows/allow_windows: with no windows the program is
// always killed; with windows, space flips between killed during them and
// killed outside them (moving the list between the two keys).
func (a *app) programDetail(n *yaml.Node) *listScreen {
	return &listScreen{title: "Program", build: func() []row {
		kw, aw := windows(n, "kill_windows"), windows(n, "allow_windows")
		rows := []row{
			a.nameRow(n, "Program", "Process name.", config.ValidateProgramName),
			flagRow("Extendible", "Allow a one-hour `glocker -extend`, once per 24h.", n, "extendible"),
		}
		modeHelp := "Space flips between killed during and killed outside the windows."
		switch {
		case len(kw) > 0 && len(aw) > 0:
			rows = append(rows, note("Has both kill and allow windows; allow windows are ignored."),
				header("Killed during"))
			rows = append(rows, a.windowRows(self(n), self(n), "kill_windows", "Killed during", "", "")...)
			rows = append(rows, header("Allowed during (ignored)"))
			return append(rows, a.windowRows(self(n), self(n), "allow_windows", "Allowed during", "", "")...)
		case len(kw) > 0:
			rows = append(rows, row{label: "Killed", value: "during the windows below", help: modeHelp, keys: "space flip",
				act: func(k string) tea.Cmd {
					if isToggle(k) {
						moveKey(n, "kill_windows", "allow_windows")
					}
					return nil
				}}, header("Windows"))
			return append(rows, a.windowRows(self(n), self(n), "kill_windows", "Killed during", "", modeHelp)...)
		case len(aw) > 0:
			rows = append(rows, row{label: "Killed", value: "outside the windows below", help: modeHelp, keys: "space flip",
				act: func(k string) tea.Cmd {
					if isToggle(k) {
						moveKey(n, "allow_windows", "kill_windows")
					}
					return nil
				}}, header("Windows"))
			return append(rows, a.windowRows(self(n), self(n), "allow_windows", "Allowed during", "", modeHelp)...)
		default:
			rows = append(rows, note("Killed always"), header("Windows"))
			return append(rows, a.windowRows(self(n), self(n), "kill_windows", "Killed during", "",
				"Add a window to kill it only at certain times.")...)
		}
	}}
}

// moveKey renames a mapping key, keeping its value node (and comments).
func moveKey(m *yaml.Node, from, to string) {
	v := mapGet(m, from)
	if v == nil {
		return
	}
	mapDel(m, from)
	mapDel(m, to)
	mapSet(m, to, v)
}
