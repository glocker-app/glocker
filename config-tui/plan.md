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
- Screens (tabs):
  - Domains: inline entries — add/remove, toggle unblockable, edit block
    windows. `!include` blocklist packs shown as toggles (on/off = keep/drop the
    include line). Contents of included files are read-only.
  - Programs: same shape as domains (kill/allow windows, extendible).
  - Keywords: URL/content keywords + whitelist.
  - Unblocking: reasons, temp_unblock_time, max_per_day.
  - Sudo: allow windows.
- Time-window editor shared by all screens: start/end + day toggles.
- Out of scope for now: other stanzas (sync, mail, accountability, panic, …),
  loosening-vs-tightening friction, root gating.
