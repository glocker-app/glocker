// Parsing for the three glocker log files. Mirrors internal/reports/reports.go
// so the web app sees exactly the same data glockpeek does.
import { readFile } from "node:fs/promises";

// Default log paths match internal/reports/reports.go.
export const DEFAULT_PATHS = {
  reports: process.env.GLOCKER_REPORTS_LOG || "/var/log/glocker/reports.log",
  unblocks: process.env.GLOCKER_UNBLOCKS_LOG || "/var/log/glocker/unblocks.log",
  lifecycle: process.env.GLOCKER_LIFECYCLE_LOG || "/var/log/glocker/lifecycle.log",
  // Written by internal/usage (the usage monitor); one JSON sample per line.
  usage: process.env.GLOCKER_USAGE_LOG || "/var/log/glocker/usage.jsonl",
  // Written by the glockdoc watchdog; one liveness sample per line.
  heartbeat: process.env.GLOCKER_HEARTBEAT_LOG || "/var/log/glocker/heartbeat.jsonl",
};

// Periods shorter than this are treated as upgrades, not real exposure.
// Matches minUnmanagedDuration in cmd/glockpeek/main.go.
const MIN_UNMANAGED_MS = 2 * 60 * 1000;

async function readLines(path) {
  try {
    const text = await readFile(path, "utf8");
    return text.split("\n").map((l) => l.trim()).filter(Boolean);
  } catch (err) {
    if (err.code === "ENOENT" || err.code === "EACCES") return null;
    throw err;
  }
}

// [2025-11-17 15:35:46] | url-keyword:porn | https://... | domain
const REPORT_RE =
  /^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\] \| (url-keyword|content-keyword):([^ |]+) \| ([^ |]+)(?: \| (.+))?$/;

// Reports use a naive local timestamp; interpret it in the server's local zone.
function parseLocalTimestamp(s) {
  const t = Date.parse(s.replace(" ", "T"));
  return Number.isNaN(t) ? null : t;
}

export async function parseReports(path = DEFAULT_PATHS.reports) {
  const lines = await readLines(path);
  if (lines === null) return { available: false, entries: [] };
  const entries = [];
  for (const line of lines) {
    const m = REPORT_RE.exec(line);
    if (!m) continue;
    const ts = parseLocalTimestamp(m[1]);
    if (ts === null) continue;
    entries.push({
      ts,
      type: m[2], // url-keyword | content-keyword
      keyword: m[3],
      url: m[4],
      domain: m[5] || hostFromUrl(m[4]),
    });
  }
  entries.sort((a, b) => a.ts - b.ts);
  return { available: true, entries };
}

// Older URL-keyword rows have no explicit domain column; derive it from the URL.
function hostFromUrl(url) {
  try {
    return new URL(url).hostname;
  } catch {
    return "";
  }
}

export async function parseUnblocks(path = DEFAULT_PATHS.unblocks) {
  const lines = await readLines(path);
  if (lines === null) return { available: false, entries: [] };
  const entries = [];
  for (const line of lines) {
    let o;
    try {
      o = JSON.parse(line);
    } catch {
      continue;
    }
    const ts = Date.parse(o.unblock_time);
    if (Number.isNaN(ts)) continue;
    const restore = Date.parse(o.restore_time);
    entries.push({
      ts,
      restoreTs: Number.isNaN(restore) ? null : restore,
      reason: o.reason || "",
      domain: o.domain || "",
    });
  }
  entries.sort((a, b) => a.ts - b.ts);
  return { available: true, entries };
}

export async function parseLifecycle(path = DEFAULT_PATHS.lifecycle) {
  const lines = await readLines(path);
  if (lines === null) return { available: false, entries: [] };
  const entries = [];
  for (const line of lines) {
    let o;
    try {
      o = JSON.parse(line);
    } catch {
      continue;
    }
    const ts = Date.parse(o.timestamp);
    if (Number.isNaN(ts)) continue;
    entries.push({
      ts,
      type: o.type, // install | uninstall
      reason: o.reason || "",
      note: o.note || "",
    });
  }
  entries.sort((a, b) => a.ts - b.ts);
  return { available: true, entries };
}

