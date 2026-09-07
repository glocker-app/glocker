// glockpeek-web frontend. Pulls the full parsed history from /api/data, then
// does all aggregation/filtering in the browser (the data set is tiny). The
// vulnerability heatmap is the centrepiece: it surfaces the hour/weekday slots
// where you actually slip — and hatches the slots where glocker was off, so a
// blank cell is never mistaken for a safe one.

const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const DAY = 86400000;
const HOUR = 3600000;

const RANGES = [
  { id: "1d", label: "1d", days: 1 },
  { id: "1w", label: "1w", days: 7 },
  { id: "30d", label: "30d", days: 30 },
  { id: "90d", label: "90d", days: 90 },
  { id: "1y", label: "1y", days: 365 },
  { id: "all", label: "all", days: null },
];

// offset counts fixed windows back from the most recent: 0 = latest window
// ending now, -1 = the window immediately before it, etc.
const state = { data: null, range: "30d", offset: 0, view: "overview", charts: {}, rules: [], tagColors: {}, usageWindow: null, selectedTag: null, expandedApps: new Set(), selectedProgram: null, lastTagUsage: null, ignored: [], openDay: null };

const VIEW_TITLES = {
  overview: "Overview",
  history: "History",
  usage: "Usage",
  sources: "Sources",
  bypasses: "Bypasses",
  devices: "Devices",
  users: "Admin",
};

// Cookie key for the persisted time window. Declared here (before init runs) so
// restoreWindow() can read it without hitting a temporal-dead-zone error.
const WINDOW_COOKIE = "gp_window";

init();

// init gates on authentication: if there's no valid session, show the login
// screen; otherwise boot the dashboard. Login success calls bootDashboard()
// directly (no full reload).
async function init() {
  let me = null;
  try {
    const res = await fetch("api/me");
    if (res.ok) me = await res.json();
  } catch { /* network error -> treat as unauthenticated */ }
  if (!me) {
    showLogin();
    return;
  }
  bootDashboard(me);
}

// showLogin reveals the login form and wires submit -> POST api/login.
function showLogin() {
  document.getElementById("loading").hidden = true;
  document.getElementById("dash").hidden = true;
  const screen = document.getElementById("login");
  screen.hidden = false;
  const form = document.getElementById("login-form");
  const errEl = document.getElementById("login-error");
  form.onsubmit = async (e) => {
    e.preventDefault();
    errEl.hidden = true;
    const email = document.getElementById("login-user").value;
    const password = document.getElementById("login-pass").value;
    let res;
    try {
      res = await fetch("api/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password }),
      });
    } catch (err) {
      errEl.textContent = `Could not reach the server: ${err.message}`;
      errEl.hidden = false;
      return;
    }
    if (!res.ok) {
      errEl.textContent = res.status === 401 ? "Invalid email or password" : `Sign-in failed (${res.status})`;
      errEl.hidden = false;
      return;
    }
    document.getElementById("login-pass").value = "";
    screen.hidden = true;
    document.getElementById("loading").hidden = false;
    bootDashboard({ auth: true }); // login only happens when auth is enabled
  };
  setupRegister();
}

// setupRegister wires the sign-up card: the login<->register toggles and the
// POST api/register submit. Registration requires a verified email before any
// session exists, so success shows a "check your email" note rather than
// signing the user in.
// Altcha proof-of-work captcha (invisible): fetch a challenge and brute-force
// the matching number in the background so it's ready by the time the user
// submits. If the endpoint is absent (captcha disabled) we send nothing and the
// server won't require it. Each solution is single-use, so we refresh after each
// attempt.
let altchaPromise = null;

function ensureCaptcha() {
  altchaPromise = (async () => {
    let ch;
    try {
      const r = await fetch("api/altcha");
      if (!r.ok) return ""; // captcha not enabled on this instance
      ch = await r.json();
    } catch { return ""; }
    return await solveAltcha(ch);
  })();
}

async function solveAltcha(ch) {
  const enc = new TextEncoder();
  for (let n = 0; n <= ch.maxnumber; n++) {
    const digest = await crypto.subtle.digest("SHA-256", enc.encode(ch.salt + n));
    const hex = [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
    if (hex === ch.challenge) {
      return btoa(JSON.stringify({ algorithm: ch.algorithm, challenge: ch.challenge, number: n, salt: ch.salt, signature: ch.signature }));
    }
  }
  return "";
}

function setupRegister() {
  const loginForm = document.getElementById("login-form");
  const regForm = document.getElementById("register-form");
  const loginErr = document.getElementById("login-error");
  const regErr = document.getElementById("register-error");
  const regNote = document.getElementById("register-note");

  const show = (which) => {
    loginErr.hidden = true;
    regErr.hidden = true;
    loginForm.hidden = which !== "login";
    regForm.hidden = which !== "register";
  };
  document.getElementById("show-register").onclick = (e) => { e.preventDefault(); show("register"); ensureCaptcha(); };
  document.getElementById("show-login").onclick = (e) => { e.preventDefault(); show("login"); };

  regForm.onsubmit = async (e) => {
    e.preventDefault();
    regErr.hidden = true;
    regNote.hidden = true;
    const email = document.getElementById("reg-user").value;
    const password = document.getElementById("reg-pass").value;
    const confirm = document.getElementById("reg-pass2").value;
    if (password !== confirm) {
      regErr.textContent = "Passwords do not match";
      regErr.hidden = false;
      return;
    }
    const submit = document.getElementById("register-submit");
    submit.disabled = true;
    // Wait for the background PoW solve (usually already done), then include it.
    const altcha = altchaPromise ? await altchaPromise : "";
    let res;
    try {
      res = await fetch("api/register", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password, altcha }),
      });
    } catch (err) {
      regErr.textContent = `Could not reach the server: ${err.message}`;
      regErr.hidden = false;
      submit.disabled = false;
      ensureCaptcha(); // the challenge was consumed; get a fresh one for retry
      return;
    }
    if (!res.ok) {
      const msg = {
        400: "Check your details and try again (a valid email and 8+ char password).",
        404: "Sign-ups are not enabled on this instance",
        409: "That email is already registered",
        429: "Too many attempts. Please wait a few minutes and try again",
        502: "Could not send the verification email; please try again",
        503: "Sign-ups are not available yet (email is not configured)",
      }[res.status] || `Sign-up failed (${res.status})`;
      regErr.textContent = msg;
      regErr.hidden = false;
      submit.disabled = false;
      ensureCaptcha(); // single-use challenge spent; refresh for the next attempt
      return;
    }
    // Success: no session yet — the account activates from the emailed link.
    // Collapse the form to a confirmation; the "Sign in" toggle stays available.
    regForm.querySelectorAll("label, #register-submit").forEach((el) => { el.hidden = true; });
    regNote.textContent = "";
    regNote.append("Account created. We sent a verification link to ");
    const strong = document.createElement("strong");
    strong.textContent = email;
    regNote.append(strong, " — open it to activate your account, then sign in.");
    regNote.hidden = false;
  };
}

async function bootDashboard(me) {
  // The login path calls us with a partial {auth:true}; fetch the full identity
  // (email/admin) so admin-only UI can gate on it without a page reload.
  if (me && me.admin === undefined) {
    try {
      const r = await fetch("api/me");
      if (r.ok) me = await r.json();
    } catch { /* keep partial me */ }
  }
  // Hosted is end-to-end-encrypted only: an account with no key vault must set a
  // passphrase before it can reach the dashboard. Both the fresh-load and the
  // post-login paths funnel through here. (Temporary bounce to the standalone
  // setup page; the planned HTMX/SSR migration replaces this with a real
  // server-side /settings/encryption route and clean URL.)
  if (me && me.auth && !me.e2e) {
    location.href = "setup-encryption.html";
    return;
  }
  restoreWindow(); // apply the saved range/offset before building controls
  buildRangeButtons();
  try {
    const res = await fetch("api/data");
    if (res.status === 401) { showLogin(); return; }
    if (!res.ok) throw new Error(`server returned ${res.status}`);
    state.data = await res.json();
  } catch (err) {
    showError(`Could not load logs: ${err.message}`);
    return;
  }
  // Usage categorization config: rules + tag colours (non-fatal if missing).
  try {
    const rres = await fetch("api/rules");
    if (rres.ok) {
      const cfg = await rres.json();
      state.rules = cfg.rules || [];
      state.tagColors = cfg.colors || {};
    }
  } catch { /* keep empty rules/colours */ }

  // False-positive ignore list (non-fatal if missing).
  try {
    const gres = await fetch("api/ignored");
    if (gres.ok) state.ignored = (await gres.json()).ignored || [];
  } catch { /* keep empty ignore list */ }

  // Content-side post-processing the server no longer does — it stays
  // content-blind so the same endpoint can serve E2E ciphertext later
  // (docs/e2e-design.md). Hide ignored violations and fill any missing domain
  // from the URL. Done once here, so all downstream aggregation sees the same
  // filtered list it always did.
  if (state.data && Array.isArray(state.data.violations)) {
    const ign = new Set(state.ignored.map(ignoreKeyOf));
    // Domain fallback (mirrors the old server-side hostFromURL): fill a missing
    // domain from the URL client-side, so the server never parses URLs.
    const hostFromUrl = (u) => { try { return new URL(u).hostname; } catch { return ""; } };
    state.data.violations = state.data.violations
      .filter((v) => !ign.has(ignoreKeyOf(v)))
      .map((v) => (v.domain ? v : { ...v, domain: hostFromUrl(v.url) }));
  }

  document.getElementById("loading").hidden = true;
  document.getElementById("dash").hidden = false;

  // Data-sync panel (last daemon push). Refresh periodically so "N ago" stays
  // current without a reload.
  renderSync();
  setInterval(renderSync, 30000);

  // Sign-out + device management: only meaningful when auth is on (single-user
  // mode has no login and ingest is tokenless).
  if (me && me.auth) {
    const logoutBtn = document.getElementById("logout-btn");
    logoutBtn.hidden = false;
    logoutBtn.onclick = async () => {
      try { await fetch("api/logout", { method: "POST" }); } catch { /* fall through */ }
      location.reload();
    };
    document.getElementById("nav-devices").hidden = false;
    setupDevices();
    // Admin-only: the accounts panel.
    if (me.admin) document.getElementById("nav-users").hidden = false;
  }

  // Delegated click: the calendar is re-rendered on every range change, but the
  // container element is stable so one listener suffices. Click (not hover) so
  // moving the mouse toward the detail panel doesn't keep changing the day.
  document.getElementById("calendar").addEventListener("click", (e) => {
    const cell = e.target.closest(".cal-day[data-ts]");
    if (cell) showDay(Number(cell.dataset.ts));
  });

  // Left-nav view switching drives the URL hash; the hashchange handler below
  // (and the initial hash) is what actually applies the view — so deep links
  // and browser back/forward all work.
  document.getElementById("nav").addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-view]");
    if (btn) routeTo(btn.dataset.view);
  });
  window.addEventListener("hashchange", () => setView(viewFromHash()));

  // Mark a violation as a false positive from the day-detail hit list.
  document.getElementById("day-detail").addEventListener("click", (e) => {
    const btn = e.target.closest(".hit-x");
    if (!btn) return;
    ignoreViolation({
      ts: Number(btn.dataset.ts), keyword: btn.dataset.kw,
      url: btn.dataset.url, domain: btn.dataset.dom,
    });
  });

  // Restore a previously-ignored violation.
  document.getElementById("ignored-list").addEventListener("click", (e) => {
    const btn = e.target.closest(".ign-restore");
    if (!btn) return;
    restoreViolation({ ts: Number(btn.dataset.ts), keyword: btn.dataset.kw, url: btn.dataset.url });
  });

  // Tag-contributor tree: click an app row to expand/collapse its windows and
  // show its common-title-words pie beside the list.
  document.getElementById("usage-tag-detail").addEventListener("click", (e) => {
    const row = e.target.closest(".tt-app[data-app]");
    if (!row) return;
    const app = row.dataset.app;
    if (state.expandedApps.has(app)) state.expandedApps.delete(app);
    else state.expandedApps.add(app);
    state.selectedProgram = app;
    renderTagContributors(state.selectedTag, state.lastTagUsage);
  });

  clampOffset(); // a saved offset may point past the data we actually have
  setupRules();
  renderFooter();
  render();
  setView(viewFromHash()); // honour a deep-linked view like #usage on load
}

