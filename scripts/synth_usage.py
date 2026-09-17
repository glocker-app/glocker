#!/usr/bin/env python3
"""Synthesize a realistic multi-month glocker usage log for exercising the
glockpeek "Usage" tab.

It learns the window universe (app classes + title styles) from an existing
usage log — by default the live ``op.jsonl`` — then fabricates plausible daily
sessions: the machine wakes and sleeps at jittered times, the user sticks on one
app then switches, takes idle breaks, and leans toward work apps during the day
and games/browsing in the evenings and on weekends.

Output matches internal/usage's JSON Lines schema exactly, one sample per line:

    {"ts": "...+05:30", "idle_ms": 1200, "windows": [{"active": true, ...}, ...]}

Usage:
    python3 scripts/synth_usage.py                       # 90 days -> op-3mo.jsonl
    python3 scripts/synth_usage.py --days 120 --out /tmp/usage.jsonl --seed 7

The output is a usage log in internal/usage's schema; the glocker syncer ingests
it into glockpeek, which drives the "Usage" tab.

Standard library only — no venv needed.
"""

import argparse
import json
import random
from datetime import datetime, timedelta, timezone

# The log's timestamps carry a +05:30 offset (Asia/Kolkata); mirror it so the
# server's local-zone day bucketing lines up with the real data.
TZ = timezone(timedelta(hours=5, minutes=30))

# Extra titles layered on top of whatever we learn from the seed file, so a
# 3-month timeline does not repeat the same handful of titles forever.
EXTRA_TITLES = {
    "Emacs": [
        "main.go - GNU Emacs at asylum",
        "notes.org - GNU Emacs at asylum",
        "config.yaml - GNU Emacs at asylum",
        "*scratch* - GNU Emacs at asylum",
        "reports.go - GNU Emacs at asylum",
    ],
    "firefox-esr": [
        "GitHub — Mozilla Firefox",
        "Stack Overflow — Mozilla Firefox",
        "Hacker News — Mozilla Firefox",
        "YouTube — Mozilla Firefox",
        "Gmail — Mozilla Firefox",
        "go.dev — Mozilla Firefox",
    ],
    "kitty": ["0:5.0 zsh", "0:11.0 zsh", "0:12.0 vim"],
    "slack": [
        "general - Kulu - Slack",
        "engineering - Hamon - Slack",
    ],
    "dosbox": [
        "DOSBox 0.74-3, Cpu speed: max 100% cycles, Frameskip  0, Program:    DOOM",
        "DOSBox 0.74-3, Cpu speed:     3000 cycles, Frameskip  0, Program:   DOSBOX",
    ],
}

# Per-app relative weight as a function of hour-of-day and weekend, before
# stickiness. Higher = more likely to be the focused app.
def app_weight(cls, hour, weekend):
    work_hours = 9 <= hour < 19 and not weekend
    evening = hour >= 19 or hour < 2
    base = {
        "Emacs": 5.0 if work_hours else 1.0,
        "kitty": 3.0 if work_hours else 1.0,
        "firefox-esr": 3.0 if work_hours else 4.0,
        "slack": 2.5 if work_hours else 0.6,
        "Claude": 2.5 if work_hours else 1.0,
        "libreoffice-calc": 1.2 if work_hours else 0.2,
        "dosbox": 0.2 if work_hours else 3.5,
    }.get(cls, 1.0)
    if evening and cls in ("dosbox", "firefox-esr"):
        base *= 1.8
    if weekend and cls == "dosbox":
        base *= 2.0
    return base


def load_universe(path):
    """Return {class: {"instance": str, "titles": [str, ...]}} from a usage log."""
    universe = {}
    with open(path) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            for w in obj.get("windows", []):
                cls = w.get("class", "")
                if not cls:
                    continue
                entry = universe.setdefault(cls, {"instance": w.get("instance", ""), "titles": []})
                title = w.get("title", "")
                if title and title not in entry["titles"]:
                    entry["titles"].append(title)
    # Fold in the extra titles for variety over long spans.
    for cls, titles in EXTRA_TITLES.items():
        if cls in universe:
            for t in titles:
                if t not in universe[cls]["titles"]:
                    universe[cls]["titles"].append(t)
    if not universe:
        raise SystemExit("no windows found in seed file; is it a usage log?")
    return universe


