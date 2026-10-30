# config-tui

Status: implementation

Standalone prototype of a TUI config editor for glocker, for refining the
editing UX. Does not modify any existing glocker code.

## Setup

- Go module `glocker/config-tui` in `glocker/config-tui/`, own `go.mod` with
  `replace glocker => ../` so it can reuse `glocker/internal/config` read-only
  (parse + validate). The parent module ignores this dir (it has its own go.mod).
- `make build` → `./config-tui`; `make run CONF=../conf/conf.yaml.sample`.
- Runs as a normal user against any file path; no root, no daemon reload.

## Architecture

- Libraries: bubbletea + bubbles (list, textinput) + lipgloss (already in the
  parent's deps), huh for forms, gopkg.in/yaml.v3, renameio for atomic writes.
- Model: the config is held as a `yaml.Node` tree, not the struct, so comments,
  key order and `!include` lines survive a save. Edits mutate nodes.
- Validation: before saving, marshal the tree → `config.ParseConfig` +
  `config.ValidateConfig`. Invalid → show the error, stay in the editor.
- Save: diff of old vs new text shown first, confirm, atomic write. Writes to
  `-o` path (default `<input>.new`) so the real config is never touched while
  prototyping.
- Layout: sidebar listing every stanza (with an on/off dot) beside the
  selected stanza's fields, and a help panel at the bottom with the focused
  field's description and the keys that apply.
- Stanzas are declared in `schema.go` as typed fields (bool, int, string,
  list, windows, enum, unset/yes/no). Viewing never creates missing stanzas;
  writes create them on demand.
  - Domains: inline entries plus non-generated includes (personal-domains.yaml,
    where new domains go). Generated blocklists are on/off toggles (keep/drop
    the include line); unincluded ones in conf.d/ show as off.
  - Programs: one "killed" schedule; saved as kill_windows (allow_windows
    folded in and dropped).
- Schedules (domain blocked, program killed, sudo allowed) are edited on one
  7×48 half-hour grid: keyboard painting, box select, copy/paste a day, mouse
  drag. Windows are derived on save (fewest windows; overnight joined when
  shorter); untouched grids are not rewritten. Edits snap to 30 minutes.
- Out of scope for now: loosening-vs-tightening friction, root gating.

## Implementation steps

- [x] Module setup; nested module can import `glocker/internal/config`. Check: `go build` succeeds.
- [x] YAML node model: load, classify includes, add/delete domains, toggle packs, validate, write. Check: `make test` passes.
- [x] Minimal saves: edits replayed onto the original text. Check: editing one value in the sample changes exactly one line.
- [x] Screens: Domains, Programs, Keywords, Unblocking, Sudo, General, plus a shared window form. Check: every field can be edited from the keyboard in tmux.
- [x] Save flow: validate, show diff, confirm, atomic write to `<file>.new` (`-inplace` to overwrite). Check: an invalid window blocks the save; the written file reloads with `config.LoadFile`.
- [x] Quit guard on unsaved changes.
- [x] Sidebar + help panel layout. Check: every stanza reachable from the sidebar; help changes with the focused row.
- [x] All agent stanzas editable via the schema. Check: viewing every section of a near-empty config leaves it unchanged (test).
- [x] Program kill mode. Check: flipping moves the list between kill_windows and allow_windows.
- [x] Window picker with week grid. Check: typed 22:00–06:00 renders as overnight; digit bursts handled (test).
- [x] Schedule grid replaces the window list + picker. Check: sample sudo windows round-trip unchanged; overnight windows round-trip (tests); box-clearing weekday evenings writes the expected windows.