// The view named by the URL hash (e.g. "#usage"), or "overview" if absent/unknown.
function viewFromHash() {
  const v = decodeURIComponent(location.hash.replace(/^#\/?/, ""));
  return VIEW_TITLES[v] ? v : "overview";
}

// Navigate to a view by updating the hash; the hashchange handler applies it.
// If the hash already matches (e.g. re-clicking the current tab), apply directly
// since no hashchange event would fire.
function routeTo(view) {
  if (viewFromHash() === view) setView(view);
  else location.hash = view;
}

function setView(view) {
  state.view = view;
  document.querySelectorAll(".view").forEach((v) => v.classList.toggle("active", v.dataset.view === view));
  document.querySelectorAll("#nav button").forEach((b) => b.classList.toggle("active", b.dataset.view === view));
  document.getElementById("view-title").textContent = VIEW_TITLES[view] || view;
  if (view === "devices") renderDevices();
  if (view === "users") renderUsers();
  // Charts built while their view was display:none have zero size; fix on reveal.
  requestAnimationFrame(() => {
    for (const c of Object.values(state.charts)) {
      try { c.resize(); } catch { /* chart may be mid-rebuild */ }
    }
  });
}

function showError(msg) {
  document.getElementById("loading").hidden = true;
  const el = document.getElementById("error");
  el.hidden = false;
  el.innerHTML = `<div>⚠ ${msg}</div><div style="color:var(--ink-faint)">Is glockpeek-web running on a host with the glocker logs?</div>`;
}

// ── Local-time helpers ──────────────────────────────────
// JS getDay() is 0=Sun..6=Sat; we want Mon=0..Sun=6 to match glockpeek.
const weekdayIndex = (d) => (d.getDay() + 6) % 7;
const dayKey = (ts) => {
  const d = new Date(ts);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};
const startOfDay = (ts) => {
  const d = new Date(ts);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
};

// ── Range / window control ──────────────────────────────
function buildRangeButtons() {
  const wrap = document.getElementById("range-buttons");
  for (const r of RANGES) {
    const b = document.createElement("button");
    b.textContent = r.label;
    b.dataset.id = r.id;
    if (r.id === state.range) b.classList.add("active");
    b.addEventListener("click", () => {
      state.range = r.id;
      state.offset = 0; // reset to the most recent window when resizing
      wrap.querySelectorAll("button").forEach((x) => x.classList.toggle("active", x.dataset.id === r.id));
      saveWindow();
      render();
    });
    wrap.appendChild(b);
  }

  // Step one window into the past / future.
  document.getElementById("range-prev").addEventListener("click", () => shiftWindow(-1));
  document.getElementById("range-next").addEventListener("click", () => shiftWindow(1));
}

function shiftWindow(dir) {
  const r = RANGES.find((x) => x.id === state.range);
  if (!r.days) return; // "all" is not shiftable
  const next = Math.min(0, state.offset + dir);
  if (next === state.offset) return;
  // Don't page past the earliest data we have.
  if (dir < 0 && windowBounds().start <= startOfDay(earliestTs())) return;
  state.offset = next;
  saveWindow();
  render();
}

// ── Persisted window (cookie) ───────────────────────────
// Remember the selected range + offset so a reload lands on the same window.
// (WINDOW_COOKIE is declared up top so init() can use it before this point.)
function saveWindow() {
  setCookie(WINDOW_COOKIE, `${state.range}|${state.offset}`, 365);
}

function restoreWindow() {
  const raw = getCookie(WINDOW_COOKIE);
  if (!raw) return;
  const [range, offsetStr] = raw.split("|");
  if (!RANGES.some((r) => r.id === range)) return; // stale/unknown range id
  state.range = range;
  const off = parseInt(offsetStr, 10);
  state.offset = Number.isFinite(off) && off < 0 ? off : 0;
}

// Pull a restored offset forward if it points before the earliest data (e.g. a
// day-old cookie, or data that has since rolled off). Needs state.data.
function clampOffset() {
  const r = RANGES.find((x) => x.id === state.range);
  if (!r || !r.days) { state.offset = 0; return; } // "all" has no offset
  if (state.offset >= 0) { state.offset = 0; return; }
  const span = r.days * DAY;
  const maxBack = Math.floor((startOfDay(state.data.now) - startOfDay(earliestTs())) / span);
  if (state.offset < -maxBack) state.offset = -maxBack;
}

function setCookie(name, value, days) {
  document.cookie = `${name}=${encodeURIComponent(value)};path=/;max-age=${days * 86400};SameSite=Lax`;
}

function getCookie(name) {
  const m = document.cookie.match(new RegExp("(?:^|; )" + name + "=([^;]*)"));
  return m ? decodeURIComponent(m[1]) : null;
}

function earliestTs() {
  let min = Infinity;
  for (const a of [state.data.violations, state.data.unblocks, state.data.lifecycle, state.data.usage || []]) {
    if (a.length) min = Math.min(min, a[0].ts); // arrays are sorted ascending
  }
  return Number.isFinite(min) ? min : state.data.now;
}

function windowBounds() {
  const now = state.data.now;
  const r = RANGES.find((x) => x.id === state.range);
  if (!r.days) {
    // "all" — from the earliest event we have.
    const start = startOfDay(earliestTs());
    return { start, end: now };
  }
  // Anchor to day boundaries and page back by whole windows.
  const endDayStart = startOfDay(now) + state.offset * r.days * DAY;
  const start = endDayStart - (r.days - 1) * DAY;
  const end = state.offset === 0 ? now : endDayStart + DAY - 1;
  return { start, end };
}

// Update the pager label and enabled/disabled state of the step buttons.
function renderRangeControl(b) {
  const r = RANGES.find((x) => x.id === state.range);
  const nav = document.getElementById("range-nav");
  const label = document.getElementById("range-label");
  const prev = document.getElementById("range-prev");
  const next = document.getElementById("range-next");

  if (!r.days) {
    nav.hidden = true;
    return;
  }
  nav.hidden = false;
  const fmt = (ts) => new Date(ts).toLocaleDateString(undefined, { day: "numeric", month: "short" });
  const fmtY = (ts) => new Date(ts).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
  label.textContent = `${fmt(b.start)} – ${fmtY(b.end)}`;

  next.disabled = state.offset === 0;
  prev.disabled = b.start <= startOfDay(earliestTs());
}

const within = (ts, b) => ts >= b.start && ts <= b.end;

// ── Master render ───────────────────────────────────────
function render() {
  const b = windowBounds();
  renderRangeControl(b);
  const violations = state.data.violations.filter((v) => within(v.ts, b));
  const unblocks = state.data.unblocks.filter((u) => within(u.ts, b));
  const unmanaged = state.data.unmanaged
    .map((p) => ({ ...p, start: Math.max(p.start, b.start), end: Math.min(p.end, b.end) }))
    .filter((p) => p.end > p.start);
  const downtime = (state.data.downtime || [])
    .map((p) => ({ ...p, start: Math.max(p.start, b.start), end: Math.min(p.end, b.end) }))
    .filter((p) => p.end > p.start);

  // Per-day index shared by the calendar, timeline, and day inspector.
  const dayIndex = new Map();
  for (const v of violations) {
    const k = dayKey(v.ts);
    if (!dayIndex.has(k)) dayIndex.set(k, []);
    dayIndex.get(k).push(v);
  }
  state.dayIndex = dayIndex;

  const heat = buildHeatmap(violations, b);
  // Composite health is a standing measure over ALL recorded history, not the
  // selected window — it doesn't change as you flip the range picker.
  renderScore(state.data.violations, state.data.unmanaged, { start: startOfDay(earliestTs()), end: state.data.now });
  renderStreak(state.data.violations); // standing, all-history — not windowed
  renderReadout(violations, unblocks, unmanaged, heat, b);
  renderHeatmap(heat);
  renderWeekdayChart(violations);
  renderHourChart(violations);
  renderCalendar(violations, b);
  renderTimeline(violations, b);
  resetDayDetail();
  renderRanklist("top-keywords", tally(violations, "keyword"), "var(--danger)");
  renderRanklist("top-domains", tally(violations, "domain"), "var(--signal)");
  renderRanklist("unblock-reasons", tally(unblocks, "reason"), "var(--safe)");
  renderUnmanaged(unmanaged);
  renderDowntime(downtime);
  renderUsage(b);
  renderIgnored();
}

// ── Aggregation ─────────────────────────────────────────
function tally(rows, field) {
  const m = new Map();
  for (const r of rows) {
    const k = r[field] || "—";
    m.set(k, (m.get(k) || 0) + 1);
  }
  return [...m.entries()].map(([name, count]) => ({ name, count })).sort((a, b) => b.count - a.count);
}

// Build the weekday × hour grid of violation counts, and the parallel grid of
// "unmanaged fraction" so we can hatch slots that were mostly glocker-off.
function buildHeatmap(violations, b) {
  const counts = Array.from({ length: 7 }, () => new Array(24).fill(0));
  for (const v of violations) {
    const d = new Date(v.ts);
    counts[weekdayIndex(d)][d.getHours()]++;
  }

  // Walk the window hour-by-hour to measure how often each slot was unmanaged.
  const slotTotal = Array.from({ length: 7 }, () => new Array(24).fill(0));
  const slotUnmanaged = Array.from({ length: 7 }, () => new Array(24).fill(0));
  const periods = state.data.unmanaged;
  const end = Math.min(b.end, state.data.now);
  for (let t = startOfDay(b.start); t < end; t += HOUR) {
    const d = new Date(t);
    const wd = weekdayIndex(d);
    const hr = d.getHours();
    slotTotal[wd][hr]++;
    if (periods.some((p) => t < (p.open ? state.data.now : p.end) && t + HOUR > p.start)) {
      slotUnmanaged[wd][hr]++;
    }
  }

  let max = 0, peak = null;
  for (let wd = 0; wd < 7; wd++) {
    for (let hr = 0; hr < 24; hr++) {
      if (counts[wd][hr] > max) {
        max = counts[wd][hr];
        peak = { wd, hr, count: counts[wd][hr] };
      }
    }
  }
  const unmanagedFrac = slotTotal.map((row, wd) =>
    row.map((tot, hr) => (tot ? slotUnmanaged[wd][hr] / tot : 0))
  );
  return { counts, max, peak, unmanagedFrac };
}

// ── Color ramp for the heatmap (ember → danger) ─────────
const RAMP = [
  [12, 17, 25],    // base / none
  [90, 28, 36],
  [150, 30, 40],
  [200, 40, 45],
  [255, 90, 60],   // hottest
];
function heatColor(ratio) {
  if (ratio <= 0) return `rgb(${RAMP[0].join(",")})`;
  const eased = Math.pow(ratio, 0.6);
  const seg = Math.min(RAMP.length - 2, Math.floor(eased * (RAMP.length - 1)));
  const f = eased * (RAMP.length - 1) - seg;
  const a = RAMP[seg + 1], c = RAMP[seg]; // start ramp at index 1 for any hit
  const mix = (i) => Math.round(c[i] + (a[i] - c[i]) * f);
  // ensure any non-zero count is visibly warm, not base
  const lo = RAMP[1];
  return `rgb(${Math.max(mix(0), lo[0] * 0.6) | 0},${mix(1)},${mix(2)})`;
}

// Absolute severity colour for a per-day / per-hour violation count — fixed
// buckets, NOT relative to the window max, so a single violation reads as low
// rather than maxing out: 0 none · 1–2 low · 3–5 · 6–10 · 10+ hottest.
const COUNT_LEGEND = [
  { label: "none", count: 0 },
  { label: "1–2", count: 1 },
  { label: "3–5", count: 3 },
  { label: "6–10", count: 6 },
  { label: "10+", count: 11 },
];
function countColor(count) {
  const i = count <= 0 ? 0 : count <= 2 ? 1 : count <= 5 ? 2 : count <= 10 ? 3 : 4;
  return `rgb(${RAMP[i].join(",")})`;
}

// ── Composite health score ──────────────────────────────
// score = 100 − penalties, where each penalty is a RATE (not a raw count) so
// the number is comparable as you switch windows. Weights are the max points
// each failure mode can cost; tune them here.
const HEALTH_WEIGHTS = {
  exposure: 80, // points lost if glocker were uninstalled the entire window
  violation: 30, // points lost per (violation/day)…
  violationCap: 45, // …capped here so one wild day can't zero the score alone
  deliberate: 50, // points lost if every day were a deliberate day (>2 hits)
};

function computeHealth(violations, unmanaged, b) {
  const windowMs = Math.max(1, Math.min(b.end, state.data.now) - b.start);
  const days = Math.max(1, Math.round(windowMs / DAY));

  const unmanagedMs = unmanaged.reduce((s, p) => s + (p.end - p.start), 0);
  const exposureFrac = Math.min(1, unmanagedMs / windowMs);

  const byDay = new Map();
  for (const v of violations) byDay.set(dayKey(v.ts), (byDay.get(dayKey(v.ts)) || 0) + 1);
  const deliberateDays = [...byDay.values()].filter((c) => c > 2).length;

  const vRate = violations.length / days;
  const dFrac = deliberateDays / days;

  const penalties = {
    exposure: exposureFrac * HEALTH_WEIGHTS.exposure,
    violations: Math.min(vRate * HEALTH_WEIGHTS.violation, HEALTH_WEIGHTS.violationCap),
    deliberate: dFrac * HEALTH_WEIGHTS.deliberate,
  };
  const score = Math.max(0, Math.round(100 - penalties.exposure - penalties.violations - penalties.deliberate));

  return { score, days, exposureFrac, vRate, deliberateDays, penalties, band: bandFor(score) };
}

function bandFor(s) {
  if (s >= 90) return { label: "Excellent", color: "#34d399" };
  if (s >= 75) return { label: "Good", color: "var(--safe)" };
  if (s >= 60) return { label: "Fair", color: "var(--signal)" };
  if (s >= 40) return { label: "At risk", color: "#ff8a3c" };
  return { label: "Critical", color: "var(--danger)" };
}

function verdict(h) {
  if (h.score >= 90) return "Fully covered with almost no slips. Keep it up.";
  const drivers = [
    ["exposure", "unmanaged time — glocker was uninstalled"],
    ["violations", "frequent violations while guarded"],
    ["deliberate", "days with repeated, deliberate attempts"],
  ].sort((a, b) => h.penalties[b[0]] - h.penalties[a[0]]);
  return `Health is ${h.band.label.toLowerCase()}. Biggest drag: ${drivers[0][1]}.`;
}

function renderScore(violations, unmanaged, b) {
  const h = computeHealth(violations, unmanaged, b);
  const R = 52, C = 2 * Math.PI * R;
  const offset = C * (1 - h.score / 100);
  const color = h.band.color;

  const pens = [
    {
      label: "Unmanaged", val: h.penalties.exposure, color: "var(--exposed)",
      help: `Glocker was uninstalled ${(h.exposureFrac * 100).toFixed(1)}% of all recorded history. Exposure is weighted heaviest — up to ${HEALTH_WEIGHTS.exposure} points.`,
    },
    {
      label: "Violations", val: h.penalties.violations, color: "var(--danger)",
      help: `${h.vRate.toFixed(2)} violations/day across all history. ${HEALTH_WEIGHTS.violation} points per violation/day, capped at ${HEALTH_WEIGHTS.violationCap}.`,
    },
    {
      label: "Deliberate days", val: h.penalties.deliberate, color: "var(--danger-deep)",
      help: `${h.deliberateDays} of ${h.days} recorded days had >2 hits. Up to ${HEALTH_WEIGHTS.deliberate} points if every day were deliberate.`,
    },
  ];

  const breakdown = pens
    .map(
      (p) => `<div class="pen" title="${esc(p.help)}">
        <span class="plabel">${p.label}</span>
        <span class="pval">&minus;${p.val.toFixed(0)}</span>
        <span class="ptrack"><span class="pfill" style="width:${Math.min(100, p.val).toFixed(1)}%;background:${p.color}"></span></span>
      </div>`
    )
    .join("");

  renderScoreExplain(h);

  document.getElementById("score").innerHTML = `
    <div class="score-gauge" title="${h.score} out of 100">
      <svg viewBox="0 0 120 120" aria-hidden="true">
        <circle class="gauge-track" cx="60" cy="60" r="${R}"></circle>
        <circle class="gauge-arc" cx="60" cy="60" r="${R}"
          style="stroke:${color};stroke-dasharray:${C.toFixed(1)};stroke-dashoffset:${offset.toFixed(1)}"></circle>
      </svg>
      <div class="gauge-center">
        <span class="gauge-num" style="color:${color}">${h.score}</span>
        <span class="gauge-max">/ 100</span>
      </div>
    </div>
    <div class="score-detail">
      <div class="score-band" style="color:${color}">${h.band.label}</div>
      <p class="score-verdict">${esc(verdict(h))}</p>
      <div class="score-breakdown">${breakdown}</div>
    </div>`;
}

// Deterministic explainer for the composite score (no LLM): the formula, the
// live per-penalty breakdown, and improvement levers ranked by how many points
// each currently costs. All values come straight from computeHealth().
function renderScoreExplain(h) {
  const el = document.getElementById("score-explain");
  if (!el) return;
  const pts = (n) => n.toFixed(0);

  const levers = [
    { val: h.penalties.exposure, tip: `Keep glocker installed — it was uninstalled ${(h.exposureFrac * 100).toFixed(0)}% of all recorded time.` },
    { val: h.penalties.violations, tip: `Fewer violations — you're averaging ${h.vRate.toFixed(2)} per day across all history.` },
    { val: h.penalties.deliberate, tip: `Avoid days with more than 2 hits — ${h.deliberateDays} of ${h.days} recorded days qualify.` },
  ].filter((l) => l.val >= 1).sort((a, b) => b.val - a.val);

  const improve = levers.length
    ? levers.map((l) => `<li>${esc(l.tip)} <span class="se-gain">up to +${pts(l.val)} pts</span></li>`).join("")
    : `<li>You're near the ceiling — sustain the streak to hold it.</li>`;

  el.innerHTML = `
    <h3>How it's calculated</h3>
    <p class="se-formula">score = 100 − exposure − violations − deliberate
      <span>Each penalty is a rate over all recorded history, so the number doesn't move with the window picker.</span></p>
    <ul class="se-list">
      <li><b>Exposure &minus;${pts(h.penalties.exposure)}</b> — glocker uninstalled ${(h.exposureFrac * 100).toFixed(0)}% of all time (max ${HEALTH_WEIGHTS.exposure}).</li>
      <li><b>Violations &minus;${pts(h.penalties.violations)}</b> — ${h.vRate.toFixed(2)}/day (${HEALTH_WEIGHTS.violation} pts per /day, capped ${HEALTH_WEIGHTS.violationCap}).</li>
      <li><b>Deliberate &minus;${pts(h.penalties.deliberate)}</b> — ${h.deliberateDays} of ${h.days} days with >2 hits (max ${HEALTH_WEIGHTS.deliberate}).</li>
    </ul>
    <h3>How to improve</h3>
    <ul class="se-list se-improve">${improve}</ul>
    <p class="se-note">Improves by dilution — every clean day lowers these rates. Past events aren't erased, but their weight fades as clean history grows.</p>`;
}

// Consecutive clean days, computed over ALL history — a standing streak,
// independent of the selected window (like the health score). A day is "clean"
// only if it had no violations AND glocker was unmanaged (uninstalled) for less
// than half of it; a violation or a majority-unmanaged day breaks the streak.
// Current = clean days ending today; best = the longest clean run ever.
function computeStreaks(violations, now) {
  const today = startOfDay(now);
  const vdays = new Set(violations.map((v) => dayKey(v.ts)));
  const broke = (d) => {
    if (vdays.has(dayKey(d))) return true;
    const span = Math.min(DAY, now - d); // 24h for past days, elapsed so far for today
    return span > 0 && dayUnmanagedMs(d) > span / 2;
  };
  let best = 0, run = 0;
  for (let d = startOfDay(earliestTs()); d <= today; d += DAY) {
    if (broke(d)) run = 0;
    else { run++; if (run > best) best = run; }
  }
  return { current: run, best, violationToday: vdays.has(dayKey(today)) };
}

function renderStreak(violations) {
  const el = document.getElementById("streak");
  if (!el) return;
  const s = computeStreaks(violations, state.data.now);
  const dLabel = (n) => `${n}<small> day${n === 1 ? "" : "s"}</small>`;
  const clean = s.current > 0;
  const currentSub = clean ? "current clean run" : s.violationToday ? "violation today" : "glocker off today";
  el.innerHTML = `
    <div class="stat ${clean ? "safe" : "alarm"}">
      <div class="k">Current streak</div>
      <div class="v">${dLabel(s.current)}</div>
      <div class="sub">${currentSub}</div>
      <div class="desc">Consecutive clean days ending today — a day counts only if it had no violations and glocker ran more than half of it. Over all history, not the window.</div>
    </div>
    <div class="stat">
      <div class="k">Best streak</div>
      <div class="v">${dLabel(s.best)}</div>
      <div class="sub">all-time best</div>
      <div class="desc">Longest run of consecutive clean days ever recorded.</div>
    </div>`;
}

// ── Renderers ───────────────────────────────────────────
function renderReadout(violations, unblocks, unmanaged, heat, b) {
  const days = Math.max(1, Math.round((b.end - b.start) / DAY));
  const byDay = new Map();
  for (const v of violations) byDay.set(dayKey(v.ts), (byDay.get(dayKey(v.ts)) || 0) + 1);
  const deliberateDays = [...byDay.values()].filter((c) => c > 2).length; // glockpeek's "egregious" threshold
  const cleanDays = days - byDay.size;

  const unmanagedMs = unmanaged.reduce((s, p) => s + (p.end - p.start), 0);

  // Computer-on coverage: how much of the window we have usage data for. Both
  // active and idle samples mean the machine was powered on and being tracked,
  // so trackedMs (active + idle) is a proxy for "computer was on".
  const usageAvailable = !!(state.data.sources && state.data.sources.usage);
  const onMs = usageAvailable
    ? computeUsage((state.data.usage || []).filter((e) => within(e.ts, b))).trackedMs
    : 0;
  const windowMs = b.end - b.start;
  const onPct = windowMs > 0 ? Math.round((onMs / windowMs) * 100) : 0;

  const peakLabel = heat.peak
    ? `${WEEKDAYS[heat.peak.wd]} ${String(heat.peak.hr).padStart(2, "0")}:00`
    : "—";

  const stats = [
    {
      k: "Violations", v: violations.length, sub: `${(violations.length / days).toFixed(1)}/day`,
      help: "Total blocked-keyword hits (URL + page content) reported by the browser extension in the selected window.",
    },
    {
      k: "Deliberate days", v: deliberateDays, sub: `>2 hits · of ${days}d`, cls: deliberateDays ? "alarm" : "",
      help: "Days with more than 2 violations. glockpeek treats >2 hits in a day as a deliberate attempt rather than an accidental one.",
    },
    {
      k: "Clean days", v: Math.max(0, cleanDays), sub: `${Math.round((Math.max(0, cleanDays) / days) * 100)}% of window`,
      help: "Days in the window with zero recorded violations.",
    },
    {
      k: "Peak exposure", v: peakLabel, sub: heat.peak ? `${heat.peak.count} hits` : "no data", cls: "peak", big: false,
      help: "The weekday + hour slot with the most violations — the time you are statistically most vulnerable to failure.",
    },
    {
      k: "Unmanaged", v: fmtDur(unmanagedMs), sub: `${unmanaged.length} span(s)`, cls: unmanagedMs ? "alarm" : "",
      help: "Total time glocker was uninstalled (not enforcing) in the window. This counts as exposure — no blocking was active.",
    },
    {
      k: "Unblocks", v: unblocks.length, sub: "deliberate grants",
      help: "Number of temporary unblock grants you requested (e.g. glocker -unblock) in the window.",
    },
    {
      k: "Computer on", v: usageAvailable ? fmtDur(onMs) : "—",
      sub: usageAvailable ? `${onPct}% of ${fmtDur(windowMs)}` : "no usage data",
      help: "Time the computer was on and being tracked (active + idle) in the window, from the usage log — i.e. how much of the total window there is activity data for.",
    },
  ];

  document.getElementById("readout").innerHTML = stats
    .map(
      (s) => `<div class="stat ${s.cls || ""}">
        <div class="k">${s.k}</div>
        <div class="v" ${s.big === false ? 'style="font-size:20px"' : ""}>${s.v}</div>
        <div class="sub">${s.sub}</div>
        <div class="desc">${esc(s.help)}</div>
      </div>`
    )
    .join("");
}

function renderHeatmap(heat) {
  const el = document.getElementById("heatmap");
  const cells = [`<div class="hm-corner"></div>`];
  for (let hr = 0; hr < 24; hr++) {
    cells.push(`<div class="hm-hour">${hr % 3 === 0 ? String(hr).padStart(2, "0") : ""}</div>`);
  }
  for (let wd = 0; wd < 7; wd++) {
    cells.push(`<div class="hm-day">${WEEKDAYS[wd]}</div>`);
    for (let hr = 0; hr < 24; hr++) {
      const count = heat.counts[wd][hr];
      const ratio = heat.max ? count / heat.max : 0;
      const isPeak = heat.peak && heat.peak.wd === wd && heat.peak.hr === hr && count > 0;
      const unmanaged = heat.unmanagedFrac[wd][hr] >= 0.5;
      const cls = `hm-cell${isPeak ? " peak" : ""}${unmanaged ? " unmanaged" : ""}`;
      const bg = count > 0 ? `background:${heatColor(ratio)}` : "";
      const title = `${WEEKDAYS[wd]} ${String(hr).padStart(2, "0")}:00 · ${count} violation${count === 1 ? "" : "s"}${unmanaged ? " · mostly unmanaged" : ""}`;
      cells.push(`<div class="${cls}" style="${bg}" title="${title}"></div>`);
    }
  }
  el.innerHTML = cells.join("");

  document.getElementById("heatmap-legend").innerHTML = `
    <span class="legend-scale">less
      ${RAMP.map((_, i) => `<i style="background:${heatColor(i / (RAMP.length - 1))}"></i>`).join("")}
      more</span>
    <span class="legend-key"><span class="swatch" style="box-shadow:0 0 0 1.5px var(--signal)"></span> peak slot</span>
    <span class="legend-key"><span class="swatch hatch"></span> mostly unmanaged (glocker off)</span>`;
}

function renderWeekdayChart(violations) {
  const data = new Array(7).fill(0);
  for (const v of violations) data[weekdayIndex(new Date(v.ts))]++;
  barChart("chart-weekday", WEEKDAYS, data, "var(--danger)");
}

function renderHourChart(violations) {
  const data = new Array(24).fill(0);
  for (const v of violations) data[new Date(v.ts).getHours()]++;
  const labels = Array.from({ length: 24 }, (_, h) => (h % 2 === 0 ? String(h).padStart(2, "0") : ""));
  barChart("chart-hour", labels, data, "var(--signal)");
}

function renderTimeline(violations, b) {
  const days = Math.round((b.end - b.start) / DAY) + 1;
  const labels = [], data = [], dayTs = [];
  const counts = new Map();
  for (const v of violations) counts.set(dayKey(v.ts), (counts.get(dayKey(v.ts)) || 0) + 1);
  for (let i = 0; i < days; i++) {
    const ts = b.start + i * DAY;
    const d = new Date(ts);
    labels.push(d.getDate() === 1 || i === 0 ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "");
    data.push(counts.get(dayKey(ts)) || 0);
    dayTs.push(ts);
  }
  // Vertical gridline density adapts to the window: every day for short
  // windows, weekly once daily would crowd, monthly for long spans.
  const granularity = days <= 45 ? "day" : days <= 180 ? "week" : "month";
  const gridAt = (i) => {
    const ts = dayTs[i];
    if (ts == null) return false;
    if (granularity === "day") return true;
    const d = new Date(ts);
    return granularity === "week" ? weekdayIndex(d) === 0 : d.getDate() === 1;
  };

  // Hovering a point fills the day inspector with that day's breakdown.
  lineChart("chart-timeline", labels, data, {
    onPoint: (index) => {
      if (index != null && dayTs[index] != null) showDay(dayTs[index]);
    },
    gridAt,
  });
}

// Milliseconds of a given day [t, t+DAY) that glocker was unmanaged, clipped to
// each span (and to "now" for the still-open span / for today's partial day).
function dayUnmanagedMs(t) {
  let ms = 0;
  for (const p of state.data.unmanaged) {
    const pEnd = p.open ? state.data.now : p.end;
    const s = Math.max(p.start, t);
    const e = Math.min(pEnd, t + DAY);
    if (e > s) ms += e - s;
  }
  return ms;
}

const HOUR_MS = 3600000;

function renderCalendar(violations, b) {
  const counts = new Map();
  for (const v of violations) counts.set(dayKey(v.ts), (counts.get(dayKey(v.ts)) || 0) + 1);
  const periods = state.data.unmanaged;
  const today = startOfDay(state.data.now);
  const winStart = startOfDay(b.start);
  const winEnd = Math.min(startOfDay(b.end), today);

  // One card per calendar month spanned by the window.
  const months = [];
  let m = new Date(winStart);
  m = new Date(m.getFullYear(), m.getMonth(), 1);
  const lastMonth = new Date(winEnd);
  while (m <= lastMonth) {
    months.push(renderMonth(m, counts, winStart, winEnd, today));
    m = new Date(m.getFullYear(), m.getMonth() + 1, 1);
  }

  const legend = `<div class="cal-legend">
    <span class="legend-scale">${COUNT_LEGEND.map((s) => `<span class="lvl"><i style="background:${countColor(s.count)}"></i>${s.label}</span>`).join("")}</span>
    <span class="legend-key"><span class="swatch clean-swatch"></span> clean &amp; managed</span>
    <span class="legend-key"><span class="swatch hatch"></span> unmanaged (proportion of day, &gt;1h)</span>
    <span class="legend-key"><span class="swatch" style="box-shadow:0 0 0 1.5px var(--signal)"></span> today</span>
  </div>`;

  document.getElementById("calendar").innerHTML = months.join("") + legend;
}

function renderMonth(monthStart, counts, winStart, winEnd, today) {
  const year = monthStart.getFullYear();
  const month = monthStart.getMonth();
  const daysInMonth = new Date(year, month + 1, 0).getDate();
  const lead = weekdayIndex(monthStart); // Mon=0 blanks before day 1

  const wdHeader = WEEKDAYS.map((d) => `<div class="cal-wd">${d[0]}</div>`).join("");

  const cells = [];
  for (let i = 0; i < lead; i++) cells.push(`<div class="cal-day blank"></div>`);

  let monthTotal = 0;
  for (let day = 1; day <= daysInMonth; day++) {
    const t = new Date(year, month, day).getTime();
    const count = counts.get(dayKey(t)) || 0;
    const inWindow = t >= winStart && t <= winEnd;
    if (inWindow) monthTotal += count;

    // Hatch only the fraction of the day that was unmanaged, and only when it
    // exceeds an hour (below that it's usually just an upgrade blip).
    const unmMs = inWindow ? dayUnmanagedMs(t) : 0;
    const hatched = unmMs > HOUR_MS;
    const frac = Math.min(1, unmMs / DAY);
    // A fully good day: in-window, no violations, and glocker was managing it
    // (no more than a brief upgrade blip of unmanaged time).
    const clean = inWindow && count === 0 && unmMs <= HOUR_MS;

    const cls = [
      "cal-day",
      inWindow ? "" : "out",
      count > 0 && inWindow ? "has" : "",
      clean ? "clean" : "",
      t === today ? "today" : "",
    ].filter(Boolean).join(" ");
    const bg = inWindow && count > 0 ? `background:${countColor(count)}` : "";
    const label = new Date(t).toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" });
    const title = inWindow
      ? `${label} · ${count} violation${count === 1 ? "" : "s"}${unmMs > 0 ? ` · ${fmtDur(unmMs)} unmanaged` : ""}${clean ? " · clean & managed ✓" : ""}`
      : `${label} · outside selected window`;
    const data = inWindow ? ` data-ts="${t}"` : "";
    const hatch = hatched ? `<span class="cal-hatch" style="height:${Math.round(frac * 100)}%"></span>` : "";
    cells.push(`<div class="${cls}" style="${bg}" title="${title}"${data}>${hatch}<span class="cal-daynum">${day}</span></div>`);
  }

  const heading = monthStart.toLocaleDateString(undefined, { month: "long", year: "numeric" });
  return `<div class="cal-month">
    <h3>${heading} <span class="mtotal">${monthTotal} hit${monthTotal === 1 ? "" : "s"}</span></h3>
    <div class="cal-weekdays">${wdHeader}</div>
    <div class="cal-days">${cells.join("")}</div>
  </div>`;
}

// ── Day inspector (shared by calendar + timeline hover) ──
function resetDayDetail() {
  document.getElementById("day-detail").innerHTML =
    `<div class="dd-empty">Click a day in the calendar or a point on the timeline for a breakdown of that day's violations.</div>`;
}

// ── False-positive (ignored violation) handling ─────────
// Non-destructive: entries are hidden via /api/ignored; the raw reports log is
// never touched. Identity is ts + keyword + url (matches the server).
const ignoreKeyOf = (e) => `${e.ts} ${e.keyword} ${e.url}`;

async function putIgnored() {
  try {
    const res = await fetch("api/ignored", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ignored: state.ignored }),
    });
    if (res.ok) state.ignored = (await res.json()).ignored || [];
  } catch { /* keep local list; a reload will reconcile */ }
}

