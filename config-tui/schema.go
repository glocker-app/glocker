package main

// The sections and fields the editor shows. Each field maps to a config key
// under its section's path; a dotted key ("monitoring.enabled") reaches into a
// nested mapping.

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"glocker/internal/config"
)

type kind int

const (
	kBool    kind = iota // true/false, always written
	kFlag                // omitempty bool: false removes the key
	kInt                 // whole number
	kStr                 // free text
	kSecret              // free text, masked
	kList                // list of strings
	kWindows             // list of time windows
	kEnum                // one of opts
	kTri                 // unset / true / false
	kHeader              // a sub-heading, no key
	kCustom              // rows from custom
)

type field struct {
	key, label, help string
	kind             kind
	unit             string // shown after an int
	zero             string // shown instead of 0 / an empty value
	opts             []string
	validate         func(string) error
	custom           func(a *app) []row
}

type section struct {
	title, help string
	path        []string
	fields      []field
	state       func(m *yaml.Node) string // "on", "off" or "" for the sidebar dot
}

func enabledState(m *yaml.Node) string {
	if mapGet(m, "enabled") == nil {
		return ""
	}
	return onOff(getBool(m, "enabled"))
}

func dflt(n int) string { return fmt.Sprintf("default (%d)", n) }

func sections() []*section {
	return []*section{
		{title: "General", help: "Machine-wide settings.", fields: []field{
			{key: "user", label: "User", kind: kStr, validate: notEmpty,
				help: "Your Linux login. Its sudo is restricted and its desktop is tracked."},
			{key: "log_level", label: "Log level", kind: kEnum, opts: []string{"debug", "info", "warn", "error"},
				help: "How much the daemon writes to the journal."},
		}},
		{title: "Domains", help: "Websites blocked through /etc/hosts.", path: []string{"domains"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Domain blocking", kind: kBool, help: "Master switch for website blocking."},
			{kind: kCustom, custom: (*app).domainRows},
			{kind: kHeader, label: "Advanced"},
			{key: "kill_on_block", label: "Restart on block", kind: kList, zero: "none",
				help: "Browsers killed on `glocker -block` so they drop cached DNS."},
			{key: "blocked_access_command", label: "On blocked visit", kind: kStr, zero: "nothing",
				help: "Command run when a blocked site is visited."},
			{key: "hosts_path", label: "Hosts file", kind: kStr, help: "Leave as /etc/hosts unless you know why not."},
		}},
		{title: "Unblocking", help: "Temporary passes with `glocker -unblock domain:reason`, for domains marked unblockable.", path: []string{"domains", "unblocking"}, fields: []field{
			{key: "reasons", label: "Reasons", kind: kList, help: "An unblock must give one of these reasons."},
			{key: "temp_unblock_time", label: "Pass length", kind: kInt, unit: " min", help: "How long an unblocked domain stays open."},
			{key: "max_per_day", label: "Passes per day", kind: kInt, zero: "unlimited", help: "Cap on unblocks per calendar day. 0 is unlimited."},
			{key: "log_file", label: "Log file", kind: kStr},
		}},
		{title: "Programs", help: "Programs killed by process name.", path: []string{"programs"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Program blocking", kind: kBool, help: "Master switch for killing programs."},
			{key: "check_interval_seconds", label: "Check every", kind: kInt, unit: " s", help: "How often running processes are checked."},
			{kind: kCustom, custom: (*app).programRows},
		}},
		{title: "Content", help: "Keyword blocking in the browser extension.", path: []string{"content"}, state: func(m *yaml.Node) string {
			return enabledState(mapGet(m, "monitoring"))
		}, fields: []field{
			{key: "monitoring.enabled", label: "Report violations", kind: kBool, help: "Log keyword hits reported by the browser extension."},
			{key: "keywords.url_keywords", label: "URL keywords", kind: kList, zero: "none", help: "Pages whose address contains one of these are blocked."},
			{key: "keywords.content_keywords", label: "Page keywords", kind: kList, zero: "none", help: "Pages whose text contains one of these are blocked."},
			{key: "keywords.whitelist", label: "Never scanned", kind: kList, zero: "none", help: "Sites the extension never scans or blocks."},
			{key: "monitoring.log_file", label: "Log file", kind: kStr},
		}},
		{title: "Sudo", help: "Sudo is only available inside the allowed windows. This is what makes the rest hard to undo.", path: []string{"sudoers"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Sudo control", kind: kBool, help: "Restrict sudo to the windows below."},
			{key: "allow_windows", label: "Sudo allowed", kind: kWindows, zero: "never", opts: []string{"sudo allowed", "sudo blocked"},
				help: "Outside these times only the restricted sudo line applies."},
			{kind: kHeader, label: "Advanced"},
			{key: "allowed_sudoers_line", label: "Allowed line", kind: kStr, validate: notEmpty, help: "sudoers line in effect inside the windows."},
			{key: "blocked_sudoers_line", label: "Restricted line", kind: kStr, validate: notEmpty, help: "sudoers line in effect outside the windows."},
		}},
		{title: "Mindful uninstall", help: "`glocker -uninstall` first needs a sentence typed as it is revealed.", path: []string{"mindful_uninstall"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Typing challenge", kind: kBool, help: "Require the challenge before uninstalling."},
			{kind: kHeader, label: "Tuning"},
			{key: "interval_ms", label: "Reveal every", kind: kInt, unit: " ms", zero: dflt(config.DefaultMindfulIntervalMs), help: "Time between revealed characters."},
			{key: "deadline_ms", label: "Miss after", kind: kInt, unit: " ms", zero: dflt(config.DefaultMindfulDeadlineMs), help: "A character not typed within this resets the challenge."},
			{key: "grace_ms", label: "Start delay", kind: kInt, unit: " ms", zero: dflt(config.DefaultMindfulGraceMs), help: "Pause before the first character."},
			{key: "lines", label: "Sentences", kind: kInt, zero: dflt(config.DefaultMindfulLines), help: "Sentences to type on a normal uninstall."},
			{key: "recency_hours", label: "Repeat window", kind: kInt, unit: " h", zero: dflt(config.DefaultMindfulRecencyHours), help: "An uninstall within this many hours of the last counts as a repeat."},
			{key: "recent_lines", label: "Sentences on repeat", kind: kInt, zero: dflt(config.DefaultMindfulRecentLines), help: "Sentences to type on a repeat uninstall."},
			{key: "sentences", label: "Custom sentences", kind: kList, zero: "built-in", help: "Your own sentence pool. Empty uses the built-in one."},
		}},
		{title: "Lifecycle", help: "Install and uninstall records.", path: []string{"lifecycle"}, fields: []field{
			{key: "reasons", label: "Uninstall reasons", kind: kList, help: "`glocker -uninstall` must give one of these."},
			{key: "log_file", label: "Log file", kind: kStr},
		}},
		{title: "Violations", help: "Too many blocked attempts in a short time runs a command, usually a screen lock.", path: []string{"violation_tracking"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Violation tracking", kind: kBool},
			{key: "max_violations", label: "Threshold", kind: kInt, help: "Violations that trigger the command."},
			{key: "time_window_minutes", label: "Within", kind: kInt, unit: " min", help: "Window the threshold is counted over."},
			{key: "command", label: "Command", kind: kStr, help: "Run when the threshold is crossed."},
			{key: "lock_duration", label: "Lock for", kind: kStr, help: "Screen lock length, e.g. 5m."},
			{key: "background", label: "Lock background", kind: kStr, zero: "default", help: "PNG/JPG for the lock screen (X11 only)."},
		}},
		{title: "Usage monitor", help: "Records the focused window and idle time for the dashboard.", path: []string{"usage_monitor"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Usage tracking", kind: kBool},
			{key: "interval_seconds", label: "Sample every", kind: kInt, unit: " s", zero: dflt(config.DefaultUsageInterval)},
			{key: "log_file", label: "Log file", kind: kStr, zero: config.DefaultUsageLogFile},
			{kind: kHeader, label: "Session access"},
			{key: "display", label: "X display", kind: kStr, zero: "$DISPLAY", help: "X display to sample, e.g. :0."},
			{key: "xauthority", label: "Xauthority", kind: kStr, zero: "none", help: "Path to your X authority cookie."},
			{key: "dbus_address", label: "D-Bus address", kind: kStr, zero: "derived from user", help: "Session bus for Wayland. Empty derives it from the user."},
		}},
		{title: "Heartbeat", help: "Watchdog that records whether glocker is running. Gaps show up on the dashboard.", path: []string{"heartbeat"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Heartbeat", kind: kBool},
			{key: "interval_minutes", label: "Check every", kind: kInt, unit: " min", zero: dflt(config.DefaultHeartbeatIntervalMinutes)},
			{key: "timeout_seconds", label: "Timeout", kind: kInt, unit: " s", zero: dflt(config.DefaultHeartbeatTimeoutSeconds)},
			{key: "log_file", label: "Log file", kind: kStr, zero: config.DefaultHeartbeatLogFile},
		}},
		{title: "Sync", help: "Sends records to a glockpeek dashboard.", path: []string{"sync"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Sync", kind: kBool},
			{key: "glockpeek_url", label: "Dashboard URL", kind: kStr, zero: config.DefaultGlockpeekURL},
			{key: "token", label: "Token", kind: kSecret, zero: "none", help: "Needed for a hosted dashboard only."},
			{key: "encrypt", label: "Encrypt", kind: kTri, help: "Required when sync is on. Must be yes for a hosted dashboard."},
			{key: "interval_seconds", label: "Sync every", kind: kInt, unit: " s", zero: dflt(config.DefaultSyncIntervalSeconds)},
			{key: "encrypt_key_file", label: "Key file", kind: kStr, zero: config.DefaultEncryptKeyFile},
		}},
		{title: "Accountability", help: "Emails to a buddy, sent by the dashboard. They contain counts, never what you visited.", path: []string{"accountability"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Buddy emails", kind: kBool},
			{key: "buddy_emails", label: "Send to", kind: kList, zero: "nobody"},
			{key: "digest_every_hours", label: "Digest every", kind: kInt, unit: " h", zero: "off"},
			{key: "digest_when_quiet", label: "Digest when quiet", kind: kBool, help: "Send the digest even when nothing happened."},
			{kind: kHeader, label: "Alerts"},
			{key: "alerts.went_dark", label: "Went dark", kind: kBool, help: "Email when glocker stops reporting."},
			{key: "alerts.high_violations", label: "Violations over", kind: kInt, zero: "off", help: "Email when a digest window has this many violations."},
		}},
		{title: "Panic", help: "`glocker -panic` suspends the machine and re-suspends it on early wake.", path: []string{"panic"}, state: enabledState, fields: []field{
			{key: "enabled", label: "Panic mode", kind: kBool},
			{key: "duration", label: "Suspend for", kind: kInt, unit: " min", zero: dflt(30)},
			{key: "command", label: "Command", kind: kStr, help: "Command that suspends the machine."},
		}},
	}
}

