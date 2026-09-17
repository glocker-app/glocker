# Tags & rules — design spec

Status: design agreed; implementation phased. This covers the tag registry, the
usage-rules editor, and how the Insights work-hours analyzer consumes them.

## Goals

- Give usage a **namespaced tag** model (`Namespace:tag`) with a per-tag
  **distraction** flag and colour.
- Ship a curated **`Glocker:*`** taxonomy so a fresh account is useful with zero
  setup (youtube→entertainment, steam→games, …), while letting users add their
  own tags/namespaces.
- Let the Insights work-hours analyzer count "distraction" time by the user's
  tags instead of a hardcoded app list.
- Keep it clean under E2E: window titles are encrypted, the server is
  content-blind, so **all matching runs in the browser**. The server only stores
  and serves the taxonomy + rules; it never applies them.

## Data model (glockpeek DB, dashboard-local)

### `tags` — the registry
One row per tag. Two scopes:
- **system** (`scope="system"`, `user_id=0`): the shared `Glocker:*` taxonomy,
  vendor-owned, the same for every account, auto-updated centrally (hosted) or by
  binary upgrade (local). Not deletable/renamable by users.
- **user** (`scope="user"`, `user_id=<owner>`): the account's own tags.

Fields: `id, scope, user_id, namespace, name, color, distraction, behavior`.
`behavior` is `""` or `"hide"` (a `Glocker:hide` tag whose windows are dropped
from usage totals). Unique on `(scope, user_id, namespace, name)`.

### `tag_overrides` — per-account override of a system tag
The only editable bit of a system tag is its distraction flag. An override row
records an account's choice without forking the shared tag.
Fields: `user_id, tag_id, distraction`. Unique `(user_id, tag_id)`.

`tag_colors` is absorbed into `tags.color` and retired.

### `rules` — per-account, fully owned
Matcher → tags. Extend the existing table with `position` (order; lower = higher
precedence). A rule references tags via `rule_tags`.
Fields: `id, user_id, position, program, title`.

### `rule_tags` — join (rule → tags)
Fields: `rule_id, tag_id`. A rule carries **at most one tag per namespace**
(enforced app-side, not by the schema). Unique `(rule_id, tag_id)`.

### `users`
Add `rules_seeded bool` — guards one-time starter-rule seeding.

## Seeding

- **System tags** (`Glocker:*`): seeded/upserted globally on `Migrate()` from the
  canonical list in code (`internal/store` seed data). Idempotent, so binary
  upgrades refresh them.
- **Starter rules**: seeded into an account **once, when it becomes active** —
  at email verification for self-serve signups, at creation for accounts active
  on creation (the local default user, admin `-adduser`). Guarded by
  `users.rules_seeded`. After that the rules are the user's: edit, delete,
  reorder. Deleted starter rules never come back.
- Trade-off accepted: later improvements to starter *rules* reach only new
  accounts; existing accounts keep their (owned) copy. System *tags* stay shared
  and do update.

## Classification (browser)

For each usage sample: run the account's rules in order; **first match wins per
namespace**. A sample can end up with several tags (one per namespace). Tags are
derived every render — nothing is stored on usage rows — so deleting a tag/rule
updates past views too, with no data rewrite. Distraction for a tag = its value,
or the account's override for a system tag.

## Insights

The work-hours analyzer counts time whose tag is a distraction, during the
work-hours window, on weekdays. No hardcoded app list. Websites (matched by
title) can't yield a domain automatically, so their suggestion is a prompt, not a
ready stanza; apps (matched by class) suggest a forbidden-program kill window.

## API (glockpeek)

- `GET/POST/PATCH/DELETE /api/tags` — user tags CRUD; toggle distraction
  (system tags via an override row, user tags in place).
- `GET/PUT /api/rules` — rules + their tags + order. Replaces the current
  `{rules, colors}` shape.
- Browser fetches system tags + the account's tags + rules, and classifies
  locally.

## Frontend

- **Analytics → Tags** sub-tab: the registry editor. Grouped by namespace;
  per tag colour + distraction toggle + (user) delete; per-namespace add/delete;
  add namespace. `Glocker` is the system namespace (distraction editable only).
- **Analytics → Usage** sub-tab: the rules editor stays here. Matcher → one slot
  per namespace, values chosen from the registry; **drag to reorder** (precedence).

## Migration (HARD GATE — validate on a prod dump before deploy)

Additive + idempotent. New tables (`tags`, `tag_overrides`, `rule_tags`), new
columns (`rules.position`, `users.rules_seeded`). Backfill:
- `tag_colors` → user `tags` (namespace from the legacy tag string if it carries
  a `:`, else a default user namespace); legacy `"hidden"` → `Glocker:hide`.
- Each legacy `rules.tag` → a tag + a `rule_tags` link; keep the old column
  (unused) rather than dropping it.
Validate per the standard drill (restore prod dump → migrate → counts unchanged →
spot-check) before it ships.

## Phases

- [ ] Store: models + system-tag seed + `rules_seeded` + `SeedAccountRules` + tests (additive; no reshape yet).
- [ ] Store: rules→`rule_tags` reshape + legacy backfill + migration validation.
- [ ] API: `/api/tags`, reworked `/api/rules`.
- [ ] Frontend: Tags sub-tab registry editor.
- [ ] Frontend: Usage rules editor on the new model + drag reorder.
- [ ] Insights: work-hours reads distraction tags from the registry; drop the hardcoded app list.