// Refetch the data (now filtered) and re-render, re-opening the same day.
async function reloadData() {
  try {
    const res = await fetch("api/data");
    if (!res.ok) return;
    state.data = await res.json();
    render();
    if (state.openDay != null) showDay(state.openDay);
  } catch { /* keep current view */ }
}

async function ignoreViolation(entry) {
  const key = ignoreKeyOf(entry);
  if (!state.ignored.some((e) => ignoreKeyOf(e) === key)) state.ignored.push(entry);
  await putIgnored();
  await reloadData();
}

async function restoreViolation(entry) {
  const key = ignoreKeyOf(entry);
  state.ignored = state.ignored.filter((e) => ignoreKeyOf(e) !== key);
  await putIgnored();
  await reloadData();
}

function renderIgnored() {
  const el = document.getElementById("ignored-list");
  if (!el) return;
  if (!state.ignored.length) {
    el.innerHTML = `<div class="empty">none — mark a hit &ldquo;not a violation&rdquo; in a day's detail to hide it here</div>`;
    return;
  }
  el.innerHTML = [...state.ignored]
    .sort((a, b) => b.ts - a.ts)
    .map((e) => {
      const when = new Date(e.ts).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
      return `<div class="ign">
        <span class="ign-when">${when}</span>
        <span class="ign-kw">${esc(e.keyword)}</span>
        <span class="ign-dom" title="${esc(e.url)}">${esc(e.domain || "—")}</span>
        <button class="ign-restore" type="button" title="Restore this violation" data-ts="${e.ts}" data-kw="${esc(e.keyword)}" data-url="${esc(e.url)}">&#8635;</button>
      </div>`;
    })
    .join("");
}