// secNode reads a section's mapping without creating it.
func (a *app) secNode(s *section) *yaml.Node {
	return path(a.doc.cfg(), s.path...)
}

func (a *app) sectionScreen(s *section) *listScreen {
	return &listScreen{title: s.title, build: func() []row {
		var rows []row
		for _, f := range s.fields {
			rows = append(rows, a.fieldRows(s, f)...)
		}
		return rows
	}}
}

// fieldRows renders one field. Reads never create nodes; writes create the
// path on demand.
func (a *app) fieldRows(s *section, f field) []row {
	switch f.kind {
	case kHeader:
		return []row{header(f.label)}
	case kCustom:
		return f.custom(a)
	}
	keys := strings.Split(f.key, ".")
	leaf := keys[len(keys)-1]
	parentPath := append(append([]string{}, s.path...), keys[:len(keys)-1]...)
	get := func() *yaml.Node { return path(a.doc.cfg(), parentPath...) }
	mk := func() *yaml.Node { return ensurePath(a.doc.cfg(), parentPath...) }
	m := get()
	r := row{label: f.label, help: f.help}

	switch f.kind {
	case kBool:
		r.value, r.keys = onOff(getBool(m, leaf)), "space toggle"
		r.act = func(k string) tea.Cmd {
			if isToggle(k) {
				mapSet(mk(), leaf, boolNode(!getBool(get(), leaf)))
			}
			return nil
		}
	case kFlag:
		r.value, r.keys = yesNo(getBool(m, leaf)), "space toggle"
		r.act = func(k string) tea.Cmd {
			if isToggle(k) {
				setFlag(mk(), leaf, !getBool(get(), leaf))
			}
			return nil
		}
	case kEnum:
		cur := getStr(m, leaf)
		r.value, r.keys = orZero(cur, "default"), "space next option"
		r.act = func(k string) tea.Cmd {
			if isToggle(k) {
				mapSet(mk(), leaf, strNode(nextOpt(f.opts, cur)))
			}
			return nil
		}
	case kTri:
		r.keys = "space cycle unset/yes/no"
		switch n := mapGet(m, leaf); {
		case n == nil:
			r.value = "unset"
		default:
			r.value = yesNo(getBool(m, leaf))
		}
		r.act = func(k string) tea.Cmd {
			if !isToggle(k) {
				return nil
			}
			switch r.value {
			case "unset":
				mapSet(mk(), leaf, boolNode(true))
			case "yes":
				mapSet(mk(), leaf, boolNode(false))
			default:
				mapDel(mk(), leaf)
			}
			return nil
		}
	case kInt:
		v := getInt(m, leaf)
		r.value = strconv.Itoa(v) + f.unit
		if v == 0 && f.zero != "" {
			r.value = f.zero
		}
		r.keys = "enter edit"
		r.act = func(k string) tea.Cmd {
			if k != "enter" {
				return nil
			}
			return a.textForm(f.label, strconv.Itoa(getInt(get(), leaf)), f.help, validInt, func(s string) {
				n, _ := strconv.Atoi(s)
				mapSet(mk(), leaf, intNode(n))
			})
		}
	case kStr, kSecret:
		cur := getStr(m, leaf)
		r.value = orZero(cur, orZero(f.zero, "—"))
		if f.kind == kSecret && cur != "" {
			r.value = "••••••"
		}
		r.keys = "enter edit"
		r.act = func(k string) tea.Cmd {
			if k != "enter" {
				return nil
			}
			return a.textForm(f.label, getStr(get(), leaf), f.help, f.validate, func(s string) {
				mapSet(mk(), leaf, strNode(s))
			})
		}
	case kList:
		items := seqStrings(mapGet(m, leaf))
		r.value = orZero(strings.Join(items, ", "), orZero(f.zero, "none"))
		r.keys, r.nav = "enter edit list", true
		r.act = func(k string) tea.Cmd {
			if k != "enter" {
				return nil
			}
			return a.push(a.listEditor(f, get, mk, leaf))
		}
	case kWindows:
		ws := windows(m, leaf)
		r = a.scheduleRow(f.label, orZero(fmtWindows(ws), f.zero), f.help, func() *scheduleScreen {
			return newSchedule(f.label, f.opts[0], f.opts[1], weekFromWindows(ws), func(w week) string {
				setWindows(mk(), leaf, w.windows())
				return ""
			})
		})
	}
	return []row{r}
}

