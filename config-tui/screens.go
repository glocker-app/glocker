package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"gopkg.in/yaml.v3"

	"glocker/internal/config"
)

// ── formatting ───────────────────────────────────────────────────────────────

func fmtDays(days []string) string {
	j := strings.Join(days, ",")
	switch j {
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
	return fmt.Sprintf("%s %s–%s", fmtDays(w.Days), w.Start, w.End)
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

// ── form helpers ─────────────────────────────────────────────────────────────

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func validHHMM(s string) error {
	if !hhmm.MatchString(s) {
		return errors.New("use HH:MM, 24-hour")
	}
	return nil
}

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

func (a *app) textForm(title, initial string, validate func(string) error, save func(string)) tea.Cmd {
	v := initial
	return a.push(newForm(func() { save(strings.TrimSpace(v)) },
		huh.NewInput().Title(title).Value(&v).Validate(validate)))
}

func (a *app) windowForm(title string, w config.TimeWindow, save func(config.TimeWindow)) tea.Cmd {
	if w.Start == "" {
		w = config.TimeWindow{Start: "09:00", End: "17:00", Days: weekDays[:5]}
	}
	start, end, days := w.Start, w.End, slices.Clone(w.Days)
	return a.push(newForm(func() {
		slices.SortFunc(days, func(x, y string) int { return slices.Index(weekDays, x) - slices.Index(weekDays, y) })
		save(config.TimeWindow{Start: start, End: end, Days: days})
	},
		huh.NewNote().Title(title),
		huh.NewInput().Title("From").Value(&start).Validate(validHHMM),
		huh.NewInput().Title("To").Value(&end).Validate(validHHMM),
		huh.NewMultiSelect[string]().Title("Days").Options(huh.NewOptions(weekDays...)...).Value(&days).
			Validate(func(d []string) error {
				if len(d) == 0 {
					return errors.New("pick at least one day")
				}
				return nil
			}),
	))
}

// windowRows lists m[key]'s windows with edit/delete, plus an add row.
func (a *app) windowRows(m *yaml.Node, key, title, empty string) []row {
	var rows []row
	ws := windows(m, key)
	if len(ws) == 0 && empty != "" {
		rows = append(rows, note(empty))
	}
	for i, w := range ws {
		rows = append(rows, row{label: fmtWindow(w), act: func(k string) tea.Cmd {
			switch k {
			case "enter":
				return a.windowForm(title, w, func(nw config.TimeWindow) { setWindow(m, key, i, nw) })
			case "d", "delete", "backspace":
				delWindow(m, key, i)
			}
			return nil
		}})
	}
	rows = append(rows, row{label: "+ Add window", act: func(k string) tea.Cmd {
		if k == "enter" {
			return a.windowForm(title, config.TimeWindow{}, func(nw config.TimeWindow) { setWindow(m, key, -1, nw) })
		}
		return nil
	}})
	return rows
}

func toggleRow(label string, m *yaml.Node, key string) row {
	return row{label: label, value: onOff(getBool(m, key)), act: func(k string) tea.Cmd {
		if isToggle(k) {
			mapSet(m, key, boolNode(!getBool(m, key)))
		}
		return nil
	}}
}

// flagRow toggles an omitempty bool (absent = false).
func flagRow(label string, m *yaml.Node, key string) row {
	return row{label: label, value: yesNo(getBool(m, key)), act: func(k string) tea.Cmd {
		if isToggle(k) {
			setFlag(m, key, !getBool(m, key))
		}
		return nil
	}}
}

func (a *app) nameRow(m *yaml.Node, title string, validate func(string) error) row {
	return row{label: "Name", value: getStr(m, "name"), act: func(k string) tea.Cmd {
		if k != "enter" {
			return nil
		}
		return a.textForm(title, getStr(m, "name"), validate, func(v string) {
			if n := mapGet(m, "name"); n != nil {
				n.Value = v
			} else {
				mapSet(m, "name", strNode(v))
			}
		})
	}}
}

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

func (a *app) domainsTab() *listScreen {
	return &listScreen{build: func() []row {
		dom := ensure(a.doc.cfg(), "domains", yaml.MappingNode)
		rows := []row{toggleRow("Domain blocking", dom, "enabled"), header("Your domains")}
		for _, e := range a.doc.Domains() {
			rows = append(rows, row{label: getStr(e.Node, "name"), value: domainSummary(e.Node), act: func(k string) tea.Cmd {
				switch k {
				case "enter":
					return a.push(a.domainDetail(e))
				case "d", "delete", "backspace":
					e.Delete()
				}
				return nil
			}})
		}
		rows = append(rows, row{label: "+ Add domain", act: func(k string) tea.Cmd {
			if k != "enter" {
				return nil
			}
			return a.textForm("Domain", "", config.ValidateDomainName, func(v string) {
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
			rows = append(rows, row{label: box + " " + p.Name, value: commas(p.Count) + " domains", act: func(k string) tea.Cmd {
				if isToggle(k) {
					a.doc.TogglePack(p)
				}
				return nil
			}})
		}
		return rows
	}}
}

func (a *app) domainDetail(e DomainEntry) *listScreen {
	file := filepath.Base(e.File.Path)
	return &listScreen{nested: true, build: func() []row {
		rows := []row{
			a.nameRow(e.Node, "Domain", config.ValidateDomainName),
			flagRow("Unblockable", e.Node, "unblockable"),
			flagRow("Log blocking", e.Node, "log_blocking"),
			header("Blocked during"),
		}
		return append(rows, a.windowRows(e.Node, "block_windows", "Blocked during", "always")...)
	}, title: "Domain · " + file}
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
		s = "killed 24/7"
	}
	if getBool(m, "extendible") {
		s += " · extendible"
	}
	return s
}

func (a *app) programsTab() *listScreen {
	return &listScreen{build: func() []row {
		prog := ensure(a.doc.cfg(), "programs", yaml.MappingNode)
		list := ensure(prog, "list", yaml.SequenceNode)
		rows := []row{toggleRow("Program blocking", prog, "enabled"), header("Programs")}
		for _, n := range list.Content {
			rows = append(rows, row{label: getStr(n, "name"), value: programSummary(n), act: func(k string) tea.Cmd {
				switch k {
				case "enter":
					return a.push(a.programDetail(n))
				case "d", "delete", "backspace":
					if i := indexOf(list, n); i >= 0 {
						removeAt(list, i)
					}
				}
				return nil
			}})
		}
		return append(rows, row{label: "+ Add program", act: func(k string) tea.Cmd {
			if k != "enter" {
				return nil
			}
			return a.textForm("Program (process name)", "", config.ValidateProgramName, func(v string) {
				n := &yaml.Node{Kind: yaml.MappingNode}
				mapSet(n, "name", strNode(v))
				list.Style = 0
				list.Content = append(list.Content, n)
				a.push(a.programDetail(n))
			})
		}})
	}}
}

func (a *app) programDetail(n *yaml.Node) *listScreen {
	return &listScreen{nested: true, title: "Program", build: func() []row {
		rows := []row{
			a.nameRow(n, "Program (process name)", config.ValidateProgramName),
			flagRow("Extendible", n, "extendible"),
			header("Killed during"),
		}
		empty := ""
		if len(windows(n, "allow_windows")) == 0 {
			empty = "always"
		}
		rows = append(rows, a.windowRows(n, "kill_windows", "Killed during", empty)...)
		rows = append(rows, header("Allowed during"))
		if len(windows(n, "kill_windows")) > 0 && len(windows(n, "allow_windows")) > 0 {
			rows = append(rows, note("ignored while kill windows are set"))
		}
		return append(rows, a.windowRows(n, "allow_windows", "Allowed during", "")...)
	}}
}

// ── keywords ─────────────────────────────────────────────────────────────────

func (a *app) keywordsTab() *listScreen {
	sections := []struct{ key, title, prompt string }{
		{"url_keywords", "URL keywords", "Keyword matched against the address"},
		{"content_keywords", "Page keywords", "Keyword matched against page text"},
		{"whitelist", "Never scanned", "Site"},
	}
	return &listScreen{build: func() []row {
		kw := ensure(ensure(a.doc.cfg(), "content", yaml.MappingNode), "keywords", yaml.MappingNode)
		var rows []row
		for _, s := range sections {
			seq := ensure(kw, s.key, yaml.SequenceNode)
			rows = append(rows, header(s.title))
			for _, item := range seq.Content {
				rows = append(rows, row{label: item.Value, act: func(k string) tea.Cmd {
					switch k {
					case "enter":
						return a.textForm(s.prompt, item.Value, notEmpty, func(v string) { item.Value = v })
					case "d", "delete", "backspace":
						if i := indexOf(seq, item); i >= 0 {
							removeAt(seq, i)
						}
					}
					return nil
				}})
			}
			rows = append(rows, row{label: "+ Add", act: func(k string) tea.Cmd {
				if k != "enter" {
					return nil
				}
				return a.textForm(s.prompt, "", notEmpty, func(v string) { seq.Content = append(seq.Content, strNode(v)) })
			}})
		}
		return rows
	}}
}

// ── unblocking ───────────────────────────────────────────────────────────────

func (a *app) intRow(label, unit string, m *yaml.Node, key, zero string) row {
	v := getInt(m, key)
	shown := fmt.Sprintf("%d%s", v, unit)
	if v == 0 && zero != "" {
		shown = zero
	}
	return row{label: label, value: shown, act: func(k string) tea.Cmd {
		if k != "enter" {
			return nil
		}
		return a.textForm(label, strconv.Itoa(getInt(m, key)), validInt, func(s string) {
			n, _ := strconv.Atoi(s)
			mapSet(m, key, intNode(n))
		})
	}}
}

func (a *app) unblockingTab() *listScreen {
	return &listScreen{build: func() []row {
		u := ensure(ensure(a.doc.cfg(), "domains", yaml.MappingNode), "unblocking", yaml.MappingNode)
		reasons := seqStrings(mapGet(u, "reasons"))
		return []row{
			{label: "Reasons", value: strings.Join(reasons, ", "), act: func(k string) tea.Cmd {
				if k != "enter" {
					return nil
				}
				return a.textForm("Reasons (comma-separated)", strings.Join(reasons, ", "), notEmpty, func(v string) {
					var out []string
					for _, r := range strings.Split(v, ",") {
						if r = strings.TrimSpace(r); r != "" {
							out = append(out, r)
						}
					}
					mapSet(u, "reasons", strSeq(out))
				})
			}},
			a.intRow("Unblock length", " min", u, "temp_unblock_time", ""),
			a.intRow("Max per day", "", u, "max_per_day", "unlimited"),
		}
	}}
}

// ── sudo ─────────────────────────────────────────────────────────────────────

func (a *app) sudoTab() *listScreen {
	return &listScreen{build: func() []row {
		s := ensure(a.doc.cfg(), "sudoers", yaml.MappingNode)
		rows := []row{toggleRow("Sudo control", s, "enabled"), header("Sudo allowed during")}
		return append(rows, a.windowRows(s, "allow_windows", "Sudo allowed during", "never")...)
	}}
}

// ── general ──────────────────────────────────────────────────────────────────

func (a *app) generalTab() *listScreen {
	return &listScreen{build: func() []row {
		c := a.doc.cfg()
		return []row{{label: "User", value: getStr(c, "user"), act: func(k string) tea.Cmd {
			if k != "enter" {
				return nil
			}
			return a.textForm("Linux login name", getStr(c, "user"), notEmpty, func(v string) { mapSet(c, "user", strNode(v)) })
		}}}
	}}
}