function showDay(dayTs) {
  state.openDay = dayTs; // remembered so re-fetches can re-open the same day
  const k = dayKey(dayTs);
  const hits = (state.dayIndex.get(k) || []).slice().sort((a, b) => a.ts - b.ts);
  const dateLabel = new Date(dayTs).toLocaleDateString(undefined, {
    weekday: "long", month: "long", day: "numeric", year: "numeric",
  });

  const t = startOfDay(dayTs);
  const elapsed = Math.max(HOUR_MS, Math.min(t + DAY, state.data.now) - t); // day so far
  const unmMs = dayUnmanagedMs(t);
  const managedMs = Math.max(0, elapsed - unmMs);
  const unmPct = Math.round((unmMs / elapsed) * 100);
  const fullyUnmanaged = managedMs < HOUR_MS && unmMs > 0;
  const deliberate = hits.length > 2;

  const badges = [];
  if (hits.length) badges.push(`<span class="dd-count">${hits.length} violation${hits.length === 1 ? "" : "s"}</span>`);
  if (deliberate) badges.push(`<span class="badge alarm" title="More than 2 violations in a day — treated as deliberate">deliberate</span>`);
  if (unmMs > HOUR_MS) badges.push(`<span class="badge exposed" title="glocker was uninstalled for part of this day">unmanaged</span>`);
  const head = `<div class="dd-head"><span class="dd-date">${dateLabel}</span><span class="dd-badges">${badges.join("")}</span></div>`;

  // Coverage line — always shown when any of the day was unmanaged, so the
  // managed remainder's stats are never hidden behind an "unmanaged" label.
  let coverage = "";
  if (unmMs > 0) {
    const spans = state.data.unmanaged.filter((p) => t < (p.open ? state.data.now : p.end) && t + DAY > p.start);
    const reasons = [...new Set(spans.map((p) => [p.reason, p.note].filter(Boolean).join(" — ")).filter(Boolean))].join(", ");
    coverage = `<div class="dd-cov">
      <span class="cov-unm">Unmanaged <b>${fmtDur(unmMs)}</b> (${unmPct}%)</span>
      <span class="cov-man">Managed ${fmtDur(managedMs)} · ${hits.length} violation${hits.length === 1 ? "" : "s"}</span>
      ${reasons ? `<span class="cov-why">Reason: ${esc(reasons)}</span>` : ""}
    </div>`;
  }

  let body;
  if (hits.length) {
    const kw = miniRank(tally(hits, "keyword"), "var(--danger)");
    const dom = miniRank(tally(hits, "domain"), "var(--signal)");
    const list = hits
      .map((h) => {
        const time = new Date(h.ts).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
        const kind = h.type === "url-keyword" ? "url" : "page";
        return `<div class="hit">
          <span class="ht">${time}</span>
          <span class="hk">${esc(h.keyword)}</span>
          <span class="hd" title="${esc(h.url)}">${esc(h.domain || "—")} <em>${kind}</em></span>
          <button class="hit-x" type="button" title="Not a violation — hide it" data-ts="${h.ts}" data-kw="${esc(h.keyword)}" data-url="${esc(h.url)}" data-dom="${esc(h.domain || "")}">&times;</button>
        </div>`;
      })
      .join("");
    body = coverage + `<div class="dd-grid">
      <div class="dd-sum">
        <h4>Keywords</h4>${kw}
        <h4>Domains</h4>${dom}
      </div>
      <div class="dd-hits"><h4>Hits (${hits.length})</h4><div class="hit-list">${list}</div></div>
    </div>`;
  } else if (fullyUnmanaged) {
    body = coverage + `<div class="dd-note exposed-note">glocker was unmanaged all day — no activity was recorded.</div>`;
  } else if (unmMs > 0) {
    body = coverage + `<div class="dd-note clean-note">No violations during the managed part of the day ✓</div>`;
  } else {
    body = `<div class="dd-note clean-note">No violations — clean day ✓</div>`;
  }

  document.getElementById("day-detail").innerHTML = head + dayHourTimeline(t, hits) + body;
}