func orZero(s, zero string) string {
	if s == "" {
		return zero
	}
	return s
}

func nextOpt(opts []string, cur string) string {
	for i, o := range opts {
		if o == cur {
			return opts[(i+1)%len(opts)]
		}
	}
	return opts[0]
}

// listEditor edits a list of strings in its own screen.
func (a *app) listEditor(f field, get, mk func() *yaml.Node, leaf string) *listScreen {
	return &listScreen{title: f.label, build: func() []row {
		var rows []row
		seq := mapGet(get(), leaf)
		if seq == nil || len(seq.Content) == 0 {
			rows = append(rows, note(orZero(f.zero, "none")))
		}
		if seq != nil {
			for _, item := range seq.Content {
				rows = append(rows, row{label: item.Value, help: f.help, keys: "enter edit · d delete", act: func(k string) tea.Cmd {
					switch k {
					case "enter":
						return a.textForm(f.label, item.Value, f.help, notEmpty, func(v string) { item.Value = v })
					case "d", "delete", "backspace":
						if i := indexOf(seq, item); i >= 0 {
							removeAt(seq, i)
						}
					}
					return nil
				}})
			}
		}
		return append(rows, row{label: "+ Add", help: f.help, keys: "enter add", act: func(k string) tea.Cmd {
			if k != "enter" {
				return nil
			}
			return a.textForm(f.label, "", f.help, notEmpty, func(v string) {
				s := ensure(mk(), leaf, yaml.SequenceNode)
				s.Content = append(s.Content, strNode(v))
			})
		}})
	}}
}
