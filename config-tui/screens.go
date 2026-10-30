package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// scheduleRow is one row summarising a schedule; enter opens the grid.
func (a *app) scheduleRow(label, value, help string, open func() *scheduleScreen) row {
	return row{label: label, value: value, help: help, keys: "enter edit schedule", act: func(k string) tea.Cmd {
		if k == "enter" {
			return a.push(open())
		}
		return nil
	}}
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
		ws := windows(n, "block_windows")
		blocked := "always"
		if len(ws) > 0 {
			blocked = fmtWindows(ws)
		}
		return []row{
			a.nameRow(n, "Domain", "In "+filepath.Base(e.File.Path)+".", config.ValidateDomainName),
			flagRow("Unblockable", "Allow temporary passes with `glocker -unblock`.", n, "unblockable"),
			flagRow("Log blocking", "Log every blocked visit to this domain.", n, "log_blocking"),
			a.scheduleRow("Blocked", blocked, "When this domain is blocked.", func() *scheduleScreen {
				w := weekFromWindows(ws)
				if len(ws) == 0 {
					w = weekOf(func(time.Time) bool { return true })
				}
				return newSchedule(getStr(n, "name")+" · blocked", "blocked", "open", w, func(w week) string {
					switch {
					case w.empty():
						return "Nothing is blocked. Delete the domain instead."
					case w.full():
						mapDel(n, "block_windows")
					default:
						setWindows(n, "block_windows", w.windows())
					}
					return ""
				})
			}),
		}
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

// programDetail shows when the program is killed as one schedule. The config
// can say this with kill_windows or allow_windows; the grid shows the result
// and saves as kill_windows.
func (a *app) programDetail(n *yaml.Node) *listScreen {
	return &listScreen{title: "Program", build: func() []row {
		killed := programKilled(windows(n, "kill_windows"), windows(n, "allow_windows"))
		value := "always"
		if !killed.full() {
			value = fmtWindows(killed.windows())
		}
		return []row{
			a.nameRow(n, "Program", "Process name.", config.ValidateProgramName),
			flagRow("Extendible", "Allow a one-hour `glocker -extend`, once per 24h.", n, "extendible"),
			a.scheduleRow("Killed", value, "When this program is killed on sight.", func() *scheduleScreen {
				return newSchedule(getStr(n, "name")+" · killed", "killed", "allowed", killed, func(w week) string {
					switch {
					case w.empty():
						return "Never killed. Delete the program instead."
					case w.full():
						mapDel(n, "kill_windows")
					default:
						setWindows(n, "kill_windows", w.windows())
					}
					mapDel(n, "allow_windows")
					return ""
				})
			}),
		}
	}}
}