// A 24-hour strip for the day inspector: violations per hour (heat-coloured),
// unmanaged hours hatched, hours later than "now" dimmed. Echoes the time-of-day
// view of `glockpeek -detail` at hourly granularity.
function dayHourTimeline(dayStart, hits) {
  const counts = new Array(24).fill(0);
  for (const h of hits) counts[new Date(h.ts).getHours()]++;
  const periods = state.data.unmanaged;
  const now = state.data.now;

  const cells = [];
  for (let h = 0; h < 24; h++) {
    const hStart = dayStart + h * HOUR_MS;
    const hEnd = hStart + HOUR_MS;
    const future = hStart >= now;
    const count = counts[h];
    const unm = !future && periods.some((p) => hStart < (p.open ? now : p.end) && hEnd > p.start);
    const cls = `dd-hcell${future ? " future" : ""}${unm ? " unmanaged" : ""}`;
    const bg = count > 0 && !future ? `background:${countColor(count)}` : "";
    const hh = String(h).padStart(2, "0");
    const title = future
      ? `${hh}:00 · later today`
      : `${hh}:00 · ${count} violation${count === 1 ? "" : "s"}${unm ? " · unmanaged" : ""}`;
    cells.push(`<div class="${cls}" style="${bg}" title="${title}"></div>`);
  }
  const axis = Array.from({ length: 24 }, (_, h) => `<span>${h % 6 === 0 ? String(h).padStart(2, "0") : ""}</span>`).join("");
  return `<div class="dd-hours">
    <div class="dd-hours-row">${cells.join("")}</div>
    <div class="dd-hours-axis">${axis}</div>
  </div>`;
}

function miniRank(items, color) {
  if (!items.length) return `<div class="empty">none</div>`;
  const max = items[0].count;
  return items
    .slice(0, 6)
    .map(
      (it) => `<div class="mrank">
        <span class="mlabel" title="${esc(it.name)}">${esc(it.name)}</span>
        <span class="mbar"><span style="width:${Math.round((it.count / max) * 100)}%;background:${color}"></span></span>
        <span class="mcount">${it.count}</span>
      </div>`
    )
    .join("");
}

function renderRanklist(id, items, color) {
  const el = document.getElementById(id);
  if (!items.length) {
    el.innerHTML = `<div class="empty">no data in window</div>`;
    return;
  }
  const max = items[0].count;
  el.innerHTML = items
    .slice(0, 8)
    .map((it) => {
      const pct = Math.round((it.count / max) * 100);
      return `<div class="rank">
        <span class="label" title="${esc(it.name)}">${esc(it.name)}</span>
        <span class="count">${it.count}</span>
        <span class="track"><span class="fill" style="width:${pct}%;background:${color}"></span></span>
      </div>`;
    })
    .join("");
}

function renderUnmanaged(unmanaged) {
  const el = document.getElementById("unmanaged-list");
  if (!unmanaged.length) {
    el.innerHTML = `<div class="empty">none in window — fully covered ✓</div>`;
    return;
  }
  el.innerHTML = [...unmanaged]
    .sort((a, b) => b.start - a.start)
    .map((p) => {
      const why = [p.reason, p.note].filter(Boolean).join(" — ") || "no reason given";
      const when = new Date(p.start).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
      return `<div class="um ${p.open ? "open" : ""}">
        <span class="when">${when}</span>
        <span class="why">${esc(why)}</span>
        <span class="dur">${fmtDur(p.end - p.start)}</span>
      </div>`;
    })
    .join("");
}

// Observed downtime from the glockdoc heartbeat watchdog. Unlike "unmanaged"
// (derived from clean uninstall/install events), these are spans the watchdog
// actually saw glocker not answering — so they also catch crashes and unclean
// stops, and can register even when no uninstall was recorded.
function renderDowntime(downtime) {
  const el = document.getElementById("downtime-list");
  if (!el) return;
  if (!state.data.sources || !state.data.sources.heartbeat) {
    el.innerHTML = `<div class="empty">no watchdog data yet</div>`;
    return;
  }
  if (!downtime.length) {
    el.innerHTML = `<div class="empty">none observed — watchdog saw glocker up ✓</div>`;
    return;
  }
  el.innerHTML = [...downtime]
    .sort((a, b) => b.start - a.start)
    .map((p) => {
      const when = new Date(p.start).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
      return `<div class="um ${p.open ? "open" : ""}">
        <span class="when">${when}</span>
        <span class="why">${p.open ? "still down" : "watchdog observed down"}</span>
        <span class="dur">${fmtDur(p.end - p.start)}</span>
      </div>`;
    })
    .join("");
}

// ── Usage metrics ───────────────────────────────────────
// The usage log samples the focused window + idle time every ~10s. We turn the
// samples into durations by attributing the gap until the next sample to that
// sample's active window — unless the user was idle (AFK) or the tracker had a
// gap (capped so a tracker restart doesn't count as hours of use).
const USAGE_IDLE_THRESHOLD_MS = 60_000; // idle ≥ 60s → counted as away, not active
const USAGE_MAX_GAP_MS = 5 * 60_000;    // cap per-sample attribution across gaps

// Attribute each sample's duration (the gap until the next sample, capped) and
// call fn(entry, dt, away). `away` folds in idle and no-focus so both the raw
// metrics and the rule categorizer share one definition of "active".
// idleMs === -1 means the screensaver extension was unavailable; treat that as
// active since we can't prove otherwise.
function eachSampleDuration(entries, fn) {
  const gaps = [];
  for (let i = 1; i < entries.length; i++) gaps.push(entries[i].ts - entries[i - 1].ts);
  gaps.sort((a, b) => a - b);
  const nominal = gaps.length ? gaps[Math.floor(gaps.length / 2)] : 10_000;

  for (let i = 0; i < entries.length; i++) {
    const e = entries[i];
    let dt = i + 1 < entries.length ? entries[i + 1].ts - e.ts : nominal;
    if (dt <= 0) continue;
    dt = Math.min(dt, USAGE_MAX_GAP_MS);
    const away = e.idleMs >= USAGE_IDLE_THRESHOLD_MS || !e.active;
    fn(e, dt, away);
  }
}

const rankMap = (m) => [...m.entries()].map(([name, ms]) => ({ name, ms })).sort((a, b) => b.ms - a.ms);

function computeUsage(entries) {
  const byApp = new Map();
  const byTitle = new Map();
  let activeMs = 0, idleMs = 0;

  eachSampleDuration(entries, (e, dt, away) => {
    if (away) {
      idleMs += dt;
      return;
    }
    activeMs += dt;
    const app = (e.active.class || "").trim() || "unknown";
    byApp.set(app, (byApp.get(app) || 0) + dt);
    const title = e.active.title || "—";
    byTitle.set(title, (byTitle.get(title) || 0) + dt);
  });

  return {
    activeMs,
    idleMs,
    trackedMs: activeMs + idleMs,
    apps: rankMap(byApp),
    titles: rankMap(byTitle),
  };
}

// ── Rule-based categorization (arbtt-style) ─────────────
// Rules are applied client-side so edits recategorize instantly. Each rule is
// { program, title, tag }: optional case-insensitive regexes on the active
// window's class and title. The first rule whose present regexes all match
// wins; $1..$9 in the tag interpolate that rule's regex captures.
function compileRules(rules) {
  return rules.map((r) => {
    let program = null, title = null, error = false;
    try { if (r.program) program = new RegExp(r.program, "i"); } catch { error = true; }
    try { if (r.title) title = new RegExp(r.title, "i"); } catch { error = true; }
    return { program, title, tag: (r.tag || "").trim(), error };
  });
}

function interpolateTag(tag, match) {
  if (!match) return tag;
  return tag.replace(/\$([1-9])/g, (_, n) => match[Number(n)] || "");
}

function tagForSample(e, compiled) {
  if (!e.active) return null;
  const cls = e.active.class || "";
  const title = e.active.title || "";
  for (const c of compiled) {
    if (c.error || !c.tag) continue;
    let pm = null, tm = null;
    if (c.program) { pm = c.program.exec(cls); if (!pm) continue; }
    if (c.title) { tm = c.title.exec(title); if (!tm) continue; }
    return interpolateTag(c.tag, tm || pm); // named/numbered captures from the last regex to match
  }
  return null;
}

const UNTAGGED = "(untagged)";

function computeTagUsage(entries, compiled) {
  const byTag = new Map();
  const hourByTag = new Map(); // tag -> [24] ms, for the stacked hour-of-day chart
  const dayByTag = new Map();  // tag -> Map(dayKey -> ms), for the per-day stack
  const untaggedByWindow = new Map(); // "program\x00title" -> { program, title, ms }
  let untagged = 0, active = 0;

  const addHour = (name, hr, dt) => {
    let a = hourByTag.get(name);
    if (!a) { a = new Array(24).fill(0); hourByTag.set(name, a); }
    a[hr] += dt;
  };
  const addDay = (name, dk, dt) => {
    let m = dayByTag.get(name);
    if (!m) { m = new Map(); dayByTag.set(name, m); }
    m.set(dk, (m.get(dk) || 0) + dt);
  };
  // Per-tag contributing windows (program + title), for the tag drill-down.
  const windowsByTag = new Map(); // tag -> Map(key -> { program, title, ms })
  const noTitle = "—";
  const addWindow = (name, e, dt) => {
    const program = (e.active.class || "").trim() || "unknown";
    const title = e.active.title || noTitle;
    const wkey = program + " " + title;
    let m = windowsByTag.get(name);
    if (!m) { m = new Map(); windowsByTag.set(name, m); }
    let w = m.get(wkey);
    if (!w) { w = { program, title, ms: 0 }; m.set(wkey, w); }
    w.ms += dt;
  };

  eachSampleDuration(entries, (e, dt, away) => {
    if (away) return;
    active += dt;
    const hr = new Date(e.ts).getHours();
    const dk = dayKey(e.ts);
    const tag = tagForSample(e, compiled);
    if (tag) {
      byTag.set(tag, (byTag.get(tag) || 0) + dt);
      addHour(tag, hr, dt);
      addDay(tag, dk, dt);
      addWindow(tag, e, dt);
    } else {
      untagged += dt;
      addHour(UNTAGGED, hr, dt);
      addDay(UNTAGGED, dk, dt);
      addWindow(UNTAGGED, e, dt);
      // Track each untagged window (program + title) separately so the user can
      // see and tag individual windows, not a collapsed per-program summary.
      const program = (e.active.class || "").trim() || "unknown";
      const title = e.active.title || "—";
      const key = program + " " + title;
      let u = untaggedByWindow.get(key);
      if (!u) { u = { program, title, ms: 0 }; untaggedByWindow.set(key, u); }
      u.ms += dt;
    }
  });

  // Rank individual untagged windows by active time (most-used first).
  const untaggedItems = [...untaggedByWindow.values()].sort((a, b) => b.ms - a.ms);

  return { tags: rankMap(byTag), untagged, active, hourByTag, dayByTag, windowsByTag, untaggedItems };
}

// Canonical ordering shared by the tag pie and the stacked hour chart, so a tag
// keeps the same colour in both. Tags sorted by total desc, untagged last-ish
// (sorted in by size but flagged so it always renders grey).
function orderedTagItems(tu) {
  const items = tu.tags.slice();
  if (tu.untagged > 0) items.push({ name: UNTAGGED, ms: tu.untagged, untagged: true });
  items.sort((a, b) => b.ms - a.ms);
  return items;
}

// A tag's colour is the user's saved override if any, else a stable palette
// slot by rank. Untagged is always the muted faint ink.
function tagColorMap(items) {
  const map = new Map();
  let ci = 0;
  for (const it of items) {
    if (it.untagged) {
      map.set(it.name, cssVar("var(--ink-faint)"));
      continue;
    }
    const fallback = TAG_COLORS[ci++ % TAG_COLORS.length];
    map.set(it.name, state.tagColors[it.name] || fallback);
  }
  return map;
}

function renderUsage(b) {
  const available = state.data.sources && state.data.sources.usage;
  const entries = (state.data.usage || []).filter((e) => within(e.ts, b));
  // Stash the in-window samples so rule edits can recategorize without a
  // full re-render of the rest of the view.
  state.usageWindow = { entries, available, b };
  const u = computeUsage(entries);

  renderUsageReadout(u, available);
  recategorize();
}

// Recompute and render only the "Time by tag" panel from the current window
// and rules — cheap enough to run on every keystroke in the rule editor.
function recategorize({ colorsOnly = false } = {}) {
  const win = state.usageWindow;
  if (!win) return;
  const tu = computeTagUsage(win.entries, compileRules(state.rules));
  renderUsagePerDay(tu, win.available, win.b);
  renderUsageTags(tu, win.available);
  renderUsageTagHours(tu, win.available);
  renderUntagged(tu, win.available);
  // Skip re-rendering the colour pickers when a picker itself triggered this,
  // so the open colour dialog / focus isn't disturbed mid-drag.
  if (!colorsOnly) renderTagColors(tu, win.available);
}