def day_session(day, weekend, rng):
    """Return (start, end) datetimes for the machine-on session on `day`, or None
    for an occasional fully-off day."""
    if rng.random() < (0.12 if weekend else 0.03):
        return None  # day off / travelling
    if weekend:
        start_h = rng.uniform(9.5, 12.0)
        length = rng.uniform(3.0, 8.0)
    else:
        start_h = rng.uniform(8.5, 10.5)
        length = rng.uniform(7.0, 11.5)
    start = datetime(day.year, day.month, day.day, tzinfo=TZ) + timedelta(hours=start_h)
    end = start + timedelta(hours=length)
    # Never run past ~01:30 the next morning.
    cap = datetime(day.year, day.month, day.day, tzinfo=TZ) + timedelta(hours=25.5)
    return start, min(end, cap)


def pick_title(entry, rng, current_title):
    titles = entry["titles"] or ["—"]
    # Mostly keep the current title (you stay in one buffer/tab a while).
    if current_title in titles and rng.random() < 0.6:
        return current_title
    return rng.choice(titles)


def generate(universe, days, interval, end_day, rng):
    classes = list(universe)
    samples = []
    start_day = end_day - timedelta(days=days - 1)
    day = start_day
    while day <= end_day:
        weekend = day.weekday() >= 5
        session = day_session(day, weekend, rng)
        day += timedelta(days=1)
        if session is None:
            continue
        start, end = session

        # Per-day: choose stable titles for the open windows, one active.
        title_state = {cls: (entry["titles"][0] if entry["titles"] else "—")
                       for cls, entry in universe.items()}
        active_cls = rng.choice(classes)
        last_input = start  # for idle_ms
        t = start
        step = timedelta(seconds=interval)
        while t < end:
            hour = t.hour
            # Switch apps now and then, weighted by hour/weekend.
            if rng.random() < 0.15:
                weights = [app_weight(c, hour, weekend) for c in classes]
                active_cls = rng.choices(classes, weights=weights, k=1)[0]
            # Refresh the active window's title occasionally.
            title_state[active_cls] = pick_title(universe[active_cls], rng, title_state[active_cls])

            # Idle model: most minutes you gave input recently; sometimes not,
            # so idle_ms grows into a break.
            if rng.random() < 0.85:
                last_input = t - timedelta(milliseconds=rng.randint(0, 15000))
            idle_ms = int((t - last_input).total_seconds() * 1000)

            windows = []
            for cls, entry in universe.items():
                windows.append({
                    "active": cls == active_cls,
                    "class": cls,
                    "instance": entry["instance"],
                    "title": title_state[cls],
                })
            samples.append({
                "ts": t.isoformat(),
                "idle_ms": idle_ms,
                "windows": windows,
            })
            t += step

        # End the session idle, so the capped gap before the next day counts as
        # away time rather than inflating the last app's active total.
        if samples:
            samples[-1]["idle_ms"] = rng.randint(120_000, 600_000)

    return samples


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--seed-file", default="op.jsonl", help="usage log to learn the window universe from")
    ap.add_argument("--out", default="op-3mo.jsonl", help="output JSONL path")
    ap.add_argument("--days", type=int, default=90, help="number of days to synthesize (ending today)")
    ap.add_argument("--interval", type=int, default=60, help="seconds between samples")
    ap.add_argument("--seed", type=int, default=1, help="RNG seed for reproducibility")
    args = ap.parse_args()

    rng = random.Random(args.seed)
    universe = load_universe(args.seed_file)
    end_day = datetime.now(TZ).date()
    end_day = datetime(end_day.year, end_day.month, end_day.day, tzinfo=TZ)

    samples = generate(universe, args.days, args.interval, end_day, rng)

    with open(args.out, "w") as fh:
        for s in samples:
            fh.write(json.dumps(s, ensure_ascii=False) + "\n")

    first = samples[0]["ts"][:10] if samples else "—"
    last = samples[-1]["ts"][:10] if samples else "—"
    print(f"wrote {len(samples):,} samples to {args.out}")
    print(f"  span: {first} .. {last}  ({args.days} days, {args.interval}s interval)")
    print(f"  apps: {', '.join(sorted(universe))}")


if __name__ == "__main__":
    main()
