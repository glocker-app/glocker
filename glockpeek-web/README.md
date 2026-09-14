# glockpeek-web

A tiny web companion to the `glockpeek` CLI. It reads the same glocker log
files and lets you walk through your history visually — to find the times you
are most **vulnerable to failure**.

The headline is a single **composite health score** (0–100) so you can see
overall health at a glance; the **vulnerability map** (a weekday × hour heatmap)
sits just below to show *when* you slip.

## Composite health score

`score = 100 − penalties`, floored at 0. Each penalty is a **rate** (not a raw
count) so the score is comparable as you switch the window:

| Penalty | Based on | Max points (weight) |
|---------|----------|---------------------|
| Unmanaged | fraction of the window glocker was uninstalled | 80 |
| Violations | violations per day | 30 / (viol·day), capped at 45 |
| Deliberate days | fraction of days with >2 hits | 50 |

Weights live in `HEALTH_WEIGHTS` at the top of `public/app.js` — tune to taste.
The score is banded (Excellent ≥ 90, Good ≥ 75, Fair ≥ 60, At risk ≥ 40,
Critical < 40) and shows a breakdown of what cost the most.

## Views

A left-hand nav switches between views instead of one long scroll:

- **Overview** — composite health score + the readout strip (totals, deliberate
  days (>2 hits), clean days, peak exposure window, unmanaged time, unblocks).
- **History** — real month-grid calendar + daily timeline. Each day is shaded
  by violation count and carries a bottom hatch strip sized to the *fraction*
  of the day glocker was unmanaged (shown only above an hour), so a briefly
  unmanaged day no longer looks fully exposed. A sticky **Day detail** inspector
  fills with a day's full breakdown — the unmanaged/managed split, keyword and
  domain tallies, and a time-ordered hit list — when you hover a calendar day
  or a timeline point.
- **Patterns** — the vulnerability map (weekday × hour, peak slot glowing) plus
  by-weekday and by-hour distributions.
- **Sources** — top keywords and top domains.
- **Bypasses** — unblock grants by reason, and every span when glocker was
  uninstalled.

The **window** control (30d / 90d / 1y / all, default 30d) reframes every view.
For the fixed sizes, the **‹ ›** pager steps one whole window into the past or
future (the label shows the current window's dates); it stops at the present and
at the earliest logged day. "all" spans everything and isn't paged.

## Data sources

Reads the three glocker logs (same defaults as `internal/reports`):

| Log | Default path | Override env var |
|-----|--------------|------------------|
| Violations | `/var/log/glocker/reports.log` | `GLOCKER_REPORTS_LOG` |
| Unblocks | `/var/log/glocker/unblocks.log` | `GLOCKER_UNBLOCKS_LOG` |
| Lifecycle | `/var/log/glocker/lifecycle.log` | `GLOCKER_LIFECYCLE_LOG` |

Unmanaged periods are computed from the lifecycle log (uninstall→install pairs),
ignoring spans under 2 minutes — matching `cmd/glockpeek/main.go`.

## Run

```bash
cd glockpeek-web
npm install
npm start                 # http://localhost:4317
```

`PORT` overrides the listen port. The browser must reach a host that can read
the glocker logs (run it on the same machine as glocker).

## API

Two read-only JSON endpoints, documented in [`openapi.yaml`](./openapi.yaml):

- `GET /api/data` — the full parsed history (violations, unblocks, lifecycle, unmanaged spans).
- `GET /api/health` — liveness and which log files were found.

All aggregation and date-range filtering happens client-side; the data set is
small (a few hundred rows).