// One colour swatch per tag (excluding untagged), shown in the Rules panel.
// Editing recolours the charts live; the value is saved with the rules.
function renderTagColors(tu, available) {
  const el = document.getElementById("tag-colors");
  if (!el) return;
  const ordered = orderedTagItems(tu);
  const items = ordered.filter((it) => !it.untagged);
  if (!available || !items.length) {
    el.innerHTML = `<span class="tagcolor-empty">Tag colours appear here once rules produce tags.</span>`;
    return;
  }
  const colors = tagColorMap(ordered);
  el.innerHTML = items
    .map(
      (it) => `<label class="tagcolor" title="Colour for ${esc(it.name)}">
        <input type="color" data-tagcolor="${esc(it.name)}" value="${colors.get(it.name)}" />
        <span>${esc(it.name)}</span>
      </label>`
    )
    .join("");
}

// The active time no rule matched, grouped by program with an example title, so
// the user can see what still needs a rule (and click to start one).
function renderUntagged(tu, available) {
  const el = document.getElementById("usage-untagged");
  if (!available) {
    el.innerHTML = `<div class="empty">usage tracking is off</div>`;
    return;
  }
  const items = tu.untaggedItems || [];
  if (!items.length) {
    el.innerHTML = `<div class="empty">nothing untagged — full coverage ✓</div>`;
    return;
  }
  const max = items[0].ms || 1;
  el.innerHTML = items
    .slice(0, 10)
    .map((it) => {
      const pct = Math.round((it.ms / max) * 100);
      return `<div class="untag">
        <div class="untag-info">
          <span class="untag-prog">${esc(it.program)}</span>
          <span class="untag-title" title="${esc(it.title)}">${esc(it.title)}</span>
        </div>
        <span class="untag-time">${esc(fmtDur(it.ms))}</span>
        <button class="untag-tag" type="button" data-tag-program="${esc(it.program)}" data-tag-title="${esc(it.title)}" title="Start a rule for this window">&plus; tag</button>
        <span class="untag-bar"><span style="width:${pct}%"></span></span>
      </div>`;
    })
    .join("");
}

// Start a rule for a specific window: append a prefilled row (program anchored,
// title as an escaped starting point the user can generalize), focus its tag
// field, and let the user name it and Save.
function startRuleForWindow(program, title) {
  syncRulesFromDom();
  state.rules.push({
    program: program ? `^${escapeRegex(program)}$` : "",
    title: title && title !== "—" ? escapeRegex(title) : "",
    tag: "",
  });
  renderRulesEditor();
  markRuleValidity();
  const rows = document.querySelectorAll("#rules-rows .rule-row");
  const last = rows[rows.length - 1];
  if (last) {
    last.scrollIntoView({ behavior: "smooth", block: "center" });
    last.querySelector('[data-field="tag"]').focus();
  }
  recategorize();
}

function escapeRegex(s) {
  return String(s).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// Categorical palette for the tag pie (distinct hues that read on the dark
// theme). "(untagged)" is always rendered in the muted faint ink instead.
const TAG_COLORS = [
  "#f0a020", "#2f9e8f", "#4a90d9", "#c0468f", "#7bc86c",
  "#9b6dff", "#ff4d4d", "#e8734a", "#54c7d6", "#c9a227",
];

function renderUsageTags(tu, available) {
  const wrap = document.getElementById("usage-tags-wrap");
  const items = orderedTagItems(tu); // already sorted by ms desc == percentage desc
  if (!available || !items.length) {
    destroy("chart-usage-tags");
    wrap.innerHTML = `<div class="empty">${available ? "no active time in window" : "usage tracking is off"}</div>`;
    renderTagContributors(null, tu);
    return;
  }
  // Re-create the canvas if a previous empty state replaced it.
  if (!document.getElementById("chart-usage-tags")) {
    wrap.innerHTML = `<canvas id="chart-usage-tags"></canvas>`;
  }
  const total = items.reduce((s, it) => s + it.ms, 0) || 1;
  const colors = tagColorMap(items);
  hbarChart("chart-usage-tags", {
    labels: items.map((it) => it.name),
    pct: items.map((it) => (100 * it.ms) / total),
    ms: items.map((it) => it.ms),
    colors: items.map((it) => colors.get(it.name)),
    onSelect: (i) => renderTagContributors(items[i].name, tu),
  });
  // Default drill-down: keep the current selection if still present, else top tag.
  const names = items.map((it) => it.name);
  renderTagContributors(names.includes(state.selectedTag) ? state.selectedTag : names[0], tu);
}

// Drill-down for "Time by tag": a tree of what fed `tagName`. Top level is apps
// (programs) grouped with their total time; clicking an app expands its window
// titles inline below it, so you can see exactly what an app's time went on and
// decide what to re-tag. Expanded apps are tracked in state.expandedApps.
function renderTagContributors(tagName, tu) {
  const el = document.getElementById("usage-tag-detail");
  const head = document.getElementById("usage-tag-detail-head");
  if (!el) return;
  state.lastTagUsage = tu;
  if (!tagName) {
    if (head) head.textContent = "Tag contributors";
    el.innerHTML = `<div class="empty">no tags yet</div>`;
    return;
  }
  // Collapse and clear the selected program when switching to a different tag.
  if (tagName !== state.selectedTag) { state.expandedApps = new Set(); state.selectedProgram = null; }
  state.selectedTag = tagName;
  if (head) head.textContent = `Apps in "${tagName}"`;

  const windows = (tu && tu.windowsByTag && tu.windowsByTag.get(tagName)) || new Map();

  // Group windows by program, keeping each program's own window list.
  const progs = new Map();
  for (const w of windows.values()) {
    let p = progs.get(w.program);
    if (!p) { p = { program: w.program, ms: 0, wins: [] }; progs.set(w.program, p); }
    p.ms += w.ms;
    p.wins.push(w);
  }
  const apps = [...progs.values()].sort((a, b) => b.ms - a.ms);

  if (!apps.length) {
    el.innerHTML = `<div class="empty">nothing recorded</div>`;
    renderProgramWordsPie(null, null);
    return;
  }

  const max = apps[0].ms || 1;
  el.innerHTML = apps.map((app) => {
    const open = state.expandedApps.has(app.program);
    const selected = app.program === state.selectedProgram;
    const pct = Math.round((app.ms / max) * 100);
    const wins = app.wins.slice().sort((a, b) => b.ms - a.ms);
    const children = open
      ? wins.map((w) => `<div class="tt-win">
          <span class="tt-wtitle" title="${esc(w.title)}">${esc(w.title)}</span>
          <span class="tt-wtime">${esc(fmtDur(w.ms))}</span>
        </div>`).join("")
      : "";
    return `<div class="tt-group${open ? " open" : ""}">
      <div class="tt-app${selected ? " selected" : ""}" data-app="${esc(app.program)}">
        <span class="tt-caret">▸</span>
        <div class="tt-info">
          <span class="tt-name">${esc(app.program)}</span>
          <span class="tt-sub">${app.wins.length} window${app.wins.length === 1 ? "" : "s"}</span>
        </div>
        <span class="tt-time">${esc(fmtDur(app.ms))}</span>
        <span class="tt-bar"><span style="width:${pct}%"></span></span>
      </div>
      ${children}
    </div>`;
  }).join("");

  // Drive the pie of common title words for the selected program.
  const sel = apps.find((a) => a.program === state.selectedProgram);
  renderProgramWordsPie(sel ? sel.program : null, sel ? sel.wins : null);
}

// Rudimentary keyword analysis over window titles: tokenize every title, drop
// the app's own name, stopwords, and short/numeric tokens, then rank what's left
// by the active time of the windows it appears in. Surfaces recurring themes
// (YouTube, Reddit, …) that individual window titles bury.
const TITLE_STOPWORDS = new Set([
  "the", "a", "an", "and", "or", "of", "to", "in", "on", "for", "with", "at", "by",
  "from", "new", "tab", "home", "private", "browsing", "mozilla", "google", "www", "com",
]);

function tokenizeTitle(s) {
  return (s || "").split(/[^A-Za-z0-9]+/).filter(Boolean);
}

// Rank the keywords in one program's window titles, weighted by active time.
// Scoping to a single program means a browser surfaces sites (YouTube, Reddit)
// while a native app's fixed-title chrome stays confined to that app.
function programWordFreq(program, wins) {
  const progWords = new Set(tokenizeTitle(program).map((t) => t.toLowerCase()));
  const byWord = new Map(); // lowercased -> { display, ms, count }
  for (const w of wins || []) {
    const seen = new Set(); // each window contributes its time once per distinct word
    for (const raw of tokenizeTitle(w.title)) {
      const lw = raw.toLowerCase();
      if (lw.length < 3 || /^\d+$/.test(lw) || TITLE_STOPWORDS.has(lw) || progWords.has(lw)) continue;
      if (seen.has(lw)) continue;
      seen.add(lw);
      let e = byWord.get(lw);
      if (!e) { e = { display: raw, ms: 0, count: 0 }; byWord.set(lw, e); }
      e.ms += w.ms;
      e.count += 1;
    }
  }
  return [...byWord.values()].sort((a, b) => b.ms - a.ms);
}

// Doughnut of the selected program's common title words, shown beside the tree.
function renderProgramWordsPie(program, wins) {
  const head = document.getElementById("tagc-pie-head");
  const body = document.getElementById("tagc-pie-body");
  if (!body) return;
  destroy("chart-tagc-words");
  if (!program) {
    if (head) head.textContent = "Common words";
    body.innerHTML = `<div class="empty">click an app to see its common title words</div>`;
    return;
  }
  if (head) head.textContent = `Common words · ${program}`;
  const all = programWordFreq(program, wins);
  if (!all.length) {
    body.innerHTML = `<div class="empty">no distinctive words</div>`;
    return;
  }
  // Cut the one-off long-tail (each YouTube title spawns ~10 unique content
  // words) so it doesn't pile into a giant "other" and dilute the real themes.
  // Keep a word if it recurs (>=2 windows) OR is a meaningful share of the
  // program's time; fall back to everything if that leaves too little.
  const total = (wins || []).reduce((s, w) => s + w.ms, 0) || 1;
  const MIN_SHARE = 0.08;
  const kept = all.filter((w) => w.count >= 2 || w.ms >= MIN_SHARE * total);
  const words = kept.length >= 3 ? kept : all;

  // Show only the top words — no "other" slice, which otherwise balloons with the
  // denoised-but-numerous tail and dilutes the real themes.
  const TOP = 10;
  const top = words.slice(0, TOP);
  const labels = top.map((w) => w.display);
  const data = top.map((w) => w.ms);
  const colors = top.map((_, i) => TAG_COLORS[i % TAG_COLORS.length]);

  body.innerHTML = `<canvas id="chart-tagc-words"></canvas>`;
  // Hovering a slice lights up the selected program's window rows whose titles
  // contain that word ("other" and off-slice hovers clear it).
  doughnutChart("chart-tagc-words", labels, data, colors,
    (label) => highlightWordRows(label && label !== "other" ? label : null));
}

// Highlight the expanded, selected program's window rows whose title contains
// `word` (token match, case-insensitive). Pass null to clear.
function highlightWordRows(word) {
  const lw = word ? word.toLowerCase() : null;
  document.querySelectorAll("#usage-tag-detail .tt-group").forEach((g) => {
    const inSelected = g.querySelector(".tt-app")?.dataset.app === state.selectedProgram;
    g.querySelectorAll(".tt-win").forEach((r) => {
      if (!lw || !inSelected) { r.classList.remove("hl"); return; }
      const title = r.querySelector(".tt-wtitle")?.textContent || "";
      r.classList.toggle("hl", tokenizeTitle(title).some((t) => t.toLowerCase() === lw));
    });
  });
}

function doughnutChart(id, labels, msValues, colors, onHoverLabel) {
  destroy(id);
  state.charts[id] = new Chart(document.getElementById(id), {
    type: "doughnut",
    data: { labels, datasets: [{ data: msValues, backgroundColor: colors, borderWidth: 0 }] },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      cutout: "55%",
      onHover: onHoverLabel
        ? (_e, els) => onHoverLabel(els.length ? labels[els[0].index] : null)
        : undefined,
      plugins: {
        legend: { position: "right", labels: { color: TICK, font: { family: "IBM Plex Sans", size: 11 }, boxWidth: 10, padding: 6 } },
        tooltip: { callbacks: { label: (ctx) => ` ${ctx.label}: ${fmtDur(ctx.parsed)}` } },
      },
    },
  });
}