// Parse the usage monitor JSONL log. Each line is a Sample:
//   {"ts":"…","idle_ms":1200,"windows":[{"active":true,"class":"firefox",…}, …]}
// We keep only what the dashboard aggregates over — the active window and idle
// time — so the payload stays small even for long histories (the full window
// list is dropped here).
export async function parseUsage(path = DEFAULT_PATHS.usage) {
  const lines = await readLines(path);
  if (lines === null) return { available: false, entries: [] };
  const entries = [];
  for (const line of lines) {
    let o;
    try {
      o = JSON.parse(line);
    } catch {
      continue;
    }
    const ts = Date.parse(o.ts);
    if (Number.isNaN(ts)) continue;
    const windows = Array.isArray(o.windows) ? o.windows : [];
    const active = windows.find((w) => w && w.active) || null;
    entries.push({
      ts,
      idleMs: Number.isFinite(o.idle_ms) ? o.idle_ms : -1,
      active: active
        ? { class: active.class || "", instance: active.instance || "", title: active.title || "" }
        : null,
      windowCount: windows.length,
    });
  }
  entries.sort((a, b) => a.ts - b.ts);
  return { available: true, entries };
}

// Pair each uninstall with the next install to find spans when glocker was not
// enforcing. An open span (uninstall with no following install) ends at `now`.
// Mirrors getUnmanagedPeriods() in cmd/glockpeek/main.go.
export function unmanagedPeriods(lifecycle, now = Date.now()) {
  const periods = [];
  let openUninstall = null;
  for (const e of lifecycle) {
    if (e.type === "uninstall") {
      openUninstall = e;
    } else if (e.type === "install" && openUninstall) {
      if (e.ts - openUninstall.ts >= MIN_UNMANAGED_MS) {
        periods.push({
          start: openUninstall.ts,
          end: e.ts,
          open: false,
          reason: openUninstall.reason,
          note: openUninstall.note,
        });
      }
      openUninstall = null;
    }
  }
  if (openUninstall) {
    periods.push({
      start: openUninstall.ts,
      end: now,
      open: true,
      reason: openUninstall.reason,
      note: openUninstall.note,
    });
  }
  return periods;
}

// Parse the glockdoc heartbeat log: one JSON liveness sample per line.
// Mirrors ParseHeartbeatLog() in internal/reports/reports.go.
export async function parseHeartbeat(path = DEFAULT_PATHS.heartbeat) {
  const lines = await readLines(path);
  if (lines === null) return { available: false, entries: [] };
  const entries = [];
  for (const line of lines) {
    let o;
    try {
      o = JSON.parse(line);
    } catch {
      continue;
    }
    const ts = Date.parse(o.timestamp);
    if (Number.isNaN(ts)) continue;
    entries.push({ ts, alive: !!o.alive });
  }
  entries.sort((a, b) => a.ts - b.ts);
  return { available: true, entries };
}

// Collapse runs of alive:false samples into observed-down spans. An open span
// (still down at the last sample) ends at `now`. Mirrors DowntimePeriods() in
// internal/reports/reports.go.
export function downtimePeriods(samples, now = Date.now()) {
  const periods = [];
  let start = null;
  for (const s of samples) {
    if (!s.alive) {
      if (start === null) start = s.ts;
      continue;
    }
    if (start !== null) {
      if (s.ts - start >= MIN_UNMANAGED_MS) periods.push({ start, end: s.ts, open: false });
      start = null;
    }
  }
  if (start !== null && now - start >= MIN_UNMANAGED_MS) {
    periods.push({ start, end: now, open: true });
  }
  return periods;
}

// Read and parse everything in one shot.
export async function loadAll(paths = DEFAULT_PATHS, now = Date.now()) {
  const [reports, unblocks, lifecycle, usage, heartbeat] = await Promise.all([
    parseReports(paths.reports),
    parseUnblocks(paths.unblocks),
    parseLifecycle(paths.lifecycle),
    parseUsage(paths.usage),
    parseHeartbeat(paths.heartbeat),
  ]);
  return {
    now,
    sources: {
      reports: reports.available,
      unblocks: unblocks.available,
      lifecycle: lifecycle.available,
      usage: usage.available,
      heartbeat: heartbeat.available,
    },
    violations: reports.entries,
    unblocks: unblocks.entries,
    lifecycle: lifecycle.entries,
    unmanaged: unmanagedPeriods(lifecycle.entries, now),
    downtime: downtimePeriods(heartbeat.entries, now),
    usage: usage.entries,
  };
}