// Hour-of-day (00–23) on X, hours on Y, one stacked segment per tag. Shows when
// during the day each tagged activity happens.
function renderUsageTagHours(tu, available) {
  const wrap = document.getElementById("usage-taghours-wrap");
  const items = orderedTagItems(tu);
  if (!available || !items.length) {
    destroy("chart-usage-taghours");
    wrap.innerHTML = `<div class="empty">${available ? "no active time in window" : "usage tracking is off"}</div>`;
    return;
  }
  if (!document.getElementById("chart-usage-taghours")) {
    wrap.innerHTML = `<canvas id="chart-usage-taghours"></canvas>`;
  }
  const colors = tagColorMap(items);
  const labels = Array.from({ length: 24 }, (_, h) => (h % 2 === 0 ? String(h).padStart(2, "0") : ""));
  const datasets = items.map((it) => ({
    label: it.name,
    data: (tu.hourByTag.get(it.name) || new Array(24).fill(0)).map((ms) => ms / 3600000),
    backgroundColor: colors.get(it.name),
    stack: "tags",
    borderWidth: 0,
    maxBarThickness: 22,
  }));
  stackedBarChart("chart-usage-taghours", labels, datasets);
}

// One bar per day in the window, stacked by tag (what you were doing that day).
// The label above each bar is total active time that day and its share of the
// 24-hour day, e.g. "7h 32m (31%)". Top labels are dropped when there are too
// many days to fit them, but the bars still render.
function renderUsagePerDay(tu, available, b) {
  const wrap = document.getElementById("usage-perday-wrap");
  if (!wrap) return;
  const items = orderedTagItems(tu);
  if (!available || !items.length || !b) {
    destroy("chart-usage-perday");
    wrap.innerHTML = `<div class="empty">${available ? "no active time in window" : "usage tracking is off"}</div>`;
    return;
  }
  if (!document.getElementById("chart-usage-perday")) {
    wrap.innerHTML = `<canvas id="chart-usage-perday"></canvas>`;
  }

  const start = startOfDay(b.start);
  const end = Math.min(startOfDay(b.end), startOfDay(state.data.now));
  const days = [];
  for (let t = start; t <= end; t += DAY) days.push(t);

  const colors = tagColorMap(items);
  const datasets = items.map((it) => {
    const m = tu.dayByTag.get(it.name) || new Map();
    return {
      label: it.name,
      data: days.map((t) => (m.get(dayKey(t)) || 0) / 3600000),
      backgroundColor: colors.get(it.name),
      stack: "day",
      borderWidth: 0,
      maxBarThickness: 40,
    };
  });

  // Total active time each day, with its share of the 24-hour day → the label
  // on top. Always out of 24h (not the elapsed span), so days are comparable.
  const topLabels = days.map((t) => {
    const activeMs = items.reduce((s, it) => s + ((tu.dayByTag.get(it.name) || new Map()).get(dayKey(t)) || 0), 0);
    if (activeMs <= 0) return "";
    return `${fmtDur(activeMs)} (${Math.round((activeMs / DAY) * 100)}%)`;
  });

  // Two-line axis label: weekday on top, date below (month added on the 1st).
  const xLabels = days.map((t) => {
    const d = new Date(t);
    const wd = d.toLocaleDateString(undefined, { weekday: "short" });
    const date = d.getDate() === 1 ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) : String(d.getDate());
    return [wd, date];
  });
  // Full weekday + date for the tooltip, which the abbreviated axis label lacks.
  const titles = days.map((t) => new Date(t).toLocaleDateString(undefined, { weekday: "long", month: "short", day: "numeric" }));

  // Labels overlap once bars get thin; show them only for reasonably short windows.
  stackedBarChart("chart-usage-perday", xLabels, datasets, days.length <= 40 ? topLabels : null, titles);
}

// ── Rule editor ─────────────────────────────────────────
function setupRules() {
  renderRulesEditor();

  const rows = document.getElementById("rules-rows");
  // Live feedback: sync + recategorize on every keystroke, without re-rendering
  // the editor (which would drop focus).
  rows.addEventListener("input", () => {
    syncRulesFromDom();
    markRuleValidity();
    recategorize();
  });
  rows.addEventListener("click", (e) => {
    const del = e.target.closest("[data-del]");
    if (!del) return;
    syncRulesFromDom();
    state.rules.splice(Number(del.dataset.del), 1);
    renderRulesEditor();
    markRuleValidity();
    recategorize();
  });

  document.getElementById("rule-add").addEventListener("click", () => {
    syncRulesFromDom();
    state.rules.push({ program: "", title: "", tag: "" });
    renderRulesEditor();
    markRuleValidity();
  });
  document.getElementById("rule-save").addEventListener("click", saveRulesToServer);

  // "＋ tag" on an untagged row prefills a rule for that specific window.
  document.getElementById("usage-untagged").addEventListener("click", (e) => {
    const btn = e.target.closest("[data-tag-program]");
    if (btn) startRuleForWindow(btn.dataset.tagProgram, btn.dataset.tagTitle || "");
  });

  // Per-tag colour pickers: recolour charts live; the value is saved with rules.
  document.getElementById("tag-colors").addEventListener("input", (e) => {
    const inp = e.target.closest("[data-tagcolor]");
    if (!inp) return;
    state.tagColors[inp.dataset.tagcolor] = inp.value;
    setRulesStatus("unsaved colour — click Save rules", "");
    recategorize({ colorsOnly: true });
  });
}

function setRulesStatus(msg, cls) {
  const s = document.getElementById("rules-status");
  s.className = "rules-status" + (cls ? " " + cls : "");
  s.textContent = msg;
}

function renderRulesEditor() {
  const wrap = document.getElementById("rules-rows");
  if (!state.rules.length) {
    wrap.innerHTML = `<div class="rules-empty">No rules yet — add one to categorize your active time.</div>`;
    return;
  }
  wrap.innerHTML = state.rules
    .map(
      (r, i) => `<div class="rule-row" data-i="${i}">
        <input data-field="program" value="${esc(r.program || "")}" placeholder="^Emacs$" spellcheck="false" autocomplete="off" />
        <input data-field="title" value="${esc(r.title || "")}" placeholder="regex on title" spellcheck="false" autocomplete="off" />
        <input data-field="tag" value="${esc(r.tag || "")}" placeholder="Activity:dev" spellcheck="false" autocomplete="off" />
        <button class="rule-del" data-del="${i}" title="Delete rule" type="button">&times;</button>
      </div>`
    )
    .join("");
}

function syncRulesFromDom() {
  const rows = [...document.querySelectorAll("#rules-rows .rule-row")];
  state.rules = rows.map((row) => ({
    program: row.querySelector('[data-field="program"]').value.trim(),
    title: row.querySelector('[data-field="title"]').value.trim(),
    tag: row.querySelector('[data-field="tag"]').value.trim(),
  }));
}

// Flag rows whose program/title is not a valid regex so the user sees why a
// rule isn't matching.
function markRuleValidity() {
  for (const row of document.querySelectorAll("#rules-rows .rule-row")) {
    let bad = false;
    for (const f of ["program", "title"]) {
      const v = row.querySelector(`[data-field="${f}"]`).value.trim();
      if (v) {
        try { new RegExp(v); } catch { bad = true; }
      }
    }
    row.classList.toggle("invalid", bad);
  }
}

async function saveRulesToServer() {
  syncRulesFromDom();
  const btn = document.getElementById("rule-save");
  btn.disabled = true;
  setRulesStatus("saving…", "");
  try {
    const res = await fetch("api/rules", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ rules: state.rules, colors: state.tagColors }),
    });
    if (!res.ok) throw new Error(`server returned ${res.status}`);
    const cfg = await res.json(); // canonical, server-sanitized
    state.rules = cfg.rules || [];
    state.tagColors = cfg.colors || {};
    renderRulesEditor();
    markRuleValidity();
    recategorize();
    setRulesStatus(`saved ${state.rules.length} rule${state.rules.length === 1 ? "" : "s"} ✓`, "ok");
  } catch (err) {
    setRulesStatus(`save failed: ${err.message}`, "err");
  } finally {
    btn.disabled = false;
  }
}

function renderUsageReadout(u, available) {
  const el = document.getElementById("usage-readout");
  if (!available) {
    el.innerHTML = `<div class="stat" title="No usage log was found on the server.">
      <div class="k">Usage tracking</div>
      <div class="v" style="font-size:20px">off</div>
      <div class="sub">no usage data yet — enable usage_monitor in the glocker config</div>
    </div>`;
    return;
  }
  const activePct = u.trackedMs ? Math.round((u.activeMs / u.trackedMs) * 100) : 0;
  const top = u.apps[0];
  const stats = [
    {
      k: "Active time", v: fmtDur(u.activeMs), sub: `${activePct}% of tracked`,
      help: "Time a window was focused and you were not idle, in the selected window.",
    },
    {
      k: "Idle time", v: fmtDur(u.idleMs), sub: `${100 - activePct}% of tracked`,
      help: `Tracked time with no input for ≥${USAGE_IDLE_THRESHOLD_MS / 1000}s (or no focused window).`,
    },
    {
      k: "Applications", v: u.apps.length, sub: "distinct apps",
      help: "Number of distinct window classes you were actively using.",
    },
    {
      k: "Busiest app", v: top ? top.name : "—", sub: top ? fmtDur(top.ms) : "no data", cls: "peak", big: false,
      help: "The application with the most active time in the window.",
    },
  ];
  el.innerHTML = stats
    .map(
      (s) => `<div class="stat ${s.cls || ""}" title="${esc(s.help)}">
        <div class="k">${s.k}</div>
        <div class="v" ${s.big === false ? 'style="font-size:20px"' : ""}>${esc(String(s.v))}</div>
        <div class="sub">${esc(s.sub)}</div>
      </div>`
    )
    .join("");
}

// Like renderRanklist, but the value is a duration rather than a count.

function renderFooter() {
  const s = state.data.sources;
  const mark = (ok) => (ok ? "✓" : "✗ missing");
  document.getElementById("footer-sources").textContent =
    `reports ${mark(s.reports)}  ·  unblocks ${mark(s.unblocks)}  ·  lifecycle ${mark(s.lifecycle)}  ·  usage ${mark(s.usage)}  ·  read ${new Date(state.data.now).toLocaleString()}`;
}

// ── Chart.js helpers ────────────────────────────────────
function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name.replace("var(", "").replace(")", "")).trim();
}
const GRID = "rgba(255,255,255,0.08)";
const TICK = "#aeb9c9";

function destroy(id) {
  if (state.charts[id]) state.charts[id].destroy();
}

// Horizontal bar: percentage on X, category (tag) on Y, one coloured bar each.
// Pass parallel arrays; ms is used only for the tooltip.
function hbarChart(id, { labels, pct, ms, colors, onSelect }) {
  destroy(id);
  const opts = baseOpts();
  opts.indexAxis = "y";
  if (onSelect) {
    opts.onClick = (_e, els) => { if (els.length) onSelect(els[0].index); };
    opts.onHover = (evt, els) => { evt.native.target.style.cursor = els.length ? "pointer" : "default"; };
  }
  opts.scales = {
    x: {
      beginAtZero: true,
      grid: { color: GRID },
      ticks: { color: TICK, font: { family: "IBM Plex Mono", size: 10 }, callback: (v) => `${v}%` },
    },
    y: {
      grid: { display: false },
      ticks: { color: TICK, font: { family: "IBM Plex Mono", size: 11 }, autoSkip: false },
    },
  };
  opts.plugins.legend = { display: false };
  opts.plugins.tooltip = {
    callbacks: { label: (ctx) => ` ${fmtDur(ms[ctx.dataIndex])} (${pct[ctx.dataIndex].toFixed(1)}%)` },
  };
  state.charts[id] = new Chart(document.getElementById(id), {
    type: "bar",
    data: {
      labels,
      datasets: [{ data: pct, backgroundColor: colors, borderWidth: 0, borderRadius: 3, maxBarThickness: 26 }],
    },
    options: opts,
  });
}

// topLabels (optional): a string per bar index, drawn above each stack (used by
// the per-day chart for the active-% annotation). Pass null/undefined to omit.
// titles (optional): a string per bar index used as the tooltip title (e.g. the
// full weekday + date), since the axis label is abbreviated.
function stackedBarChart(id, labels, datasets, topLabels, titles) {
  destroy(id);
  const opts = baseOpts();
  opts.interaction = { mode: "index", intersect: false };
  opts.scales.x.stacked = true;
  opts.scales.y.stacked = true;
  opts.scales.y.ticks.callback = (v) => `${v}h`;
  opts.plugins.legend = {
    display: true,
    position: "bottom",
    labels: { color: TICK, font: { family: "IBM Plex Sans", size: 11 }, boxWidth: 12, padding: 8 },
  };
  opts.plugins.tooltip = {
    mode: "index",
    intersect: false,
    filter: (item) => item.parsed.y > 0, // hide the many zero-height segments
    callbacks: { label: (ctx) => ` ${ctx.dataset.label}: ${ctx.parsed.y.toFixed(1)}h` },
  };
  if (titles) {
    opts.plugins.tooltip.callbacks.title = (items) => (items.length ? titles[items[0].dataIndex] : "");
  }

  const cfg = { type: "bar", data: { labels, datasets }, options: opts };

  if (topLabels) {
    opts.layout = { padding: { top: 18 } }; // room for the labels above the tallest bar
    cfg.plugins = [{
      id: "topPct",
      afterDatasetsDraw(chart) {
        const { ctx } = chart;
        ctx.save();
        ctx.fillStyle = cssVar("var(--ink-dim)");
        ctx.font = "10px IBM Plex Mono, monospace";
        ctx.textAlign = "center";
        ctx.textBaseline = "bottom";
        for (let i = 0; i < topLabels.length; i++) {
          if (!topLabels[i]) continue;
          let topY = Infinity, x = null;
          for (let di = 0; di < datasets.length; di++) {
            const bar = chart.getDatasetMeta(di).data[i];
            if (!bar || !datasets[di].data[i]) continue;
            if (bar.y < topY) { topY = bar.y; x = bar.x; }
          }
          if (x != null) ctx.fillText(topLabels[i], x, topY - 3);
        }
        ctx.restore();
      },
    }];
  }

  state.charts[id] = new Chart(document.getElementById(id), cfg);
}

function barChart(id, labels, data, colorVar) {
  destroy(id);
  const color = cssVar(colorVar);
  state.charts[id] = new Chart(document.getElementById(id), {
    type: "bar",
    data: { labels, datasets: [{ data, backgroundColor: color, borderRadius: 3, maxBarThickness: 34 }] },
    options: baseOpts(),
  });
}

function lineChart(id, labels, data, { onPoint, gridAt } = {}) {
  destroy(id);
  const ctx = document.getElementById(id).getContext("2d");
  const grad = ctx.createLinearGradient(0, 0, 0, 240);
  grad.addColorStop(0, "rgba(255,77,77,0.35)");
  grad.addColorStop(1, "rgba(255,77,77,0)");
  const opts = baseOpts();
  opts.interaction = { mode: "index", intersect: false };
  opts.plugins.tooltip.enabled = false; // the day inspector replaces the native tooltip
  if (onPoint) {
    // Click (not hover) a point to inspect that day, matching the calendar.
    opts.onClick = (_e, els) => { if (els.length) onPoint(els[0].index); };
  }
  if (gridAt) {
    // Vertical gridlines only at ticks the predicate selects (keeps 1y readable).
    opts.scales.x.grid = {
      display: true,
      drawTicks: false,
      color: (c) => (gridAt(c.index) ? GRID : "transparent"),
    };
  }
  state.charts[id] = new Chart(ctx, {
    type: "line",
    data: {
      labels,
      datasets: [{
        data,
        borderColor: cssVar("var(--danger)"),
        backgroundColor: grad,
        fill: true,
        tension: 0.3,
        pointRadius: 0,
        pointHoverRadius: 4,
        pointHoverBackgroundColor: cssVar("var(--danger)"),
        borderWidth: 1.5,
      }],
    },
    options: opts,
  });
}

function baseOpts() {
  return {
    responsive: true,
    maintainAspectRatio: false,
    plugins: { legend: { display: false }, tooltip: { intersect: false, mode: "index" } },
    scales: {
      x: { grid: { display: false }, ticks: { color: TICK, font: { family: "IBM Plex Mono", size: 10 }, maxRotation: 0, autoSkip: false } },
      y: { beginAtZero: true, grid: { color: GRID }, ticks: { color: TICK, font: { family: "IBM Plex Mono", size: 10 }, precision: 0 } },
    },
  };
}

// ── Misc ────────────────────────────────────────────────
function fmtDur(ms) {
  if (ms <= 0) return "0";
  const mins = Math.round(ms / 60000);
  if (mins < 60) return `${mins}m`;
  const d = Math.floor(mins / 1440), h = Math.floor((mins % 1440) / 60), m = mins % 60;
  if (d > 0) return `${d}d ${h}h`;
  return `${h}h ${m}m`;
}

// ── Admin: accounts panel (only mounted for the configured admin) ───────
async function renderUsers() {
  const listEl = document.getElementById("user-list");
  const countEl = document.getElementById("user-count");
  const errEl = document.getElementById("user-error");
  errEl.hidden = true;
  let data;
  try {
    const res = await fetch("api/admin/users");
    if (!res.ok) throw new Error(res.status);
    data = await res.json();
  } catch (e) {
    errEl.textContent = `Could not load accounts (${e.message})`;
    errEl.hidden = false;
    return;
  }
  const users = data.users || [];
  const totals = data.totals || {};
  const recs = totals.records ?? users.reduce((s, u) => s + (u.records || 0), 0);
  countEl.textContent = `${users.length} account${users.length === 1 ? "" : "s"} · ${recs.toLocaleString()} total records.`;
  listEl.textContent = "";
  for (const u of users) listEl.append(userRow(u));
}

function userRow(u) {
  const row = document.createElement("div");
  row.className = "user-row";

  const main = document.createElement("div");
  main.className = "user-main";
  const email = document.createElement("span");
  email.className = "user-email";
  email.textContent = u.email;
  if (u.isAdmin) email.append(badge("admin", "admin"));
  if (!u.verified) email.append(badge("unverified", "unverified"));
  const sub = document.createElement("span");
  sub.className = "user-sub";
  const limit = u.deviceLimit < 0 ? "∞" : u.deviceLimit;
  const lastSync = u.lastSyncAt ? `last sync ${fmtAgo(u.lastSyncAt)}` : "never synced";
  sub.textContent = `${(u.records || 0).toLocaleString()} records · ${u.devices} of ${limit} devices · ${lastSync} · added ${new Date(u.createdAt).toLocaleDateString()}`;
  main.append(email, sub);

  const actions = document.createElement("div");
  actions.className = "user-actions";

  // Device-limit editor.
  const limInput = document.createElement("input");
  limInput.type = "number";
  limInput.title = "Device limit (−1 = unlimited)";
  limInput.value = String(u.deviceLimit);
  const saveBtn = document.createElement("button");
  saveBtn.className = "btn-ghost";
  saveBtn.type = "button";
  saveBtn.textContent = "Save";
  saveBtn.onclick = async () => {
    saveBtn.disabled = true;
    try {
      await fetch(`api/admin/users?id=${u.id}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ deviceLimit: parseInt(limInput.value, 10) || 0 }),
      });
    } catch { /* re-render reflects state */ }
    renderUsers();
  };
  actions.append(limInput, saveBtn);

  // Delete (never your own admin account).
  if (!u.self) {
    const del = document.createElement("button");
    del.className = "btn-danger";
    del.type = "button";
    del.textContent = "Delete";
    del.onclick = async () => {
      if (!confirm(`Permanently delete ${u.email} and ALL of their data? This cannot be undone.`)) return;
      try { await fetch(`api/admin/users?id=${u.id}`, { method: "DELETE" }); } catch { /* re-render */ }
      renderUsers();
    };
    actions.append(del);
  }

  row.append(main, actions);
  return row;
}

function badge(cls, text) {
  const b = document.createElement("span");
  b.className = "user-badge " + cls;
  b.textContent = text;
  return b;
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

// ── Devices: connect a glocker agent via its own ingest token ───────────
// A token is one device. It's shown exactly once (only its hash is stored), so
// minting reveals the ready-to-paste sync config block. setupDevices wires the
// static controls once; renderDevices refreshes the list each time the view
// opens.
function setupDevices() {
  const nameEl = document.getElementById("device-name");
  const addBtn = document.getElementById("add-device");
  const errEl = document.getElementById("device-error");
  const reveal = document.getElementById("device-reveal");

  addBtn.onclick = async () => {
    errEl.hidden = true;
    addBtn.disabled = true;
    let res;
    try {
      res = await fetch("api/tokens", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: nameEl.value.trim() }),
      });
    } catch (err) {
      errEl.textContent = `Could not reach the server: ${err.message}`;
      errEl.hidden = false;
      addBtn.disabled = false;
      return;
    }
    addBtn.disabled = false;
    if (res.status === 402) { showDeviceUpsell(true); return; } // at plan limit
    if (!res.ok) {
      errEl.textContent = `Could not create the device (${res.status})`;
      errEl.hidden = false;
      return;
    }
    const { token } = await res.json();
    nameEl.value = "";
    document.getElementById("device-config").textContent = deviceConfig(token);
    reveal.hidden = false;
    renderDevices();
  };

  document.getElementById("device-copy").onclick = async () => {
    const btn = document.getElementById("device-copy");
    try {
      await navigator.clipboard.writeText(document.getElementById("device-config").textContent);
      btn.textContent = "Copied ✓";
      setTimeout(() => { btn.textContent = "Copy config"; }, 1500);
    } catch { /* clipboard may be blocked; the text is on-screen to copy manually */ }
  };
  document.getElementById("device-done").onclick = () => {
    reveal.hidden = true;
    document.getElementById("device-config").textContent = "";
  };
}

// deviceConfig builds the sync block the user pastes into the agent's config.
function deviceConfig(token) {
  return [
    "sync:",
    "  enabled: true",
    `  glockpeek_url: "${location.origin}"`,
    `  token: "${token}"`,
    "  interval_seconds: 300",
  ].join("\n");
}

function showDeviceUpsell(show) {
  const el = document.getElementById("device-upsell");
  el.textContent = "You've reached your device limit. More devices are a paid feature — reply to your welcome email to upgrade.";
  el.hidden = !show;
}

async function renderDevices() {
  const listEl = document.getElementById("device-list");
  const usageEl = document.getElementById("device-usage");
  const nameEl = document.getElementById("device-name");
  const addBtn = document.getElementById("add-device");
  let data;
  try {
    const res = await fetch("api/tokens");
    if (!res.ok) throw new Error(res.status);
    data = await res.json();
  } catch {
    usageEl.textContent = "";
    return;
  }

  const limitText = data.limit < 0 ? "∞" : data.limit;
  usageEl.textContent = `${data.used} of ${limitText} device${data.limit === 1 ? "" : "s"} used.`;

  listEl.textContent = "";
  if (!data.tokens.length) {
    const empty = document.createElement("div");
    empty.className = "device-empty";
    empty.textContent = "No devices connected yet.";
    listEl.append(empty);
  }
  for (const t of data.tokens) listEl.append(deviceRow(t));

  const atLimit = !data.canAdd;
  nameEl.disabled = atLimit;
  addBtn.disabled = atLimit;
  showDeviceUpsell(atLimit);
}

function deviceRow(t) {
  const row = document.createElement("div");
  row.className = "device-row";

  const meta = document.createElement("div");
  meta.className = "device-meta";
  const name = document.createElement("span");
  name.className = "device-name";
  name.textContent = t.name || "device";
  const sub = document.createElement("span");
  sub.className = "device-sub";
  const added = new Date(t.createdAt).toLocaleDateString();
  sub.textContent = `added ${added} · ${t.lastUsedAt ? `last synced ${fmtAgo(t.lastUsedAt)}` : "never synced"}`;
  meta.append(name, sub);

  const btn = document.createElement("button");
  btn.className = "btn-danger";
  btn.type = "button";
  btn.textContent = "Revoke";
  btn.onclick = async () => {
    if (!confirm(`Revoke "${t.name || "device"}"? That device will stop syncing.`)) return;
    try { await fetch(`api/tokens?id=${t.id}`, { method: "DELETE" }); } catch { /* re-render reflects state */ }
    renderDevices();
  };

  row.append(meta, btn);
  return row;
}

// fmtAgo renders an epoch-ms instant as a short relative string ("3m ago").
function fmtAgo(ms) {
  const diff = Date.now() - ms;
  if (diff < 5000) return "just now";
  const s = Math.floor(diff / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

// renderSync fills the Data-sync panel from /api/sync: when the daemon last
// pushed, the last batch's makeup, and the stored total.
async function renderSync() {
  const el = document.getElementById("sync-status");
  if (!el) return;
  let s;
  try {
    const res = await fetch("api/sync");
    if (!res.ok) throw new Error(res.status);
    s = await res.json();
  } catch {
    el.innerHTML = `<div class="empty">sync status unavailable</div>`;
    return;
  }
  const totals = s.totals || {};
  const totalRows = Object.values(totals).reduce((a, b) => a + b, 0);
  const last = s.last || {};
  const parts = Object.entries(last).filter(([, n]) => n > 0).map(([k, n]) => `${n} ${k}`);
  const when = s.lastSyncAt ? esc(fmtAgo(s.lastSyncAt)) : "never";
  el.innerHTML = `
    <div class="sync-row"><span class="sync-label">Last sync</span><span class="sync-val${s.lastSyncAt ? "" : " sync-stale"}">${when}</span></div>
    <div class="sync-row"><span class="sync-label">Last batch</span><span class="sync-val">${parts.length ? esc(parts.join(", ")) : "&mdash;"}</span></div>
    <div class="sync-row"><span class="sync-label">Stored total</span><span class="sync-val">${totalRows.toLocaleString()} records</span></div>
  `;
}
