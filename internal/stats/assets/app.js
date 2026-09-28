// glockpeek dashboard frontend. Pulls the full parsed history from /api/data, then
// does all aggregation/filtering in the browser (the data set is tiny). The
// vulnerability heatmap is the centrepiece: it surfaces the hour/weekday slots
// where you actually slip — and hatches the slots where glocker was off, so a
// blank cell is never mistaken for a safe one.

import { openVault, decryptField } from "./e2e-crypto.js";

const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
const DAY = 86400000;
const HOUR = 3600000;

// The window is an arbitrary [winStart, winEnd] range, chosen by brushing the
// all-history timeline strip at the top (no more fixed presets). Null until the
// first data load picks a default (see defaultWindow).
const DEFAULT_WINDOW_DAYS = 30;
const state = { data: null, deviceFilter: null, devices: [], winStart: null, winEnd: null, view: "overview", analyticsTab: "history", charts: {}, rules: [], tags: [], tagById: new Map(), tagByLabel: new Map(), usageNamespace: null, usageWindow: null, selectedTag: null, expandedApps: new Set(), selectedProgram: null, lastTagUsage: null, ignored: [], openDay: null, readOnly: false, revokedIds: new Set() };

const VIEW_TITLES = {
  overview: "Overview",
  analytics: "Analytics",
  insights: "Insights",
  config: "Config",
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

// ── End-to-end decryption ────────────────────────────────────────────────────
// When the account's records are encrypted (me.encrypted), the browser holds the
// master key for the session and decrypts each record's sensitive fields before
// any aggregation runs — the server only ever sees ciphertext.

let masterKey = null; // Uint8Array once unlocked; null otherwise

// Which fields are encrypted per collection, and the record "kind" — must match
// the agent's enc() calls (internal/syncer/syncer.go). The aad each field was
// sealed under is `${ts}|${kind}|${field}`.
// Flat collections: the JSON field name equals the aad label the agent sealed
// under. Usage is handled separately below because /api/data nests it as
// active.{title} while the agent's aad label is "activeTitle".
const ENC_FIELDS = {
  violations: { kind: "violation", fields: ["keyword", "url", "domain"] },
  unblocks: { kind: "unblock", fields: ["domain", "reason"] },
  lifecycle: { kind: "lifecycle", fields: ["note"] },
};

// promptPassphrase shows a minimal overlay asking for the vault passphrase and
// resolves with the entered string. (A styled screen can replace this with the
// planned frontend migration; it's self-contained for now.)
function promptPassphrase(errMsg) {
  return new Promise((resolve) => {
    const ov = document.createElement("div");
    ov.style.cssText =
      "position:fixed;inset:0;background:rgba(7,9,13,.94);display:flex;align-items:center;justify-content:center;z-index:9999;";
    ov.innerHTML = `
      <form style="background:#0d1017;border:1px solid #1b2230;border-radius:12px;padding:24px;width:min(420px,90vw);color:#e7ecf3;font-family:inherit;">
        <h2 style="margin:0 0 8px;font-size:1.1rem;">Unlock your data</h2>
        <p style="color:#8b97a8;font-size:.9rem;margin:0 0 14px;">Enter your passphrase to view your dashboard.</p>
        <input type="password" autocomplete="current-password" placeholder="passphrase" style="width:100%;padding:10px 12px;background:#05070a;border:1px solid #1b2230;border-radius:8px;color:#e7ecf3;font-size:1rem;box-sizing:border-box;" />
        <div class="pp-err" style="color:#ff4d4d;font-size:.85rem;min-height:1.2em;margin-top:8px;"></div>
        <button type="submit" style="margin-top:12px;width:100%;padding:11px;background:#ff4d4d;color:#150404;border:none;border-radius:8px;font-weight:600;cursor:pointer;">Unlock</button>
      </form>`;
    ov.querySelector(".pp-err").textContent = errMsg || "";
    const form = ov.querySelector("form");
    const input = ov.querySelector("input");
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      const v = input.value;
      document.body.removeChild(ov);
      resolve(v);
    });
    document.body.appendChild(ov);
    input.focus();
  });
}

// unlockVault fetches the vault and opens it with the user's passphrase, retrying
// on a wrong passphrase. Sets masterKey. Returns false only if the vault can't be
// fetched.
async function unlockVault() {
  if (masterKey) return true;
  let vault;
  try {
    const r = await fetch("api/vault", { credentials: "same-origin" });
    if (!r.ok) return false;
    vault = await r.json();
  } catch {
    return false;
  }
  let err = "";
  for (;;) {
    const pass = await promptPassphrase(err);
    try {
      masterKey = await openVault(vault, new TextEncoder().encode(pass));
      return true;
    } catch {
      err = "That passphrase didn't work — try again.";
    }
  }
}

// Usage nests its active window, so the JSON path (active.title) and the aad
// label the agent sealed under (activeTitle) differ — the flat collections in
// ENC_FIELDS use one name for both.
const USAGE_ENC_FIELDS = [
  ["title", "activeTitle"],
  ["class", "activeClass"],
  ["instance", "activeInstance"],
];

// collectDecryptJobs walks the fetched data and returns one job per sealed
// field: the object holding it, the key to overwrite, and the aad it was sealed
// under. Collecting first means the total is known before any work starts (so
// the boot dial can show a real fraction) and the opens can be run in parallel
// batches rather than one await at a time.
function collectDecryptJobs(data) {
  const jobs = [];
  for (const [coll, spec] of Object.entries(ENC_FIELDS)) {
    const rows = data[coll];
    if (!Array.isArray(rows)) continue;
    for (const row of rows) {
      for (const f of spec.fields) {
        if (!row[f]) continue; // empty fields were sent unencrypted
        jobs.push({ obj: row, key: f, aad: `${row.ts}|${spec.kind}|${f}` });
      }
    }
  }
  if (Array.isArray(data.usage)) {
    for (const row of data.usage) {
      const a = row.active;
      if (!a) continue;
      for (const [field, label] of USAGE_ENC_FIELDS) {
        if (!a[field]) continue;
        jobs.push({ obj: a, key: field, aad: `${row.ts}|usage|${label}` });
      }
    }
  }
  return jobs;
}

// How many fields to open at once. WebCrypto runs off the main thread, so a
// batch genuinely overlaps instead of just queueing; awaiting between batches
// also yields to the event loop, which is what lets the boot dial paint.
const DECRYPT_BATCH = 256;

// decryptData decrypts every encrypted field of the fetched data in place, so all
// downstream aggregation/categorization sees plaintext (exactly as before E2E).
// A field that fails to decrypt (e.g. data encrypted under a different key) is
// marked with a lock glyph; the count of failures is returned.
//
// onProgress(done, total, final) is called after each batch, if given.
async function decryptData(data, onProgress) {
  if (!masterKey || !data) return 0;
  const td = new TextDecoder();
  const te = new TextEncoder();
  const jobs = collectDecryptJobs(data);
  let failures = 0;

  if (onProgress) onProgress(0, jobs.length, false);
  for (let i = 0; i < jobs.length; i += DECRYPT_BATCH) {
    const batch = jobs.slice(i, i + DECRYPT_BATCH);
    await Promise.all(batch.map(async (job) => {
      try {
        job.obj[job.key] = td.decode(await decryptField(masterKey, job.obj[job.key], te.encode(job.aad)));
      } catch {
        // No silent fallback: a field that won't decrypt shows the lock glyph
        // and counts as a failure, so a still-cleartext (pre-encryption) or
        // wrong-key row is visibly broken rather than passing as real data —
        // that's how you can tell the encryption is actually in effect. Legacy
        // rows read 🔒 until a `glocker -resync` re-encrypts them.
        job.obj[job.key] = "🔒";
        failures++;
      }
    }));
    const done = i + batch.length;
    if (onProgress) onProgress(done, jobs.length, done >= jobs.length);
  }
  return failures;
}

// ── Boot dial ───────────────────────────────────────────
// The first load can't render anything until every sealed field has been opened
// in the browser, which on a long history is tens of thousands of AES-GCM opens.
// These drive the ring on the loading screen (markup in dashboard.html.tmpl,
// styling under "Boot dial" in styles.css) so that wait is legible instead of a
// static "decrypting data…".
const bootUI = { root: null, pct: null, phase: null, count: null, lastPaint: 0 };

function bootEls() {
  if (!bootUI.root) {
    bootUI.root = document.querySelector("#loading .boot");
    bootUI.pct = document.getElementById("boot-pct");
    bootUI.phase = document.getElementById("boot-phase");
    bootUI.count = document.getElementById("boot-count");
  }
  return bootUI.root;
}

// bootPhase names what we are waiting on and returns the dial to its
// indeterminate sweep — the phase has started but nothing has counted the work
// in it yet.
function bootPhase(label) {
  if (!bootEls()) return;
  bootUI.phase.textContent = label;
  bootUI.count.textContent = "";
  bootUI.pct.textContent = "—";
  bootUI.root.classList.add("indeterminate");
  bootUI.root.style.setProperty("--p", "0");
  bootUI.root.setAttribute("aria-valuenow", "0");
  bootUI.lastPaint = 0;
}

// bootProgress paints the fraction. decryptData reports after every batch —
// hundreds of calls on a real history — so painting is throttled to ~15fps;
// numbers changing faster than that are unreadable anyway. final always paints,
// so the dial can never stop short of 100%.
function bootProgress(done, total, final) {
  if (!bootEls() || total <= 0) return;
  const now = performance.now();
  if (!final && now - bootUI.lastPaint < 66) return;
  bootUI.lastPaint = now;

  const frac = Math.min(1, done / total);
  bootUI.root.classList.remove("indeterminate");
  bootUI.root.style.setProperty("--p", frac.toFixed(4));
  bootUI.pct.textContent = Math.floor(frac * 100);
  bootUI.count.textContent = `${done.toLocaleString()} / ${total.toLocaleString()} fields`;
  bootUI.root.setAttribute("aria-valuenow", String(Math.round(frac * 100)));
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
  state.me = me; // stash identity (billing flag, subStatus, deviceLimit) for the UI
  renderAccessBanner();
  // Hosted is end-to-end-encrypted only: an account with no key vault must set a
  // passphrase before it can reach the dashboard. Both the fresh-load and the
  // post-login paths funnel through here → the SSR encryption-setup route.
  if (me && me.auth && !me.e2e) {
    location.href = "/settings/encryption";
    return;
  }
  // If the account's data is encrypted, unlock the vault (prompt for passphrase)
  // so records can be decrypted below. Done before any data is rendered.
  if (me && me.encrypted) {
    bootPhase("unlocking vault");
    await unlockVault();
  }
  bootPhase("fetching records");
  try {
    const res = await fetch("api/data");
    if (res.status === 401) { showLogin(); return; }
    if (!res.ok) throw new Error(`server returned ${res.status}`);
    state.data = await res.json();
    // Decrypt in the browser before any aggregation sees the records. This is
    // the long pole on a big history, so it's the phase that gets a real count.
    bootPhase("decrypting");
    const failed = await decryptData(state.data, bootProgress);
    if (failed > 0) {
      console.warn(`glockpeek: ${failed} field(s) could not be decrypted — the installed agent key may not match this vault.`);
    }
  } catch (err) {
    showError(`Could not load logs: ${err.message}`);
    return;
  }
  bootPhase("applying rules");
  // Tag registry (shared Glocker:* system tags + this account's own), then the
  // rules that attach them. Both non-fatal if missing. Matching stays client-side.
  try {
    const tres = await fetch("api/tags");
    if (tres.ok) setTags((await tres.json()).tags || []);
  } catch { /* keep empty registry */ }
  try {
    const rres = await fetch("api/rules");
    if (rres.ok) state.rules = (await rres.json()).rules || [];
  } catch { /* keep empty rules */ }

  // False-positive ignore list (non-fatal if missing).
  try {
    const gres = await fetch("api/ignored");
    if (gres.ok) state.ignored = (await gres.json()).ignored || [];
  } catch { /* keep empty ignore list */ }

  // Device list for the analytics per-device filter (non-fatal). One token = one
  // device; the dropdown appears only when there's more than one.
  try {
    const dres = await fetch("api/tokens");
    if (dres.ok) state.devices = (await dres.json()).tokens || [];
  } catch { /* no device filter */ }
  populateDeviceFilter();

  // Content-side post-processing the server no longer does — enrich + hide ignored
  // violations and fill any missing domain, all client-side so the endpoint stays
  // content-blind (docs/e2e-design.md). Shared with reloadData.
  applyOverlay();

  document.getElementById("loading").hidden = true;
  document.getElementById("dash").hidden = false;

  // Data-sync panel (last daemon push). Refresh periodically so "N ago" stays
  // current without a reload.
  renderSync();
  setInterval(renderSync, 30000);

  // In-app refresh: a manual button plus a countdown-driven auto-refresh with
  // backoff. reloadData() re-fetches and re-decrypts in place, so unlike Ctrl-R it
  // keeps the session unlocked. The interval starts at 2m and doubles after each
  // auto-refresh (an idle dashboard refreshes ever less often); a manual refresh
  // resets it to 2m. Skips while the tab is hidden or the user is typing.
  const BASE_REFRESH_MS = 2 * 60 * 1000;
  const MAX_REFRESH_MS = 32 * 60 * 1000;
  let refreshEveryMs = BASE_REFRESH_MS;
  let nextRefreshAt = Date.now() + refreshEveryMs;
  const refreshBtn = document.getElementById("refresh-btn");

  const cdText = (ms) => {
    const s = Math.max(0, Math.round(ms / 1000));
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
  };
  // The button is a conic-progress ring (fraction of time left until the next
  // auto-refresh) around a ↻ glyph, plus the countdown. Built once; paintRefresh
  // only updates the ring fraction (--rf) and the countdown text.
  if (refreshBtn) {
    refreshBtn.innerHTML = `<span class="rf-ring" aria-hidden="true">&#8635;</span><span class="rf-text">Refresh <span class="rf-cd"></span></span>`;
  }
  const paintRefresh = () => {
    if (!refreshBtn || refreshBtn.disabled) return;
    const remaining = Math.max(0, nextRefreshAt - Date.now());
    refreshBtn.style.setProperty("--rf", (refreshEveryMs > 0 ? remaining / refreshEveryMs : 0).toFixed(3));
    const cd = refreshBtn.querySelector(".rf-cd");
    if (cd) cd.textContent = cdText(remaining);
  };
  const runRefresh = async () => {
    if (refreshBtn) { refreshBtn.disabled = true; refreshBtn.classList.add("refreshing"); }
    try {
      await reloadData();
      renderSync();
      // Config comes from its own endpoint, so reloadData() doesn't touch it.
      // Only re-fetch when the tab is actually showing: elsewhere the view
      // switch already re-renders it.
      if (state.view === "config") await renderConfig();
    } finally { if (refreshBtn) { refreshBtn.disabled = false; refreshBtn.classList.remove("refreshing"); } }
  };

  if (refreshBtn) {
    refreshBtn.onclick = async () => {
      await runRefresh();
      refreshEveryMs = BASE_REFRESH_MS; // manual refresh restarts the cadence at 2m
      nextRefreshAt = Date.now() + refreshEveryMs;
      paintRefresh();
    };
  }

  paintRefresh();
  setInterval(async () => {
    if (Date.now() < nextRefreshAt) { paintRefresh(); return; }
    const ae = document.activeElement;
    if (document.hidden || (ae && /^(INPUT|TEXTAREA|SELECT)$/.test(ae.tagName))) {
      nextRefreshAt = Date.now() + 15000; // defer while hidden/typing; don't back off on a skip
      paintRefresh();
      return;
    }
    await runRefresh();
    refreshEveryMs = Math.min(refreshEveryMs * 2, MAX_REFRESH_MS); // back off
    nextRefreshAt = Date.now() + refreshEveryMs;
    paintRefresh();
  }, 1000);

  // Sign-out + device management: only meaningful when auth is on (single-user
  // mode has no login and ingest is tokenless).
  if (me && me.auth) {
    // Top-right account dropdown (email + Sign out).
    const accountMenu = document.getElementById("account-menu");
    const accountBtn = document.getElementById("account-btn");
    const accountDrop = document.getElementById("account-drop");
    const label = document.getElementById("account-label");
    if (label && me.email) label.textContent = me.email;
    accountMenu.hidden = false;
    accountBtn.onclick = (e) => {
      e.stopPropagation();
      const open = accountDrop.hidden;
      accountDrop.hidden = !open;
      accountBtn.setAttribute("aria-expanded", String(open));
    };
    document.addEventListener("click", () => {
      accountDrop.hidden = true;
      accountBtn.setAttribute("aria-expanded", "false");
    });
    document.getElementById("logout-btn").onclick = async () => {
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

  // Left-nav view switching → clean URL paths via pushState. popstate (back/
  // forward) and the initial path are what apply the view, so deep links and
  // browser history all work.
  document.getElementById("nav").addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-view]");
    if (btn) routeTo(btn.dataset.view);
  });
  window.addEventListener("popstate", () => setView(viewFromPath()));

  // Analytics sub-tabs (History / Usage / Tags): each is its own clean path
  // (/analytics/<tab>) so the selection lives in the address bar and a refresh
  // restores it.
  document.getElementById("analytics-subtabs").addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-subtab]");
    if (!btn) return;
    state.analyticsTab = btn.dataset.subtab;
    const path = "/analytics/" + btn.dataset.subtab;
    if (location.pathname !== path) history.pushState({}, "", path);
    setSubtab(btn.dataset.subtab);
  });

  // Mark a violation as a false positive from the day-detail hit list.
  document.getElementById("day-detail").addEventListener("click", (e) => {
    const btn = e.target.closest(".hit-x");
    if (!btn) return;
    ignoreViolation({
      recordId: btn.dataset.rid, ts: Number(btn.dataset.ts), keyword: btn.dataset.kw,
      url: btn.dataset.url, domain: btn.dataset.dom,
    });
  });

  // Restore a previously-ignored violation.
  document.getElementById("ignored-list").addEventListener("click", (e) => {
    const btn = e.target.closest(".ign-restore");
    if (!btn) return;
    restoreViolation({ recordId: btn.dataset.rid });
  });

  // A chart sized while its collapsible panel was closed renders at 0×0; resize
  // the charts once the panel is opened so they fill their revealed container.
  document.querySelectorAll("details.collapse").forEach((d) => {
    d.addEventListener("toggle", () => {
      if (!d.open) return;
      requestAnimationFrame(() => {
        for (const c of Object.values(state.charts)) { try { c.resize(); } catch { /* mid-rebuild */ } }
      });
    });
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

  // The Config tab is available in both local and hosted mode (a device's own
  // config in local mode, every device's in hosted). Shown always; it carries an
  // empty state until an agent has synced its config.
  document.getElementById("nav-config").hidden = false;
  document.getElementById("nav-insights").hidden = false;

  restoreWindow(); // pick the window (saved cookie or default) now data is loaded
  setupRules();
  renderFooter();
  render();
  loadUsage();             // usage is lazy-loaded + windowed (see loadUsage); fills in behind the first paint
  migrateLegacyHash();     // turn an old #usage-style bookmark into the clean URL
  setView(viewFromPath()); // honour a deep-linked path like /analytics/usage on load
}

// The view named by the URL path, or "overview" if absent/unknown. Analytics
// sub-tabs live at /analytics/<history|usage|tags>, so a deep link or refresh
// restores the sub-view.
function viewFromPath() {
  const seg = location.pathname.split("/").filter(Boolean);
  const v = seg[0] || "overview";
  if (v === "analytics") {
    const sub = seg[1];
    if (sub === "history" || sub === "usage" || sub === "tags") state.analyticsTab = sub;
    return "analytics";
  }
  return VIEW_TITLES[v] ? v : "overview";
}

// The canonical clean-URL path for a view (Analytics carries its sub-tab).
function pathFor(view) {
  if (view === "analytics") return "/analytics/" + (state.analyticsTab || "history");
  return view === "overview" ? "/" : "/" + view;
}

// Navigate to a view by pushing its clean URL, then applying it (pushState does
// not fire popstate, so apply directly). Re-clicking the current view is a no-op
// push that still re-applies.
function routeTo(view) {
  const path = pathFor(view);
  if (location.pathname !== path) history.pushState({}, "", path);
  setView(view);
}

// One-time migration for old #hash deep links (#usage, #config, …): rewrite them
// to the equivalent clean URL so existing bookmarks keep working.
function migrateLegacyHash() {
  if (location.pathname !== "/" || !location.hash) return;
  const h = decodeURIComponent(location.hash.replace(/^#\/?/, ""));
  const sub = { history: "history", usage: "usage", tags: "tags" }[h];
  const path = sub ? "/analytics/" + sub : (VIEW_TITLES[h] ? (h === "overview" ? "/" : "/" + h) : null);
  if (path) history.replaceState({}, "", path);
}

function setView(view) {
  state.view = view;
  document.querySelectorAll(".view").forEach((v) => v.classList.toggle("active", v.dataset.view === view));
  document.querySelectorAll("#nav button").forEach((b) => b.classList.toggle("active", b.dataset.view === view));
  document.getElementById("view-title").textContent = VIEW_TITLES[view] || view;
  // Range readouts (Analytics + Insights) are populated/shown by updateRangeReadout.
  if (view === "analytics") setSubtab(state.analyticsTab || "history");
  if (view === "config") renderConfig();
  if (view === "insights") renderInsights();
  if (view === "devices") renderDevices();
  if (view === "users") renderUsers();
  // Charts built while their view was display:none have zero size; fix on reveal.
  requestAnimationFrame(() => {
    for (const c of Object.values(state.charts)) {
      try { c.resize(); } catch { /* chart may be mid-rebuild */ }
    }
  });
}

// setSubtab switches the Analytics History/Usage/Tags sub-view. History and
// Usage share the strip + window above; Tags is a registry editor that doesn't
// use the window, so the strip is hidden there.
function setSubtab(tab) {
  state.analyticsTab = tab;
  document.querySelectorAll("#analytics-subtabs button").forEach((b) => b.classList.toggle("active", b.dataset.subtab === tab));
  document.querySelectorAll(".subview").forEach((v) => v.classList.toggle("active", v.dataset.subtab === tab));
  const strip = document.querySelector("#view-analytics .strip-panel") || document.querySelector('.view[data-view="analytics"] .strip-panel');
  if (strip) strip.hidden = tab === "tags";
  if (tab === "tags") {
    renderTags();
    // Tags has no range strip and is short, so a preserved scroll position would
    // clamp and show the registry from the middle (heading cut off). Show it from
    // the top instead. History/Usage keep their scroll (they share the strip).
    window.scrollTo({ top: 0 });
  }
  // Charts sized while their sub-view was hidden need a resize once revealed.
  requestAnimationFrame(() => {
    for (const c of Object.values(state.charts)) { try { c.resize(); } catch { /* mid-rebuild */ } }
  });
}

function showError(msg) {
  document.getElementById("loading").hidden = true;
  const el = document.getElementById("error");
  el.hidden = false;
  el.innerHTML = `<div>⚠ ${msg}</div><div style="color:var(--ink-faint)">Is glockpeek running on a host with the glocker logs?</div>`;
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

// ── Window: an arbitrary [winStart, winEnd] range brushed on the strip ──────
// allBounds is the full data extent (the strip's domain); windowBounds is the
// current selection every detail view renders.
function allBounds() {
  return { start: startOfDay(earliestTs()), end: state.data.now };
}

// defaultWindow selects the most recent DEFAULT_WINDOW_DAYS, clamped to the data
// we have — used when there's no usable saved window.
function defaultWindow() {
  const all = allBounds();
  state.winEnd = all.end;
  state.winStart = Math.max(all.start, startOfDay(all.end) - (DEFAULT_WINDOW_DAYS - 1) * DAY);
}

// setWindow commits a new selection: clamp into the data extent, enforce a
// one-day minimum, persist, then re-render the detail views + floating readout.
function setWindow(start, end) {
  const all = allBounds();
  let s = Math.max(all.start, Math.min(start, end));
  let e = Math.min(all.end, Math.max(start, end));
  if (e - s < DAY) e = Math.min(all.end, s + DAY);
  state.winStart = s;
  state.winEnd = e;
  saveWindow();
  render();
  loadUsage(); // window changed → fetch that window's usage (all usage views filter by it)
}

// ── Persisted window (cookie): remember the selection across reloads. ────────
function saveWindow() {
  if (state.winStart == null || state.winEnd == null) return;
  setCookie(WINDOW_COOKIE, `${state.winStart}|${state.winEnd}`, 365);
}

// restoreWindow loads a saved [start,end] clamped to the current data extent,
// falling back to defaultWindow if absent/unusable. Needs state.data.
function restoreWindow() {
  const all = allBounds();
  const [a, b] = (getCookie(WINDOW_COOKIE) || "").split("|").map(Number);
  if (Number.isFinite(a) && Number.isFinite(b) && b > a) {
    state.winStart = Math.max(all.start, a);
    state.winEnd = Math.min(all.end, b);
    if (state.winEnd - state.winStart >= DAY) return;
  }
  defaultWindow();
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
  return { start: state.winStart, end: state.winEnd };
}

// updateRangeReadout writes the selected range into every Range-panel readout
// (Analytics + Insights share the same strip component, each with its own readout).
function updateRangeReadout() {
  if (state.winStart == null) return;
  const fmt = (ts) => new Date(ts).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
  const days = Math.round((state.winEnd - state.winStart) / DAY);
  const html = `<span class="rr-from">${fmt(state.winStart)}</span><span class="rr-arrow">→</span><span class="rr-to">${fmt(state.winEnd)}</span><span class="rr-days">${days}d</span>`;
  document.querySelectorAll(".range-readout").forEach((el) => { el.innerHTML = html; el.hidden = false; });
}

const within = (ts, b) => ts >= b.start && ts <= b.end;

// Device filter: null = all devices; otherwise a deviceId. Only the analytics
// sheets honor it — Overview (score/streak) stays account-wide.
const DEVICE_COOKIE = "glockpeek_device";
// Combined (deviceFilter null) rolls up every ACTIVE device — revoked devices'
// history is excluded so revoking one cleans it out of Combined + analytics (the
// row survives only for the Devices management screen). A specific filter matches
// that device exactly.
const inDevice = (row) => state.deviceFilter == null ? !state.revokedIds.has(row.deviceId) : row.deviceId === state.deviceFilter;

// Exposure spans (unmanaged + downtime) arrive per device. scopeSpans returns the
// current view: filtered to the selected device, or — for Combined — the UNION of
// all devices' spans (ANY: the account is exposed if any device was off).
function mergeSpans(spans) {
  const s = (spans || []).map((p) => ({ ...p })).sort((a, b) => a.start - b.start);
  const out = [];
  for (const p of s) {
    const last = out[out.length - 1];
    if (last && p.start <= last.end) { // overlapping / touching → union
      last.end = Math.max(last.end, p.end);
      last.open = last.open || p.open;
    } else {
      out.push({ ...p });
    }
  }
  return out;
}
function scopeSpans(spans) {
  if (state.deviceFilter != null) return (spans || []).filter((p) => p.deviceId === state.deviceFilter);
  // Combined = union of ACTIVE devices' spans only (revoked excluded).
  return mergeSpans((spans || []).filter((p) => !state.revokedIds.has(p.deviceId)));
}
// Re-derive the device-scoped exposure spans from the raw per-device ones, so every
// consumer of state.data.unmanaged/downtime sees the right scope. Called on load
// (applyOverlay) and whenever the device switcher changes.
function scopeExposure() {
  if (!state.data) return;
  state.data.unmanaged = scopeSpans(state.rawUnmanaged);
  state.data.downtime = scopeSpans(state.rawDowntime);
}

// populateDeviceFilter fills the sidebar device switcher (global scope) from the
// account's devices (API tokens): "Combined" + each device, with a machine count.
// One device shows as a static name label; none hides the block. Combined
// (deviceFilter null) is the account-wide roll-up; picking a device scopes the
// Analytics + Insights sheets to it.
function populateDeviceFilter() {
  const box = document.getElementById("sidebar-devices");
  const el = document.getElementById("device-filter");
  if (!box || !el) return;
  const all = state.devices || [];
  // Revoked devices are excluded from analytics entirely — kept only for the
  // Devices management screen. Track their ids so Combined/scoping drop their rows.
  state.revokedIds = new Set(all.filter((d) => d.revokedAt != null).map((d) => d.id));
  const devs = all.filter((d) => d.revokedAt == null);
  if (devs.length === 0) { box.hidden = true; state.deviceFilter = null; return; }
  box.hidden = false;

  const saved = getCookie(DEVICE_COOKIE);
  state.deviceFilter = saved ? Number(saved) : null;
  if (state.deviceFilter != null && !devs.some((d) => d.id === state.deviceFilter)) state.deviceFilter = null;

  const label = (d) => d.name || ("device " + d.id);
  if (devs.length === 1) {
    el.innerHTML = `<option>${escHtml(label(devs[0]))}</option>`;
    el.disabled = true;
    el.classList.add("device-filter--label");
    state.deviceFilter = null; // one device: Combined == that device
    return;
  }
  el.disabled = false;
  el.classList.remove("device-filter--label");
  el.innerHTML = [`<option value="">Combined</option>`]
    .concat(devs.map((d) => `<option value="${d.id}">${escHtml(label(d))}</option>`))
    .join("");
  el.value = state.deviceFilter == null ? "" : String(state.deviceFilter);
  el.onchange = () => {
    state.deviceFilter = el.value === "" ? null : Number(el.value);
    setCookie(DEVICE_COOKIE, el.value, 365);
    scopeExposure(); // re-derive unmanaged/downtime for the new scope
    render();
    if (state.view === "insights") renderInsights();
    renderTimeline();
  };
}

// ── Master render ───────────────────────────────────────
function render() {
  const b = windowBounds();
  updateRangeReadout();
  const violations = state.data.violations.filter((v) => within(v.ts, b) && inDevice(v));
  const unblocks = state.data.unblocks.filter((u) => within(u.ts, b) && inDevice(u));
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
  // selected window — it doesn't change as you flip the range picker. It IS scoped
  // by the device switcher: violations filter by device, and unmanaged/downtime
  // are already device-scoped (Combined = union) via scopeExposure.
  const allViolations = state.data.violations.filter(inDevice);
  renderScore(allViolations, state.data.unmanaged, { start: startOfDay(earliestTs()), end: state.data.now });
  renderStreak(allViolations); // standing, all-history — not windowed
  renderReadout(violations, unblocks, unmanaged, heat, b);
  renderHeatmap(heat);
  renderUnblocksByHour(unblocks);
  renderCalendar(violations, b);
  renderTimeline();
  resetDayDetail();
  renderExposure(unmanaged, downtime, b);
  renderUnblockList(unblocks);
  renderUsage(b);
  renderIgnored();
  // Insights read the same window; re-run them when it moves (brush release).
  if (state.view === "insights") renderInsights();
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
    ? computeUsage((state.data.usage || []).filter((e) => within(e.ts, b) && inDevice(e))).trackedMs
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

// renderTimeline draws the ALL-HISTORY strip at the top: two shared-Y sparklines
// (violations + unblocks per day), the unmanaged stretches as a pink shaded
// background, and the current window as a brush selection (the rest dimmed).
// Dragging the selection — create / move / resize its edges — sets the window.
function renderTimeline(canvasId = "chart-timeline") {
  if (!document.getElementById(canvasId)) return;
  const all = allBounds();
  const start = all.start, end = all.end, now = state.data.now;
  const days = Math.max(1, Math.round((end - start) / DAY) + 1);
  const vday = new Array(days).fill(0), uday = new Array(days).fill(0);
  const idx = (ts) => Math.min(days - 1, Math.max(0, Math.floor((ts - start) / DAY)));
  for (const v of state.data.violations) if (v.ts >= start && inDevice(v)) vday[idx(v.ts)]++;
  for (const u of state.data.unblocks) if (u.ts >= start && inDevice(u)) uday[idx(u.ts)]++;

  const labels = [];
  for (let i = 0; i < days; i++) {
    const d = new Date(start + i * DAY);
    labels.push(d.getDate() === 1 || i === 0 ? d.toLocaleDateString(undefined, { month: "short", day: days > 120 ? undefined : "numeric" }) : "");
  }

  // Continuous time↔pixel across the plot; with offset:false, category point i
  // sits at i/(days-1), which equals (ts-start)/(end-start), so the brush aligns
  // with the sparkline.
  const spanMs = Math.max(1, end - start);
  const xAt = (a, ts) => a.left + Math.max(0, Math.min(1, (ts - start) / spanMs)) * a.width;

  // Behind the lines: unmanaged stretches, in the calendar-hatch pink.
  const underlay = (c) => {
    const { ctx, chartArea: a } = c;
    ctx.save();
    ctx.fillStyle = "rgba(192,70,143,0.20)";
    for (const p of state.data.unmanaged) {
      const x1 = xAt(a, Math.max(p.start, start));
      const x2 = xAt(a, Math.min(p.open ? now : p.end, end));
      if (x2 > x1) ctx.fillRect(x1, a.top, x2 - x1, a.height);
    }
    ctx.restore();
  };

  // On top: dim everything outside the selected window and mark its edges — the
  // classic brush look. Reads state.winStart/winEnd live so a drag redraw (or a
  // calendar click) updates it without a full re-render.
  const overlay = (c) => {
    const { ctx, chartArea: a } = c;
    if (state.winStart == null) return;
    const x1 = xAt(a, state.winStart), x2 = xAt(a, state.winEnd);
    ctx.save();
    ctx.fillStyle = "rgba(5,7,11,0.60)";
    ctx.fillRect(a.left, a.top, x1 - a.left, a.height);
    ctx.fillRect(x2, a.top, a.right - x2, a.height);
    ctx.strokeStyle = "#e8eefc";
    ctx.lineWidth = 2;
    for (const x of [x1, x2]) {
      ctx.beginPath(); ctx.moveTo(x, a.top); ctx.lineTo(x, a.bottom); ctx.stroke();
      ctx.fillStyle = "#e8eefc";
      ctx.fillRect(x - 1.5, a.top + a.height / 2 - 8, 3, 16); // grab knob
    }
    ctx.restore();
  };

  stripChart(canvasId, labels, [
    { label: "violations", data: vday, color: cssVar("var(--danger)"), fill: true },
    { label: "unblocks", data: uday, color: cssVar("var(--safe)"), fill: false },
  ], { underlay, overlay });

  wireBrush(document.getElementById(canvasId));
  updateRangeReadout(); // keep this strip's from→to readout in sync
}

// stripChart: a compact multi-line sparkline (shared Y, integer axis) with the
// brush under/overlay plugins. Distinct from lineChart (single series + points).
function stripChart(id, labels, series, { underlay, overlay } = {}) {
  destroy(id);
  const ctx = document.getElementById(id).getContext("2d");
  const opts = baseOpts();
  opts.plugins.tooltip.enabled = false;
  opts.scales.x.grid = { display: false };
  opts.scales.x.ticks.maxTicksLimit = 8;
  opts.scales.y.ticks.maxTicksLimit = 3;
  const plugins = [{
    id: id + "-brush",
    beforeDatasetsDraw: (c) => underlay && underlay(c),
    afterDatasetsDraw: (c) => overlay && overlay(c),
  }];
  state.charts[id] = new Chart(ctx, {
    type: "line",
    data: {
      labels,
      datasets: series.map((s) => ({
        label: s.label, data: s.data,
        borderColor: s.color,
        backgroundColor: s.fill ? s.color + "33" : "transparent",
        fill: s.fill, tension: 0.3, pointRadius: 0, borderWidth: 1.5,
      })),
    },
    options: opts,
    plugins,
  });
}

// wireBrush turns the strip canvas into a range selector: drag empty space to
// create a window, drag its body to pan, drag an edge to resize. It live-updates
// the selection + floating readout during the drag (cheap chart.draw()), then
// commits via setWindow (full re-render) on release. Attached once per canvas;
// reads the chart live so it survives the chart being rebuilt each render.
function wireBrush(canvas) {
  if (!canvas || canvas.dataset.brushWired) return;
  canvas.dataset.brushWired = "1";
  const HANDLE = 8;
  let drag = null;

  const chart = () => state.charts[canvas.id]; // this canvas's own strip chart
  const area = () => { const c = chart(); return c && c.chartArea; };
  const relX = (e) => e.clientX - canvas.getBoundingClientRect().left;
  const pxToTs = (a, px) => {
    const { start, end } = allBounds();
    return start + Math.max(0, Math.min(1, (px - a.left) / a.width)) * (end - start);
  };
  const tsToPx = (a, ts) => {
    const { start, end } = allBounds();
    return a.left + Math.max(0, Math.min(1, (ts - start) / Math.max(1, end - start))) * a.width;
  };
  const zone = (a, px) => {
    const xL = tsToPx(a, state.winStart), xR = tsToPx(a, state.winEnd);
    if (Math.abs(px - xL) <= HANDLE) return "resizeL";
    if (Math.abs(px - xR) <= HANDLE) return "resizeR";
    return px > xL && px < xR ? "move" : "create";
  };

  canvas.addEventListener("pointermove", (e) => {
    const a = area(); if (!a) return;
    const px = relX(e);
    if (!drag) {
      if (state.winStart == null || px < a.left || px > a.right) { canvas.style.cursor = "default"; return; }
      const z = zone(a, px);
      canvas.style.cursor = z === "move" ? "grab" : z === "create" ? "crosshair" : "ew-resize";
      return;
    }
    const ts = pxToTs(a, px);
    if (drag.mode === "resizeL") state.winStart = Math.min(ts, state.winEnd - DAY);
    else if (drag.mode === "resizeR") state.winEnd = Math.max(ts, state.winStart + DAY);
    else if (drag.mode === "move") {
      const w = drag.w, { start, end } = allBounds();
      state.winStart = Math.max(start, Math.min(ts - drag.off, end - w));
      state.winEnd = state.winStart + w;
    } else {
      state.winStart = Math.min(drag.anchor, ts);
      state.winEnd = Math.max(drag.anchor, ts);
    }
    updateRangeReadout();
    chart().draw();
  });

  canvas.addEventListener("pointerdown", (e) => {
    const a = area(); if (!a) return;
    const px = relX(e);
    if (px < a.left || px > a.right) return;
    const z = state.winStart == null ? "create" : zone(a, px);
    drag = { mode: z };
    if (z === "create") { drag.anchor = pxToTs(a, px); state.winStart = state.winEnd = drag.anchor; }
    else if (z === "move") { drag.w = state.winEnd - state.winStart; drag.off = pxToTs(a, px) - state.winStart; }
    canvas.setPointerCapture(e.pointerId);
    canvas.style.cursor = z === "move" ? "grabbing" : "ew-resize";
    e.preventDefault();
  });

  const finish = () => { if (drag) { drag = null; setWindow(state.winStart, state.winEnd); } };
  canvas.addEventListener("pointerup", finish);
  canvas.addEventListener("pointercancel", finish);
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
    // The background reflects the status of the MANAGED portion of the day:
    // green (clean) when there were no violations, red-by-count when there were.
    // The unmanaged stipple is layered on top (height = unmanaged fraction), so a
    // day that was clean during the hours glocker watched still reads green with
    // hatching over it — and a fully-unmanaged day is simply covered by 100%
    // stipple, so no false green shows through.
    const clean = inWindow && count === 0;

    const cls = [
      "cal-day",
      inWindow ? "" : "out",
      count > 0 && inWindow ? "has" : "",
      clean ? "clean" : "",
      t === today ? "today" : "",
      state.openDay != null && dayKey(t) === dayKey(state.openDay) ? "sel" : "",
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
const ignoreKeyOf = (e) => e.recordId || ""; // opaque, content-blind identity

// applyOverlay runs client-side after every data load so the server stays
// content-blind: it enriches the ignore list (which stores only recordIds) with
// the decrypted keyword/url/domain for display, then hides ignored violations and
// fills any missing domain from the URL. Shared by the initial load and reloads.
function applyOverlay() {
  if (!state.data || !Array.isArray(state.data.violations)) return;
  const byRid = new Map(state.data.violations.map((v) => [v.recordId, v]));
  for (const e of state.ignored) {
    const v = byRid.get(e.recordId);
    if (v) { e.keyword = v.keyword; e.url = v.url; e.domain = v.domain || ""; }
  }
  const ign = new Set(state.ignored.map(ignoreKeyOf));
  const hostFromUrl = (u) => { try { return new URL(u).hostname; } catch { return ""; } };
  state.data.violations = state.data.violations
    .filter((v) => !ign.has(ignoreKeyOf(v)))
    .map((v) => (v.domain ? v : { ...v, domain: hostFromUrl(v.url) }));

  // Stash the raw per-device exposure spans, then derive the current scope.
  state.rawUnmanaged = state.data.unmanaged || [];
  state.rawDowntime = state.data.downtime || [];
  scopeExposure();
}

async function putIgnored() {
  try {
    const res = await fetch("api/ignored", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      // Send only the opaque recordId (+ ts for display) — never keyword/url.
      body: JSON.stringify({ ignored: state.ignored.map((e) => ({ recordId: e.recordId, ts: e.ts })) }),
    });
    if (res.ok) state.ignored = (await res.json()).ignored || [];
  } catch { /* keep local list; a reload will reconcile */ }
}

// Refetch the data (now filtered) and re-render, re-opening the same day. Also
// reconciles the ignore list with the server so an in-app refresh can't drift from
// server state. (Rules are NOT re-fetched here — that would clobber an in-progress
// rule edit; they reconcile on a full load.)
async function reloadData() {
  try {
    try {
      const gres = await fetch("api/ignored");
      if (gres.ok) state.ignored = (await gres.json()).ignored || [];
    } catch { /* keep current ignore list */ }
    const res = await fetch("api/data");
    if (!res.ok) return;
    const savedUsage = state.data.usage || []; // /api/data no longer carries usage
    state.data = await res.json();
    await decryptData(state.data); // decrypt the small stuff (usage is empty here → no jobs)
    state.data.usage = savedUsage; // keep the already-decrypted usage we lazy-loaded
    applyOverlay();                // hide ignored + domain fallback, like initial load
    render();
    if (state.openDay != null) showDay(state.openDay);
    loadUsage();                   // refresh the window's usage too
  } catch { /* keep current view */ }
}

// loadUsage fetches the CURRENT window's usage samples, decrypts them, and
// re-renders. Usage is the high-volume, fully-encrypted table, so it's split out
// of /api/data (which serves the small all-history overview) and windowed here —
// keeping the first paint light. Every usage view already filters by the window,
// so loading just the window's rows is correct.
async function loadUsage() {
  if (state.winStart == null) return;
  try {
    const res = await fetch(`api/usage?from=${state.winStart}&to=${state.winEnd}`);
    if (!res.ok) return;
    const u = await res.json();
    await decryptData(u); // decrypts u.usage in place (same job collector as the initial data)
    state.data.usage = u.usage || [];
    render();
    if (state.openDay != null) showDay(state.openDay);
  } catch { /* keep current usage */ }
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
  const count = document.getElementById("ignored-count");
  if (count) count.textContent = state.ignored.length ? String(state.ignored.length) : "";
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
        <span class="ign-kw">${esc(e.keyword || "(hidden)")}</span>
        <span class="ign-dom" title="${esc(e.url || "")}">${esc(e.domain || "—")}</span>
        <button class="ign-restore" type="button" title="Restore this violation" data-rid="${esc(e.recordId || "")}">&#8635;</button>
      </div>`;
    })
    .join("");
}

// highlightSelectedDay reflects the open day on BOTH the calendar and the
// timeline, so clicking a day in one shows the selection in the other. The
// calendar gets a ring on the matching cell; the timeline redraws so its overlay
// paints the selected-day band (it reads state.openDay live).
function highlightSelectedDay() {
  document.querySelectorAll(".cal-day[data-ts]").forEach((cell) => {
    cell.classList.toggle("sel", state.openDay != null && dayKey(Number(cell.dataset.ts)) === dayKey(state.openDay));
  });
  const tl = state.charts["chart-timeline"];
  if (tl) tl.draw();
}

function showDay(dayTs) {
  state.openDay = dayTs; // remembered so re-fetches can re-open the same day
  highlightSelectedDay();
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
          <button class="hit-x" type="button" title="Not a violation — hide it" data-rid="${esc(h.recordId || "")}" data-ts="${h.ts}" data-kw="${esc(h.keyword)}" data-url="${esc(h.url)}" data-dom="${esc(h.domain || "")}">&times;</button>
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

// bucketize splits [start, end] into day/week/month buckets, reusing the same
// day≤45 / week≤180 / month thresholds the timeline uses so the exposure bar
// lines up with the rest of History.
function bucketize(start, end) {
  const endc = Math.min(end, state.data.now);
  const days = Math.round((endc - start) / DAY) + 1;
  const gran = days <= 45 ? "day" : days <= 180 ? "week" : "month";
  const out = [];
  if (gran === "day") {
    for (let t = startOfDay(start); t <= endc; t += DAY) {
      out.push({ start: t, end: t + DAY, label: new Date(t).toLocaleDateString(undefined, { month: "short", day: "numeric" }) });
    }
  } else if (gran === "week") {
    let t = startOfDay(start) - weekdayIndex(new Date(start)) * DAY; // Monday of the first week
    while (t <= endc) {
      out.push({ start: t, end: t + 7 * DAY, label: new Date(t).toLocaleDateString(undefined, { month: "short", day: "numeric" }) });
      t += 7 * DAY;
    }
  } else {
    let d = new Date(start); d = new Date(d.getFullYear(), d.getMonth(), 1);
    const last = new Date(endc);
    while (d <= last) {
      const ne = new Date(d.getFullYear(), d.getMonth() + 1, 1);
      out.push({ start: d.getTime(), end: ne.getTime(), label: d.toLocaleDateString(undefined, { month: "short", year: "2-digit" }) });
      d = ne;
    }
  }
  return out;
}

// renderExposure draws the stacked exposure bar: per bucket, hours glocker
// wasn't guarding, split into unmanaged (uninstalled — from lifecycle events)
// and downtime (installed but the heartbeat watchdog saw it stopped). Replaces
// the old flat unmanaged/downtime lists. (Multi-device: both are the current
// single-track signals; per-device semantics land in the liveness rework.)
function renderExposure(unmanaged, downtime, b) {
  const buckets = bucketize(b.start, b.end);
  const now = state.data.now;
  const overlapHrs = (periods, bs, be) => {
    let ms = 0;
    for (const p of periods) {
      const pe = p.open ? now : p.end;
      const s = Math.max(p.start, bs), e = Math.min(pe, be);
      if (e > s) ms += e - s;
    }
    return ms / HOUR_MS;
  };
  const labels = buckets.map((bk) => bk.label);
  const unm = buckets.map((bk) => overlapHrs(unmanaged, bk.start, bk.end));
  const down = buckets.map((bk) => overlapHrs(downtime, bk.start, bk.end));
  // Colours match the tab's shared language: --exposed (pink) is "unmanaged"
  // everywhere (calendar hatch, timeline shading), and downtime is the same
  // family one step back — so there's one "not guarding" colour to remember.
  stackedBar("chart-exposure", labels, [
    { label: "removed", data: unm, color: cssVar("var(--exposed)") },
    { label: "stopped", data: down, color: cssVar("var(--exposed-dim)") },
  ], { yUnit: "h", precision: 1, autoSkipX: true });
}

// renderUnblocksByHour: unblocks aggregated into 24 hour-of-day buckets, stacked
// by reason (same reason→colour map as the timeline markers). Window-agnostic —
// always 24 bars — so it never crowds, and it surfaces the time-of-day pattern
// ("when do I cave"). Sits beside the vulnerability map.
function renderUnblocksByHour(unblocks) {
  const rc = reasonColorMap(unblocks);
  const labels = Array.from({ length: 24 }, (_, h) => (h % 3 === 0 ? String(h).padStart(2, "0") : ""));
  const series = [...rc.keys()].map((r) => {
    const data = new Array(24).fill(0);
    for (const u of unblocks) {
      if (((u.reason || "(none)").trim() || "(none)") === r) data[new Date(u.ts).getHours()]++;
    }
    return { label: r, data, color: rc.get(r) };
  });
  stackedBar("chart-unblock-hour", labels, series, { yUnit: "", precision: 0 });
}

// renderUnblockList shows deliberate temporary grants dated (was a reason→count
// ranklist): date · domain · reason · duration, newest first.
function renderUnblockList(unblocks) {
  const el = document.getElementById("unblock-list");
  if (!el) return;
  if (!unblocks.length) {
    el.innerHTML = `<div class="empty">no unblocks in window</div>`;
    return;
  }
  el.innerHTML = [...unblocks]
    .sort((a, b) => b.ts - a.ts)
    .map((u) => {
      const when = new Date(u.ts).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
      const what = [u.domain, u.reason].filter(Boolean).map(esc).join(" — ") || "unblock";
      const dur = u.restoreTs ? fmtDur(u.restoreTs - u.ts) : "—";
      return `<div class="um">
        <span class="when">${when}</span>
        <span class="why">${what}</span>
        <span class="dur">${dur}</span>
      </div>`;
    })
    .join("");
}

// stackedBar renders a stacked bar chart with a small legend. yUnit suffixes the
// y-axis + tooltip ("h" for hours, "" for counts); precision is the tooltip's
// decimal places.
function stackedBar(id, labels, series, { yUnit = "", precision = 0, autoSkipX = false } = {}) {
  destroy(id);
  const ctx = document.getElementById(id).getContext("2d");
  const opts = baseOpts();
  opts.scales.x.stacked = true;
  // Let Chart.js thin the x labels to what actually fits (day buckets over a
  // long window would otherwise smear into an unreadable row).
  if (autoSkipX) { opts.scales.x.ticks.autoSkip = true; opts.scales.x.ticks.maxTicksLimit = 12; }
  opts.scales.y.stacked = true;
  opts.scales.y.ticks.callback = (v) => v + yUnit;
  opts.plugins.legend = { display: true, position: "bottom", labels: { color: TICK, boxWidth: 10, font: { family: "IBM Plex Mono", size: 10 } } };
  opts.plugins.tooltip = { callbacks: { label: (c) => `${c.dataset.label}: ${c.parsed.y.toFixed(precision)}${yUnit}` } };
  state.charts[id] = new Chart(ctx, {
    type: "bar",
    data: { labels, datasets: series.map((s) => ({ label: s.label, data: s.data, backgroundColor: translucent(s.color), borderColor: s.color, borderWidth: 1, borderRadius: 3, borderSkipped: false })) },
    options: opts,
  });
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
function setTags(tags) {
  state.tags = tags || [];
  state.tagById = new Map();
  state.tagByLabel = new Map();
  for (const t of state.tags) {
    state.tagById.set(t.id, t);
    state.tagByLabel.set(tagLabel(t), t);
  }
}
const tagLabel = (t) => `${t.namespace}:${t.name}`;

// The registry's namespaces, Glocker first then the rest alphabetically. Drives
// the per-namespace Usage breakdown and the per-rule tag slots.
function tagNamespaces() {
  const seen = new Set(state.tags.map((t) => t.namespace));
  const rest = [...seen].filter((n) => n !== "Glocker").sort();
  return (seen.has("Glocker") ? ["Glocker"] : []).concat(rest);
}

function compileRules(rules) {
  return rules.map((r) => {
    let program = null, title = null, error = false;
    try { if (r.program) program = new RegExp(r.program, "i"); } catch { error = true; }
    try { if (r.title) title = new RegExp(r.title, "i"); } catch { error = true; }
    return { program, title, tagIds: r.tagIds || [], error };
  });
}

// matchedTags classifies a sample: `.tags` is the winning display tag per
// namespace (first matching rule per namespace) and `.hidden` says whether the
// window is dropped from all usage totals.
//
// "hide" is a behaviour, not a display category, and a pure LAST RESORT: it never
// occupies a namespace slot, and it hides the window only when NO other (non-hide)
// rule tagged it. So a hide rule can neither be shown as "Glocker:hide" nor
// clobber a specific tag — a broad catch-all like "hide leftover Firefox windows"
// (^firefox-esr$ / Mozilla Firefox) only hides Firefox windows nothing else
// categorized, no matter where it sits in the list.
function matchedTags(e, compiled) {
  const out = new Map(); // namespace -> tag object (display tags only)
  let sawHide = false;
  if (!e.active) return { tags: out, hidden: false };
  const cls = e.active.class || "", title = e.active.title || "";
  for (const c of compiled) {
    if (c.error || !c.tagIds.length) continue;
    if (c.program && !c.program.test(cls)) continue;
    if (c.title && !c.title.test(title)) continue;
    for (const id of c.tagIds) {
      const t = state.tagById.get(id);
      if (!t) continue;
      if (t.behavior === "hide") { sawHide = true; continue; } // last-resort, decided below
      if (!out.has(t.namespace)) out.set(t.namespace, t);
    }
  }
  return { tags: out, hidden: sawHide && out.size === 0 };
}

const UNTAGGED = "(untagged)";

// computeTagUsage aggregates active time once into a per-namespace bucket, each
// shaped like the old single-tag result. Within a namespace every active sample
// is either tagged or untagged, so a namespace's tags + untagged sum to its
// active time (the "per prefix" view). Hidden samples are dropped everywhere.
function computeTagUsage(entries, compiled) {
  const namespaces = tagNamespaces();
  const buckets = new Map(); // ns -> accumulators
  const bucketFor = (ns) => {
    let b = buckets.get(ns);
    if (!b) {
      b = { byTag: new Map(), hourByTag: new Map(), dayByTag: new Map(), windowsByTag: new Map(), untaggedByWindow: new Map(), untagged: 0 };
      buckets.set(ns, b);
    }
    return b;
  };
  const addHour = (m, name, hr, dt) => { let a = m.get(name); if (!a) { a = new Array(24).fill(0); m.set(name, a); } a[hr] += dt; };
  const addDay = (m, name, dk, dt) => { let d = m.get(name); if (!d) { d = new Map(); m.set(name, d); } d.set(dk, (d.get(dk) || 0) + dt); };
  const noTitle = "—";
  const addWindow = (m, name, e, dt) => {
    const program = (e.active.class || "").trim() || "unknown";
    const title = e.active.title || noTitle;
    const wkey = program + " " + title;
    let wm = m.get(name); if (!wm) { wm = new Map(); m.set(name, wm); }
    let w = wm.get(wkey); if (!w) { w = { program, title, ms: 0 }; wm.set(wkey, w); }
    w.ms += dt;
  };
  let active = 0, hidden = 0;

  eachSampleDuration(entries, (e, dt, away) => {
    if (away) return;
    const m = matchedTags(e, compiled);
    if (m.hidden) { hidden += dt; return; }
    const byNs = m.tags;
    active += dt;
    const hr = new Date(e.ts).getHours();
    const dk = dayKey(e.ts);
    for (const ns of namespaces) {
      const b = bucketFor(ns);
      const t = byNs.get(ns);
      const name = t ? tagLabel(t) : null;
      if (name) {
        b.byTag.set(name, (b.byTag.get(name) || 0) + dt);
        addHour(b.hourByTag, name, hr, dt);
        addDay(b.dayByTag, name, dk, dt);
        addWindow(b.windowsByTag, name, e, dt);
      } else {
        b.untagged += dt;
        addHour(b.hourByTag, UNTAGGED, hr, dt);
        addDay(b.dayByTag, UNTAGGED, dk, dt);
        addWindow(b.windowsByTag, UNTAGGED, e, dt);
        const program = (e.active.class || "").trim() || "unknown";
        const title = e.active.title || noTitle;
        const key = program + " " + title;
        let u = b.untaggedByWindow.get(key);
        if (!u) { u = { program, title, ms: 0 }; b.untaggedByWindow.set(key, u); }
        u.ms += dt;
      }
    }
  });

  const byNamespace = new Map();
  for (const ns of namespaces) {
    const b = bucketFor(ns);
    byNamespace.set(ns, {
      tags: rankMap(b.byTag),
      untagged: b.untagged,
      active, hidden,
      hourByTag: b.hourByTag,
      dayByTag: b.dayByTag,
      windowsByTag: b.windowsByTag,
      untaggedItems: [...b.untaggedByWindow.values()].sort((a, c) => c.ms - a.ms),
    });
  }
  return { byNamespace, namespaces, active, hidden };
}

// A per-namespace bucket in the {tags, untagged, ...} shape the renderers use;
// an all-zero bucket when the namespace has no time, so callers never crash.
function tuForNamespace(all, ns) {
  return all.byNamespace.get(ns) || { tags: [], untagged: 0, active: all.active, hidden: all.hidden, hourByTag: new Map(), dayByTag: new Map(), windowsByTag: new Map(), untaggedItems: [] };
}

// Canonical ordering shared by the tag pie and the stacked charts, so a tag
// keeps the same colour across them. Untagged is appended and always grey.
function orderedTagItems(tu) {
  const items = tu.tags.slice();
  if (tu.untagged > 0) items.push({ name: UNTAGGED, ms: tu.untagged, untagged: true });
  items.sort((a, b) => b.ms - a.ms);
  return items;
}

// A tag's colour is its registry colour; untagged is the muted faint ink; a tag
// no longer in the registry falls back to a stable palette slot.
function tagColorMap(items) {
  const map = new Map();
  let ci = 0;
  for (const it of items) {
    if (it.untagged) { map.set(it.name, cssVar("var(--ink-faint)")); continue; }
    const t = state.tagByLabel.get(it.name);
    map.set(it.name, (t && t.color) || TAG_COLORS[ci++ % TAG_COLORS.length]);
  }
  return map;
}

function renderUsage(b) {
  const available = state.data.sources && state.data.sources.usage;
  const entries = (state.data.usage || []).filter((e) => within(e.ts, b) && inDevice(e));
  // Stash the in-window samples so rule edits can recategorize without a
  // full re-render of the rest of the view.
  state.usageWindow = { entries, available, b };
  const u = computeUsage(entries);

  renderUsageReadout(u, available);
  recategorize();
}

// Recompute and render the per-namespace tag panels from the current window and
// rules — cheap enough to run on every keystroke in the rule editor.
function recategorize() {
  const win = state.usageWindow;
  if (!win) return;
  const all = computeTagUsage(win.entries, compileRules(state.rules));
  // Only offer namespaces that actually produced a tag in this window — a
  // namespace whose only rules are hide rules (or that nothing matched) would be
  // all-untagged and just noise. Fall back to every namespace if none tagged.
  const withTags = all.namespaces.filter((ns) => tuForNamespace(all, ns).tags.length);
  const shown = withTags.length ? withTags : all.namespaces;
  // Keep the current selection only if it's still on offer; else pick the first.
  if (!state.usageNamespace || !shown.includes(state.usageNamespace)) {
    state.usageNamespace = shown[0] || null;
  }
  renderNamespacePills(shown);
  const tu = tuForNamespace(all, state.usageNamespace);
  renderUsagePerDay(tu, win.available, win.b);
  renderUsageTags(tu, win.available);
  renderUsageTagHours(tu, win.available);
  renderUntagged(tu, win.available);
}

// One pill per namespace on offer (see recategorize); clicking one re-renders the
// tag charts for that namespace (the "per prefix" view). Hidden when there are
// none, or only one (nothing to switch between).
function renderNamespacePills(namespaces) {
  const el = document.getElementById("usage-ns-pills");
  if (!el) return;
  if (namespaces.length <= 1) { el.innerHTML = ""; el.hidden = true; return; }
  el.hidden = false;
  el.innerHTML = namespaces
    .map((ns) => `<button type="button" class="ns-pill${ns === state.usageNamespace ? " active" : ""}" data-ns="${esc(ns)}">${esc(ns)}</button>`)
    .join("");
}

// The active time no rule matched, grouped by program with an example title, so
// the user can see what still needs a rule (and click to start one).
function renderUntagged(tu, available) {
  const el = document.getElementById("usage-untagged");
  const totalEl = document.getElementById("untagged-total");
  const setTotal = (t) => { if (totalEl) totalEl.textContent = t; };
  if (!available) {
    setTotal("tracking off");
    el.innerHTML = `<div class="empty">usage tracking is off</div>`;
    return;
  }
  const hiddenNote = tu.hidden > 0
    ? `<div style="margin-top:10px;font-size:.82rem;color:#8b97a8;">${fmtDur(tu.hidden)} hidden by rules</div>`
    : "";
  const items = tu.untaggedItems || [];
  if (!items.length) {
    setTotal("none ✓");
    el.innerHTML = `<div class="empty">nothing untagged — full coverage ✓</div>` + hiddenNote;
    return;
  }
  const top = items.slice(0, 10);
  const total10 = top.reduce((s, it) => s + it.ms, 0);
  // Summary line (visible while collapsed): how much time the top 10 add up to,
  // so a trivial amount can stay tucked away.
  setTotal(`${fmtDur(total10)}${items.length > 10 ? ` · top 10 of ${items.length}` : ` · ${items.length} window${items.length === 1 ? "" : "s"}`}`);
  const max = items[0].ms || 1;
  el.innerHTML = top
    .map((it) => {
      const pct = Math.round((it.ms / max) * 100);
      return `<div class="untag">
        <div class="untag-info">
          <span class="untag-prog">${esc(it.program)}</span>
          <span class="untag-title" title="${esc(it.title)}">${esc(it.title)}</span>
        </div>
        <span class="untag-time">${esc(fmtDur(it.ms))}</span>
        <button class="untag-tag" type="button" data-tag-program="${esc(it.program)}" data-tag-title="${esc(it.title)}" title="Start a rule for this window">&plus; tag</button>
        <button class="untag-hide" type="button" data-hide-program="${esc(it.program)}" data-hide-title="${esc(it.title)}" title="Hide this window from all usage stats">hide</button>
        <span class="untag-bar"><span style="width:${pct}%"></span></span>
      </div>`;
    })
    .join("") + hiddenNote;
}

// Start a rule for a specific window: append a prefilled row (program anchored,
// title as an escaped starting point the user can generalize), focus its tag
// field, and let the user name it and Save.
function startRuleForWindow(program, title) {
  syncRulesFromDom();
  state.rules.push({
    program: program ? `^${escapeRegex(program)}$` : "",
    title: title && title !== "—" ? `^${escapeRegex(title)}$` : "",
    tagIds: [],
  });
  renderRulesEditor();
  markRuleValidity();
  const rows = document.querySelectorAll("#rules-rows .rule-row");
  const last = rows[rows.length - 1];
  if (last) {
    last.scrollIntoView({ behavior: "smooth", block: "center" });
    const sel = last.querySelector("select");
    if (sel) sel.focus();
  }
  recategorize();
}

// hideWindow adds a rule tagging this exact window Glocker:hide and saves it
// immediately, so the window drops out of every usage total in one click.
async function hideWindow(program, title) {
  const hid = hideTagId();
  if (!hid) { setRulesStatus("can’t hide — hide rule unavailable", "err"); return; }
  syncRulesFromDom();
  state.rules.push({
    program: program ? `^${escapeRegex(program)}$` : "",
    title: title && title !== "—" ? `^${escapeRegex(title)}$` : "",
    tagIds: [hid],
  });
  renderRulesEditor();
  await saveRulesToServer();
  recategorize();
}

// The registry id of the Glocker:hide tag (its windows are dropped from all
// usage totals), or null when it is not present.
function hideTagId() {
  const t = state.tagByLabel.get("Glocker:hide");
  return t ? t.id : null;
}

function escapeRegex(s) {
  return String(s).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// Categorical palette for the tag pie (distinct hues that read on the dark
// theme). "(untagged)" is always rendered in the muted faint ink instead.
// Fallback palette for user tags without a set colour, and for unblock-reason
// colours. Same muted family as the Glocker:* defaults (see store/seed.go),
// ordered so neighbours in a stack stay distinguishable.
const TAG_COLORS = [
  "#6f9de0", "#e0a94e", "#4bb8a5", "#d9789f", "#92c26f",
  "#a98fe0", "#5cc4d6", "#e08a5c", "#8493e6", "#d76b6b",
];

// reasonColorMap assigns each unblock reason a stable colour from the palette
// (first-appearance order, sorted so it's deterministic across renders). Shared
// by the timeline unblock markers and the by-hour chart so a reason is the same
// colour in both. Blank reasons collapse to "(none)".
function reasonColorMap(unblocks) {
  const reasons = [...new Set(unblocks.map((u) => (u.reason || "(none)").trim() || "(none)"))].sort();
  const m = new Map();
  reasons.forEach((r, i) => m.set(r, TAG_COLORS[i % TAG_COLORS.length]));
  return m;
}

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
    data: { labels, datasets: [{ data: msValues, backgroundColor: translucent(colors), borderColor: colors, borderWidth: 1, borderRadius: 4, spacing: 2, hoverOffset: 6 }] },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      cutout: "55%",
      onHover: onHoverLabel
        ? (_e, els) => onHoverLabel(els.length ? labels[els[0].index] : null)
        : undefined,
      plugins: {
        legend: { position: "right", labels: { color: TICK, font: { family: "IBM Plex Mono", size: 10 }, boxWidth: 10, padding: 6 } },
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
  const spanDays = Math.floor((end - start) / DAY) + 1;
  // Bucket granularity by span so a long window ("all") stays compact and readable
  // instead of thousands of hairline bars: daily ≤45d, weekly ≤~7mo, else monthly.
  const mode = spanDays <= 45 ? "day" : spanDays <= 210 ? "week" : "month";

  // Roll each day into its bucket, summing ms per tag. Buckets keep insertion order.
  const order = [];
  const labelOf = new Map(), titleOf = new Map(), sums = new Map();
  for (let t = start; t <= end; t += DAY) {
    const d = new Date(t);
    let key, label, title;
    if (mode === "day") {
      key = dayKey(t);
      const date = d.getDate() === 1 ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) : String(d.getDate());
      label = [d.toLocaleDateString(undefined, { weekday: "short" }), date];
      title = d.toLocaleDateString(undefined, { weekday: "long", month: "short", day: "numeric" });
    } else if (mode === "week") {
      const ws = startOfDay(t - ((d.getDay() + 6) % 7) * DAY); // Monday of this week
      key = "w" + dayKey(ws);
      label = new Date(ws).toLocaleDateString(undefined, { month: "short", day: "numeric" });
      title = `Week of ${label}`;
    } else {
      key = d.getFullYear() + "-" + d.getMonth();
      label = d.toLocaleDateString(undefined, { month: "short" });
      title = d.toLocaleDateString(undefined, { month: "long", year: "numeric" });
    }
    if (!sums.has(key)) { order.push(key); labelOf.set(key, label); titleOf.set(key, title); sums.set(key, new Map()); }
    const s = sums.get(key);
    for (const it of items) {
      const ms = (tu.dayByTag.get(it.name) || new Map()).get(dayKey(t)) || 0;
      if (ms) s.set(it.name, (s.get(it.name) || 0) + ms);
    }
  }

  const colors = tagColorMap(items);
  const datasets = items.map((it) => ({
    label: it.name,
    data: order.map((k) => (sums.get(k).get(it.name) || 0) / 3600000),
    backgroundColor: colors.get(it.name),
    stack: "day",
    borderWidth: 0,
    maxBarThickness: 40,
  }));

  // Bucket total on top: for daily, its share of the 24-hour day; for week/month
  // just the total (a share-of-period is less meaningful there).
  const topLabels = order.map((k) => {
    const activeMs = items.reduce((a, it) => a + (sums.get(k).get(it.name) || 0), 0);
    if (activeMs <= 0) return "";
    return mode === "day" ? `${fmtDur(activeMs)} (${Math.round((activeMs / DAY) * 100)}%)` : fmtDur(activeMs);
  });
  const xLabels = order.map((k) => labelOf.get(k));
  const titles = order.map((k) => titleOf.get(k));

  // Top labels only when the bars are wide enough not to collide.
  stackedBarChart("chart-usage-perday", xLabels, datasets, order.length <= 40 ? topLabels : null, titles);
}

// ── Rule editor ─────────────────────────────────────────
function setupRules() {
  renderRulesEditor();

  const normalRows = document.getElementById("rules-rows");
  const hideRows = document.getElementById("hide-rows");

  // Live feedback on the program/title regexes: sync + recategorize on every
  // keystroke, without re-rendering (which would drop focus). Shared by both lists.
  const onInput = (e) => {
    if (e.target.closest("select")) return; // selects handled on change
    syncRulesFromDom();
    markRuleValidity();
    recategorize();
    setRulesStatus("unsaved change — click Save rules", "");
  };
  const onDelete = (e) => {
    const del = e.target.closest(".rule-del");
    if (!del) return;
    del.closest(".rule-row").remove();
    syncRulesFromDom();
    renderRulesEditor();
    markRuleValidity();
    recategorize();
    setRulesStatus("unsaved change — click Save rules", "");
  };
  for (const el of [normalRows, hideRows]) {
    el.addEventListener("input", onInput);
    el.addEventListener("click", onDelete);
  }
  // A tag slot changed (categorization rows only).
  normalRows.addEventListener("change", (e) => {
    if (!e.target.closest("select.rule-tag")) return;
    syncRulesFromDom();
    recategorize();
    setRulesStatus("unsaved change — click Save rules", "");
  });

  // Drag-to-reorder the categorization rules by the handle (precedence). Hidden
  // windows are a last resort, so they aren't ordered.
  let dragFrom = null;
  normalRows.addEventListener("dragstart", (e) => {
    const h = e.target.closest(".rule-drag");
    if (!h) return;
    dragFrom = Number(h.closest(".rule-row").dataset.i);
    e.dataTransfer.effectAllowed = "move";
    h.closest(".rule-row").classList.add("dragging");
  });
  normalRows.addEventListener("dragend", () => {
    dragFrom = null;
    normalRows.querySelectorAll(".dragging, .drop-target").forEach((el) => el.classList.remove("dragging", "drop-target"));
  });
  normalRows.addEventListener("dragover", (e) => {
    if (dragFrom == null) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    const row = e.target.closest(".rule-row");
    normalRows.querySelectorAll(".drop-target").forEach((el) => el.classList.remove("drop-target"));
    if (row) row.classList.add("drop-target");
  });
  normalRows.addEventListener("drop", (e) => {
    if (dragFrom == null) return;
    e.preventDefault();
    const row = e.target.closest(".rule-row");
    if (!row) return;
    const to = Number(row.dataset.i);
    if (to === dragFrom) return;
    syncRulesFromDom();
    // Categorization rules occupy the front of state.rules (see syncRulesFromDom),
    // so the handle indices map straight onto it.
    const [moved] = state.rules.splice(dragFrom, 1);
    state.rules.splice(to, 0, moved);
    dragFrom = null;
    renderRulesEditor();
    markRuleValidity();
    recategorize();
    setRulesStatus("reordered — click Save rules", "");
  });

  document.getElementById("rule-add").addEventListener("click", () => {
    syncRulesFromDom();
    state.rules.push({ program: "", title: "", tagIds: [] });
    renderRulesEditor();
    markRuleValidity();
  });
  document.getElementById("hide-add").addEventListener("click", () => {
    const hid = hideTagId();
    if (!hid) { setRulesStatus("can’t hide — hide rule unavailable", "err"); return; }
    syncRulesFromDom();
    state.rules.push({ program: "", title: "", tagIds: [hid] });
    renderRulesEditor();
    markRuleValidity();
  });
  document.getElementById("rule-save").addEventListener("click", saveRulesToServer);

  // "＋ tag" prefills a categorization rule for that window; "hide" adds a
  // hidden-window rule directly (no typing).
  document.getElementById("usage-untagged").addEventListener("click", (e) => {
    const hideBtn = e.target.closest("[data-hide-program]");
    if (hideBtn) { hideWindow(hideBtn.dataset.hideProgram, hideBtn.dataset.hideTitle || ""); return; }
    const btn = e.target.closest("[data-tag-program]");
    if (btn) startRuleForWindow(btn.dataset.tagProgram, btn.dataset.tagTitle || "");
  });

  // Namespace pills switch which prefix the Usage tag charts break down.
  document.getElementById("usage-ns-pills").addEventListener("click", (e) => {
    const btn = e.target.closest(".ns-pill");
    if (!btn) return;
    state.usageNamespace = btn.dataset.ns;
    recategorize();
  });

  setupTags();
}

function setRulesStatus(msg, cls) {
  const s = document.getElementById("rules-status");
  s.className = "rules-status" + (cls ? " " + cls : "");
  s.textContent = msg;
}

// A rule whose tags include the Glocker:hide tag is a "hidden window" rule and
// lives in its own section; everything else is a categorization rule.
function isHideRule(r) {
  const hid = hideTagId();
  return hid != null && (r.tagIds || []).includes(hid);
}

function renderRulesEditor() {
  const normal = state.rules.filter((r) => !isHideRule(r));
  const hidden = state.rules.filter(isHideRule);

  const wrap = document.getElementById("rules-rows");
  if (!normal.length) {
    wrap.innerHTML = `<div class="rules-empty">No rules yet — add one to categorize your active time.</div>`;
  } else {
    const namespaces = tagNamespaces();
    wrap.innerHTML = normal
      .map((r, i) => {
        const selected = new Map(); // ns -> tagId (first per namespace wins)
        for (const id of (r.tagIds || [])) {
          const t = state.tagById.get(id);
          if (t && t.behavior !== "hide" && !selected.has(t.namespace)) selected.set(t.namespace, id);
        }
        const slots = namespaces.map((ns) => tagSlot(ns, selected.get(ns))).join("");
        return `<div class="rule-row" data-i="${i}">
          <span class="rule-drag" draggable="true" title="Drag to reorder">⋮⋮</span>
          <input data-field="program" value="${esc(r.program || "")}" placeholder="^Emacs$" spellcheck="false" autocomplete="off" />
          <input data-field="title" value="${esc(r.title || "")}" placeholder="regex on title" spellcheck="false" autocomplete="off" />
          <div class="rule-tags">${slots || `<span class="rule-tags-empty">add tags on the Tags sub-tab</span>`}</div>
          <button class="rule-del" title="Delete rule" type="button">&times;</button>
        </div>`;
      })
      .join("");
  }

  const hwrap = document.getElementById("hide-rows");
  if (hwrap) {
    hwrap.innerHTML = hidden.length
      ? hidden
          .map((r) => `<div class="rule-row hide-row">
            <input data-field="program" value="${esc(r.program || "")}" placeholder="^firefox-esr$" spellcheck="false" autocomplete="off" />
            <input data-field="title" value="${esc(r.title || "")}" placeholder="^Exact title$" spellcheck="false" autocomplete="off" />
            <button class="rule-del" title="Remove" type="button">&times;</button>
          </div>`)
          .join("")
      : `<div class="rules-empty">No hidden windows. Add one to drop specific windows from usage.</div>`;
  }
}

// One tag slot for a namespace: a small namespace label plus a <select> of that
// namespace's tags. Hide is a behaviour managed in the Hidden-windows section, so
// it's not offered here. One tag per namespace, so a single slot is enough.
function tagSlot(ns, selectedId) {
  const opts = state.tags
    .filter((t) => t.namespace === ns && t.behavior !== "hide")
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((t) => `<option value="${t.id}"${t.id === selectedId ? " selected" : ""}>${esc(t.name)}</option>`)
    .join("");
  return `<label class="rule-slot"><span class="rule-slot-ns">${esc(ns)}</span><select class="rule-tag" data-ns="${esc(ns)}" title="${esc(ns)} tag"><option value="">—</option>${opts}</select></label>`;
}

// Rebuild state.rules from both editors: categorization rules first (their order
// is precedence), then the hidden-window rules (a last resort, so order among them
// doesn't matter). Each hide row carries the Glocker:hide tag implicitly.
function syncRulesFromDom() {
  const hid = hideTagId();
  const normal = [...document.querySelectorAll("#rules-rows .rule-row")].map((row) => ({
    program: row.querySelector('[data-field="program"]').value.trim(),
    title: row.querySelector('[data-field="title"]').value.trim(),
    tagIds: [...row.querySelectorAll("select.rule-tag")].map((s) => Number(s.value)).filter((n) => n > 0),
  }));
  const hidden = [...document.querySelectorAll("#hide-rows .rule-row")].map((row) => ({
    program: row.querySelector('[data-field="program"]').value.trim(),
    title: row.querySelector('[data-field="title"]').value.trim(),
    tagIds: hid ? [hid] : [],
  }));
  state.rules = normal.concat(hidden);
}

// Flag rows whose program/title is not a valid regex so the user sees why a rule
// isn't matching (both editors).
function markRuleValidity() {
  for (const row of document.querySelectorAll("#rules-rows .rule-row, #hide-rows .rule-row")) {
    let bad = false;
    for (const f of ["program", "title"]) {
      const inp = row.querySelector(`[data-field="${f}"]`);
      const v = inp ? inp.value.trim() : "";
      if (v) { try { new RegExp(v); } catch { bad = true; } }
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
    const payload = { rules: state.rules.map((r, i) => ({ position: i, program: r.program, title: r.title, tagIds: r.tagIds || [] })) };
    const res = await fetch("api/rules", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!res.ok) throw new Error(await errText(res, `server returned ${res.status}`));
    const cfg = await res.json(); // canonical, server-validated
    state.rules = cfg.rules || [];
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

// ── Tag registry editor (Tags sub-tab) ──────────────────
function setupTags() {
  const reg = document.getElementById("tags-registry");
  // Commit colour/distraction on change (not every input event, which would
  // spam the server while dragging the colour picker).
  reg.addEventListener("change", (e) => {
    const row = e.target.closest(".tagreg-row");
    if (!row) return;
    const id = Number(row.dataset.id);
    if (e.target.classList.contains("tagreg-color")) patchTag(id, { color: e.target.value });
    else if (e.target.classList.contains("tagreg-dist-cb")) patchTag(id, { distraction: e.target.checked });
  });
  reg.addEventListener("click", (e) => {
    const del = e.target.closest(".tagreg-del");
    if (del) { deleteTag(Number(del.closest(".tagreg-row").dataset.id)); return; }
    const add = e.target.closest(".tagreg-add-btn");
    if (add) {
      const inp = add.closest(".tagreg-add").querySelector(".tagreg-newname");
      const name = inp ? inp.value.trim() : "";
      if (name) createTag(add.dataset.ns, name);
    }
  });
  reg.addEventListener("keydown", (e) => {
    if (e.key !== "Enter") return;
    const inp = e.target.closest(".tagreg-newname");
    if (!inp) return;
    const name = inp.value.trim();
    if (name) createTag(inp.dataset.ns, name);
  });

  document.getElementById("tags-add-ns-btn").addEventListener("click", () => {
    const inp = document.getElementById("tags-new-ns");
    const ns = inp.value.trim();
    if (!ns) return;
    if (ns === "Glocker") { setTagsStatus("Glocker is the shared namespace — pick another name", "err"); return; }
    state.pendingNamespace = ns;
    inp.value = "";
    renderTags();
    setTagsStatus(`namespace “${ns}” — add a tag to keep it`, "");
  });
}

function renderTags() {
  const el = document.getElementById("tags-registry");
  if (!el) return;
  const namespaces = tagNamespaces();
  if (state.pendingNamespace && !namespaces.includes(state.pendingNamespace)) namespaces.push(state.pendingNamespace);
  if (!namespaces.length) {
    el.innerHTML = `<div class="empty">No tags yet — add a namespace below to start.</div>`;
    return;
  }
  el.innerHTML = namespaces
    .map((ns) => {
      const isSystem = ns === "Glocker";
      const tags = state.tags.filter((t) => t.namespace === ns).sort((a, b) => a.name.localeCompare(b.name));
      const rows = tags.map((t) => tagRow(t, isSystem)).join("") || `<div class="empty">no tags yet</div>`;
      const add = isSystem ? "" : `
        <div class="tagreg-add">
          <input class="tagreg-newname" data-ns="${esc(ns)}" placeholder="new tag name" spellcheck="false" autocomplete="off" />
          <button class="btn-ghost tagreg-add-btn" data-ns="${esc(ns)}" type="button">+ Add tag</button>
        </div>`;
      return `<section class="tagreg-ns">
        <h3 class="tagreg-ns-head">${esc(ns)}${isSystem ? ` <span class="tagreg-badge">shared</span>` : ""}</h3>
        <div class="tagreg-list">${rows}</div>
        ${add}
      </section>`;
    })
    .join("");
}

// One tag row. System (Glocker) tags: distraction is editable (stored as this
// account's override); colour/name are fixed and there's no delete (the shared
// namespace is already flagged in its header, so rows carry no extra marker).
function tagRow(t, isSystem) {
  const dist = t.distraction ? " checked" : "";
  return `<div class="tagreg-row" data-id="${t.id}">
    <input type="color" class="tagreg-color" value="${esc(t.color || "#7c9cff")}"${isSystem ? " disabled" : ""} title="${isSystem ? "Shared tag colour is fixed" : "Tag colour"}" />
    <span class="tagreg-name">${esc(t.name)}</span>
    <label class="switch" title="Count this tag as a distraction in the work-hours insights">
      <input type="checkbox" class="tagreg-dist-cb"${dist} />
      <span class="switch-track"><span class="switch-thumb"></span></span>
      <span class="switch-text">distraction</span>
    </label>
    ${isSystem ? `` : `<button class="tagreg-del" type="button" title="Delete tag">&times;</button>`}
  </div>`;
}

function setTagsStatus(msg, cls) {
  const s = document.getElementById("tags-status");
  if (!s) return;
  s.className = "rules-status" + (cls ? " " + cls : "");
  s.textContent = msg;
}

// Re-fetch the registry and re-render everything that depends on it (the editor,
// the rule tag slots, and the Usage charts).
async function reloadTags() {
  try {
    const res = await fetch("api/tags");
    if (res.ok) setTags((await res.json()).tags || []);
  } catch { /* keep current registry */ }
  renderTags();
  renderRulesEditor();
  markRuleValidity();
  recategorize();
}

async function createTag(namespace, name) {
  setTagsStatus("saving…", "");
  try {
    const res = await fetch("api/tags", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ namespace, name, color: randomTagColor(), distraction: false }),
    });
    if (!res.ok) { setTagsStatus(await errText(res, "add failed"), "err"); return; }
    if (state.pendingNamespace === namespace) state.pendingNamespace = null;
    await reloadTags();
    setTagsStatus(`added ${namespace}:${name} ✓`, "ok");
  } catch (err) { setTagsStatus(`add failed: ${err.message}`, "err"); }
}

async function patchTag(id, fields) {
  setTagsStatus("saving…", "");
  try {
    const res = await fetch(`api/tags?id=${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(fields),
    });
    if (!res.ok) { setTagsStatus(await errText(res, "update failed"), "err"); return; }
    await reloadTags();
    setTagsStatus("saved ✓", "ok");
  } catch (err) { setTagsStatus(`update failed: ${err.message}`, "err"); }
}

async function deleteTag(id) {
  const t = state.tagById.get(id);
  if (t && !window.confirm(`Delete tag ${tagLabel(t)}? It will be removed from any rules that use it.`)) return;
  setTagsStatus("deleting…", "");
  try {
    const res = await fetch(`api/tags?id=${id}`, { method: "DELETE" });
    if (!res.ok) { setTagsStatus(await errText(res, "delete failed"), "err"); return; }
    await reloadTags();
    setTagsStatus("deleted ✓", "ok");
  } catch (err) { setTagsStatus(`delete failed: ${err.message}`, "err"); }
}

// Read a short error message from a failed response (plain text or {error} JSON),
// falling back to a default.
async function errText(res, fallback) {
  try {
    const t = (await res.text()).trim();
    if (!t) return fallback;
    try { const j = JSON.parse(t); if (j && j.error) return j.error; } catch { /* not JSON */ }
    return t.slice(0, 200);
  } catch { return fallback; }
}

function randomTagColor() {
  return TAG_COLORS[Math.floor(Math.random() * TAG_COLORS.length)];
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

// The footer build tag is now server-rendered into the shell template
// (dashboard.html.tmpl, injected from buildVersion), so there's nothing to do
// here. Kept as a no-op so existing callers don't need touching.
function renderFooter() { /* server-rendered */ }

// ── Chart.js helpers ────────────────────────────────────
function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name.replace("var(", "").replace(")", "")).trim();
}
const GRID = "rgba(255,255,255,0.08)";
const TICK = "#aeb9c9";

// Chart fills read better translucent, with a solid same-colour border keeping
// the shape crisp (the same treatment the violations area chart uses). FILL_ALPHA
// is a hex alpha suffix appended to a #rrggbb colour; non-hex colours pass through.
const FILL_ALPHA = "b3"; // ~0.70
const fillOf = (c) => (typeof c === "string" && /^#[0-9a-f]{6}$/i.test(c) ? c + FILL_ALPHA : c);
const translucent = (c) => (Array.isArray(c) ? c.map(fillOf) : fillOf(c));

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
      border: { display: false },
      ticks: { color: TICK, font: { family: "IBM Plex Mono", size: 10 }, callback: (v) => `${v}%` },
    },
    y: {
      grid: { display: false },
      border: { display: false },
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
      datasets: [{ data: pct, backgroundColor: translucent(colors), borderColor: colors, borderWidth: 1, borderRadius: 6, borderSkipped: false, maxBarThickness: 26 }],
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
  // Over a long window (e.g. the per-day chart on "all") every label would draw on
  // top of the next into an unreadable smear — let Chart.js thin them out. Short
  // charts (≤31 categories) keep every label.
  if (labels.length > 31) {
    opts.scales.x.ticks.autoSkip = true;
    opts.scales.x.ticks.maxTicksLimit = 16;
  }
  opts.scales.y.ticks.callback = (v) => `${v}h`;
  opts.plugins.legend = {
    display: true,
    position: "bottom",
    labels: { color: TICK, font: { family: "IBM Plex Mono", size: 10 }, boxWidth: 12, padding: 8 },
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

  // Translucent fills, a thin solid outline, and rounded corners turn each
  // stacked piece into its own soft rounded block — reads more modern than flat
  // rectangles. categoryPercentage slims the bars for a bit of breathing room.
  const shaded = datasets.map((d) => ({
    ...d,
    backgroundColor: translucent(d.backgroundColor),
    borderColor: d.backgroundColor,
    borderWidth: 1,
    borderRadius: 3,
    borderSkipped: false,
    categoryPercentage: 0.7,
    barPercentage: 0.9,
  }));
  const cfg = { type: "bar", data: { labels, datasets: shaded }, options: opts };

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
    data: { labels, datasets: [{ data, backgroundColor: translucent(color), borderColor: color, borderWidth: 1, borderRadius: 6, borderSkipped: false, maxBarThickness: 34 }] },
    options: baseOpts(),
  });
}

function lineChart(id, labels, data, { onPoint, gridAt, underlay, overlay } = {}) {
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
  // underlay draws behind the line (unmanaged shading); overlay on top (unblock
  // markers). Both get (chart) and use its chartArea for time→pixel mapping.
  const xplugin = (underlay || overlay) ? [{
    id: id + "-xdraw",
    beforeDatasetsDraw: (c) => { if (underlay) underlay(c); },
    afterDatasetsDraw: (c) => { if (overlay) overlay(c); },
  }] : [];
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
    plugins: xplugin,
  });
}

function baseOpts() {
  return {
    responsive: true,
    maintainAspectRatio: false,
    plugins: { legend: { display: false }, tooltip: { intersect: false, mode: "index" } },
    scales: {
      x: { grid: { display: false }, border: { display: false }, ticks: { color: TICK, font: { family: "IBM Plex Mono", size: 10 }, maxRotation: 0, autoSkip: false } },
      y: { beginAtZero: true, grid: { color: GRID }, border: { display: false }, ticks: { color: TICK, font: { family: "IBM Plex Mono", size: 10 }, precision: 0 } },
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
  if (u.subStatus) email.append(badge("sub", u.subStatus));
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

  // Clear subscription — reset to free tier (for when Polar and glockpeek drift).
  if (u.subStatus) {
    const clearSub = document.createElement("button");
    clearSub.className = "btn-ghost";
    clearSub.type = "button";
    clearSub.textContent = "Clear subscription";
    clearSub.onclick = async () => {
      if (!confirm(`Reset ${u.email} to the free tier? Clears the subscription status and drops the device cap back to free.`)) return;
      try { await fetch(`api/admin/users?id=${u.id}`, { method: "PATCH" }); } catch { /* re-render reflects state */ }
      renderUsers();
    };
    actions.append(clearSub);
  }

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
  const modal = document.getElementById("device-modal");
  const closeModal = () => {
    modal.hidden = true;
    document.getElementById("device-config").textContent = "";
  };

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
    modal.hidden = false;
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
  document.getElementById("device-done").onclick = closeModal;
  // Dismiss on backdrop click or Esc — the warning already says copy first.
  modal.addEventListener("click", (e) => { if (e.target === modal) closeModal(); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !modal.hidden) closeModal(); });

  const manage = document.getElementById("manage-billing");
  if (manage) manage.onclick = () => openBillingPortal(manage);
}

// openBillingPortal redirects to the Polar customer portal (manage/cancel).
async function openBillingPortal(btn) {
  if (btn) btn.disabled = true;
  try {
    const res = await fetch("api/billing/portal", { method: "POST" });
    if (!res.ok) throw new Error(`server returned ${res.status}`);
    const { url } = await res.json();
    if (!url) throw new Error("no portal url");
    location.href = url;
  } catch {
    if (btn) btn.disabled = false;
    alert("Could not open the billing portal. Please try again in a moment.");
  }
}

// renderAccessBanner reflects the account's subscription state at the top of the
// dashboard: a trial countdown, a lapsed read-only warning with a Subscribe CTA,
// or a start-trial CTA for a never-subscribed account. Sets state.readOnly, which
// write controls consult. No-op when billing is off (local / self-host).
function renderAccessBanner() {
  const el = document.getElementById("access-banner");
  if (!el) return;
  const me = state.me || {};
  if (!me.billing) { el.hidden = true; state.readOnly = false; return; }

  const s = me.subStatus;
  const hasAccess = s === "trialing" || s === "active" || s === "canceled";
  state.readOnly = !hasAccess;

  el.className = "access-banner";
  el.textContent = "";
  const btn = (label, fn) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn-primary";
    b.textContent = label;
    b.onclick = () => fn(b);
    return b;
  };

  if (s === "trialing") {
    const end = me.subCurrentEnd ? new Date(me.subCurrentEnd) : null;
    const days = end ? Math.max(0, Math.ceil((end - Date.now()) / 86400000)) : null;
    el.classList.add("access-banner--info");
    el.append(document.createTextNode(days != null
      ? `Free trial — ${days} day${days === 1 ? "" : "s"} left, then your subscription begins. `
      : "Free trial active. "));
    el.append(btn("Manage billing", openBillingPortal));
    el.hidden = false;
    return;
  }
  if (hasAccess) { el.hidden = true; return; } // active / canceled: no banner

  el.classList.add("access-banner--warn");
  if (s === "expired") {
    el.append(document.createTextNode("Your subscription has lapsed — the dashboard is read-only and syncing is paused. "));
    el.append(btn("Subscribe", startCheckout));
  } else {
    el.append(document.createTextNode("Start your 2-week free trial to connect devices and use the dashboard. "));
    el.append(btn("Start free trial", startCheckout));
  }
  el.hidden = false;
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
  el.hidden = !show;
  if (!show) return;
  el.textContent = "";
  // When billing is configured, offer a real one-click upgrade (hosted checkout
  // via the Merchant-of-Record). Otherwise fall back to the manual note.
  if (state.me && state.me.billing) {
    el.append(document.createTextNode("You've reached your device limit. Upgrade for more devices. "));
    const btn = document.createElement("button");
    btn.className = "btn-primary";
    btn.type = "button";
    btn.textContent = "Upgrade";
    btn.onclick = () => startCheckout(btn);
    el.append(btn);
  } else {
    el.textContent = "You've reached your device limit. More devices are a paid feature — reply to your welcome email to upgrade.";
  }
}

// startCheckout asks the server for a hosted checkout URL and redirects there.
// Payment happens entirely on the Merchant-of-Record's page — no card details
// ever touch glockpeek.
async function startCheckout(btn) {
  if (btn) { btn.disabled = true; btn.textContent = "Starting…"; }
  try {
    const res = await fetch("api/billing/checkout", { method: "POST" });
    if (!res.ok) throw new Error(`server returned ${res.status}`);
    const { url } = await res.json();
    if (!url) throw new Error("no checkout url");
    location.href = url;
  } catch (err) {
    if (btn) { btn.disabled = false; btn.textContent = "Upgrade"; }
    alert("Could not start checkout. Please try again in a moment.");
  }
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
  // Surface the current plan alongside usage.
  const sub = state.me && state.me.subStatus;
  if (sub === "active") {
    usageEl.textContent += "  ·  Pro plan (active)";
  } else if (sub === "canceled") {
    usageEl.textContent += "  ·  Pro plan (cancels at period end)";
  }
  // Manage/cancel is available once there's a subscription (active or winding
  // down); an expired/free account sees the upgrade CTA instead.
  const manage = document.getElementById("manage-billing");
  if (manage) manage.hidden = !(state.me && state.me.billing && (sub === "active" || sub === "canceled"));

  listEl.textContent = "";
  if (!data.tokens.length) {
    const empty = document.createElement("div");
    empty.className = "device-empty";
    empty.textContent = "No devices connected yet.";
    listEl.append(empty);
  }
  for (const t of data.tokens) listEl.append(deviceRow(t));

  const atLimit = !data.canAdd;
  // Read-only accounts can't connect devices (the banner explains why); the
  // device-limit upsell only applies to accounts that still have access.
  nameEl.disabled = atLimit || state.readOnly;
  addBtn.disabled = atLimit || state.readOnly;
  showDeviceUpsell(atLimit && !state.readOnly);

  // Keep the analytics device switcher in step with rename/revoke done here, and
  // re-derive exposure so a just-revoked device drops out of Combined too.
  state.devices = data.tokens || [];
  populateDeviceFilter();
  scopeExposure();
}

function deviceRow(t) {
  const row = document.createElement("div");
  row.className = "device-row";
  const revoked = t.revokedAt != null;
  if (revoked) row.classList.add("device-row--revoked");

  const meta = document.createElement("div");
  meta.className = "device-meta";
  const nameLine = document.createElement("div");
  nameLine.className = "device-name-line";
  const name = document.createElement("span");
  name.className = "device-name";
  name.textContent = t.name || "device";
  nameLine.append(name);
  if (revoked) {
    const badge = document.createElement("span");
    badge.className = "device-badge";
    badge.textContent = "revoked";
    nameLine.append(badge);
  }
  const sub = document.createElement("span");
  sub.className = "device-sub";
  if (revoked) {
    sub.textContent = `revoked ${new Date(t.revokedAt).toLocaleDateString()} · history kept`;
  } else {
    const added = new Date(t.createdAt).toLocaleDateString();
    sub.textContent = `added ${added} · ${t.lastUsedAt ? `last synced ${fmtAgo(t.lastUsedAt)}` : "never synced"}`;
  }
  meta.append(nameLine, sub);
  row.append(meta);

  // Revoked devices are frozen — kept only so their history stays named.
  if (revoked) return row;

  const actions = document.createElement("div");
  actions.className = "device-actions";

  const renameBtn = document.createElement("button");
  renameBtn.className = "btn-ghost";
  renameBtn.type = "button";
  renameBtn.textContent = "Rename";
  renameBtn.onclick = () => startRename(row, t);

  const revokeBtn = document.createElement("button");
  revokeBtn.className = "btn-danger";
  revokeBtn.type = "button";
  revokeBtn.textContent = "Revoke";
  revokeBtn.onclick = async () => {
    if (!confirm(`Revoke "${t.name || "device"}"? It will stop syncing; its history is kept.`)) return;
    try { await fetch(`api/tokens?id=${t.id}`, { method: "DELETE" }); } catch { /* re-render reflects state */ }
    renderDevices();
  };

  actions.append(renameBtn, revokeBtn);
  row.append(actions);
  return row;
}

// startRename swaps a device row into an inline edit form. Save PATCHes the new
// name; both Save and Cancel re-render the list from the server.
function startRename(row, t) {
  row.textContent = "";
  row.classList.add("device-row--editing");

  const input = document.createElement("input");
  input.type = "text";
  input.maxLength = 64;
  input.className = "device-rename-input";
  input.value = t.name || "";

  const err = document.createElement("span");
  err.className = "device-rename-err";
  err.hidden = true;

  const save = document.createElement("button");
  save.className = "btn-primary";
  save.type = "button";
  save.textContent = "Save";

  const cancel = document.createElement("button");
  cancel.className = "btn-ghost";
  cancel.type = "button";
  cancel.textContent = "Cancel";
  cancel.onclick = () => renderDevices();

  const doSave = async () => {
    const name = input.value.trim();
    if (!name) { err.textContent = "Name required"; err.hidden = false; return; }
    save.disabled = true;
    let res;
    try {
      res = await fetch(`api/tokens?id=${t.id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name }),
      });
    } catch {
      err.textContent = "Could not reach the server"; err.hidden = false; save.disabled = false; return;
    }
    if (!res.ok) { err.textContent = `Rename failed (${res.status})`; err.hidden = false; save.disabled = false; return; }
    renderDevices();
  };
  save.onclick = doSave;
  input.onkeydown = (e) => {
    if (e.key === "Enter") { e.preventDefault(); doSave(); }
    if (e.key === "Escape") renderDevices();
  };

  const wrap = document.createElement("div");
  wrap.className = "device-rename";
  wrap.append(input, save, cancel, err);
  row.append(wrap);
  input.focus();
  input.select();
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

// ── Config tab ──────────────────────────────────────────
// The per-device configuration snapshot the agent syncs (see /api/config). The
// blob is opaque to the server; the browser decrypts it (if encrypted) and
// renders it. The JSON shape parsed below is produced by the agent's config
// subset builder (internal/syncer) — keep the two in step.

const CONFIG_LOCKED = Symbol("locked");

// SERVICE_LABELS maps the services object's keys to human labels, in display order.
const SERVICE_LABELS = [
  ["hosts", "Website blocking"],
  ["forbiddenPrograms", "Forbidden programs"],
  ["contentMonitoring", "Content monitoring"],
  ["violationTracking", "Violation tracking"],
  ["usageMonitor", "Usage tracking"],
  ["sudoers", "Sudo restrictions"],
  ["panic", "Panic mode"],
  ["sync", "Sync to glockpeek"],
];

// escHtml escapes a string for safe interpolation into innerHTML. Config content
// (keywords, program names, domains) is user data, so every interpolation of it
// goes through here.
function escHtml(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

const DAY_ORDER = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

// fmtDays compacts a day list into the shortest readable form: "daily",
// "weekdays", "weekends", a contiguous range ("Mon–Fri"), or a comma list.
// Output draws only from fixed labels, so it needs no escaping.
function fmtDays(days) {
  if (!Array.isArray(days) || days.length === 0) return "";
  const set = new Set(days);
  if (set.size === 7) return "daily";
  if (set.size === 5 && ["Mon", "Tue", "Wed", "Thu", "Fri"].every((d) => set.has(d))) return "weekdays";
  if (set.size === 2 && ["Sat", "Sun"].every((d) => set.has(d))) return "weekends";
  const idx = days.map((d) => DAY_ORDER.indexOf(d)).filter((i) => i >= 0).sort((a, b) => a - b);
  const contiguous = idx.length > 2 && idx.every((v, i) => i === 0 || v === idx[i - 1] + 1);
  if (contiguous) return `${DAY_ORDER[idx[0]]}–${DAY_ORDER[idx[idx.length - 1]]}`;
  return idx.map((i) => DAY_ORDER[i]).join(", ");
}

// fmtWindows renders a TimeWindow array as compact "HH:MM–HH:MM <days>" lines,
// one window per line. An empty list means the field's "always" default.
function fmtWindows(wins) {
  if (!Array.isArray(wins) || wins.length === 0) return "always";
  return wins
    .map((w) => {
      const d = fmtDays(w.days);
      return `${escHtml(w.start)}–${escHtml(w.end)}${d ? ` <span class="cfg-days">${d}</span>` : ""}`;
    })
    .join("<br>");
}

// decryptConfigBlob turns one device's stored blob into its parsed config object.
// Plaintext rows parse directly; encrypted rows are opened under the master key
// (unlocking the vault first if needed). Returns CONFIG_LOCKED when it can't be
// decrypted or parsed.
async function decryptConfigBlob(c) {
  try {
    if (!c.encrypted) return JSON.parse(c.config);
    if (!masterKey && !(await unlockVault())) return CONFIG_LOCKED;
    const aad = new TextEncoder().encode(`${c.ts}|deviceconfig|config`);
    const plain = new TextDecoder().decode(await decryptField(masterKey, c.config, aad));
    return JSON.parse(plain);
  } catch {
    return CONFIG_LOCKED;
  }
}

// renderConfig fetches every device's snapshot, decrypts each in the browser, and
// renders one card per device. Empty state until an agent has synced its config.
async function renderConfig() {
  const listEl = document.getElementById("config-list");
  const emptyEl = document.getElementById("config-empty");
  const errEl = document.getElementById("config-error");
  const subEl = document.getElementById("config-sub");
  errEl.hidden = true;

  let configs = [];
  try {
    const r = await fetch("api/config", { credentials: "same-origin" });
    if (!r.ok) throw new Error(`server returned ${r.status}`);
    configs = (await r.json()).configs || [];
  } catch (e) {
    listEl.innerHTML = "";
    emptyEl.hidden = true;
    if (subEl) subEl.textContent = "";
    errEl.textContent = `Could not load configuration: ${e.message}`;
    errEl.hidden = false;
    return;
  }

  if (configs.length === 0) {
    listEl.innerHTML = "";
    if (subEl) subEl.textContent = "";
    emptyEl.hidden = false;
    return;
  }
  emptyEl.hidden = true;
  if (subEl) subEl.textContent = `${configs.length} device${configs.length === 1 ? "" : "s"}.`;

  const cards = [];
  for (const c of configs) {
    const parsed = await decryptConfigBlob(c);
    cards.push(renderConfigCard(c, parsed === CONFIG_LOCKED ? null : parsed, parsed === CONFIG_LOCKED));
  }
  listEl.innerHTML = cards.join("");
}

// renderConfigCard builds one device's card. cfg is the parsed subset, or null
// when the snapshot is locked/unreadable.
function renderConfigCard(c, cfg, locked) {
  const name = c.name ? escHtml(c.name) : "This machine";
  const when = c.ts ? escHtml(fmtAgo(c.ts)) : "";
  const meta = c.encrypted
    ? `<span class="cfg-lock">🔒</span> Encrypted · synced ${when}`
    : `Synced ${when}`;
  const head = `<div class="cfg-head"><span class="cfg-name">${name}</span>` +
    `<span class="cfg-when">${meta}</span></div>`;

  if (locked) {
    return `<div class="config-card">${head}<div class="cfg-locked">Encrypted under a key this browser can't open.</div></div>`;
  }
  if (!cfg || typeof cfg !== "object") {
    return `<div class="config-card">${head}<div class="cfg-locked">Unreadable snapshot.</div></div>`;
  }

  // The narrow side rail (status-shaped: services + guardrails) and the wide
  // main column (the list-heavy sections that need room to breathe).
  const side = [];
  const main = [];

  // Services as a state checklist — a green dot for on, muted for off.
  const svc = cfg.services || {};
  const svcItems = SERVICE_LABELS.map(([k, label]) => {
    const on = !!svc[k];
    const tag = k === "sync" && on && svc.syncEncrypted ? ` <span class="svc-tag">end-to-end</span>` : "";
    return `<div class="svc-item ${on ? "on" : "off"}"><span class="svc-dot"></span><span>${escHtml(label)}${tag}</span></div>`;
  }).join("");
  side.push(cfgSection("Services", `<div class="svc-list">${svcItems}</div>`));

  // Guardrails — compact label/value pairs.
  const g = cfg.guardrails || {};
  const gRows = [];
  if ((g.sudoAllow || []).length) gRows.push(cfgRow("Sudo allowed", fmtWindows(g.sudoAllow)));
  if (g.unblockMaxPerDay != null) gRows.push(cfgRow("Unblocks/day", g.unblockMaxPerDay > 0 ? `${g.unblockMaxPerDay}` : "no cap"));
  if (g.violationMax != null) gRows.push(cfgRow("Violation limit", `${g.violationMax} in ${g.violationWindowMin ?? "?"} min`));
  if (g.panicDurationMin != null) gRows.push(cfgRow("Panic duration", `${g.panicDurationMin} min`));
  if (g.checkIntervalSec != null) gRows.push(cfgRow("Check interval", `${g.checkIntervalSec} s`));
  if (gRows.length) side.push(cfgSection("Guardrails", cfgRows(gRows)));

  // Accountability email (glockpeek sends the digest + alerts). Shown once set up.
  const ac = cfg.accountability || {};
  if (ac.enabled || (ac.buddies || []).length) {
    const acRows = [cfgRow("Status", ac.enabled ? "on" : "off")];
    if ((ac.buddies || []).length) acRows.push(cfgRow("Buddies", ac.buddies.map(escHtml).join(", ")));
    acRows.push(cfgRow("Digest", ac.digestEveryHours > 0 ? `every ${ac.digestEveryHours}h${ac.digestWhenQuiet ? " (incl. quiet)" : ""}` : "off"));
    const alerts = [];
    if (ac.wentDark) alerts.push("went-dark");
    if (ac.highViolations > 0) alerts.push(`high (${ac.highViolations})`);
    acRows.push(cfgRow("Alerts", alerts.length ? alerts.join(", ") : "none"));
    side.push(cfgSection("Accountability", cfgRows(acRows)));
  }

  // Keywords — the comma lists want the wide column.
  const kw = cfg.keywords || {};
  const kwRows = [];
  if ((kw.url || []).length) kwRows.push(cfgRow(`URL · ${kw.url.length}`, kw.url.map(escHtml).join(", ")));
  if ((kw.content || []).length) kwRows.push(cfgRow(`Content · ${kw.content.length}`, kw.content.map(escHtml).join(", ")));
  if (kw.whitelistCount) kwRows.push(cfgRow("Whitelisted", `${kw.whitelistCount} domains`));
  if (kwRows.length) main.push(cfgSection("Keywords", cfgRows(kwRows)));

  // Forbidden programs — a growing list. Always-killed names as one wrapped
  // line; each windowed program as a list item (name over its schedule).
  const pr = cfg.programs || {};
  const prParts = [];
  if ((pr.alwaysKilled || []).length) {
    prParts.push(`<div class="cfg-note"><span class="cfg-note-k">Always killed · ${pr.alwaysKilled.length}</span>${pr.alwaysKilled.map(escHtml).join(", ")}</div>`);
  }
  const prItems = (pr.windowed || []).map((p) => {
    let detail;
    if ((p.kill || []).length && (p.allow || []).length) detail = `killed ${fmtWindows(p.kill)}<br>else allowed ${fmtWindows(p.allow)}`;
    else if ((p.allow || []).length) detail = `allowed ${fmtWindows(p.allow)}, killed otherwise`;
    else detail = `killed ${fmtWindows(p.kill)}`;
    return cfgItem(escHtml(p.name) + (p.extendible ? ` <span class="cfg-ext" title="extendible">⏳</span>` : ""), detail);
  });
  if (prParts.length || prItems.length) {
    main.push(cfgSection("Forbidden programs", prParts.join("") + cfgList(prItems)));
  }

  // Domains — a count summary, then a growing list of the time-based ones.
  const dm = cfg.domains || {};
  const dParts = [];
  if (dm.blocked != null) {
    const bits = [`${Number(dm.blocked).toLocaleString()} blocked`];
    if (dm.alwaysBlocked != null) bits.push(`${Number(dm.alwaysBlocked).toLocaleString()} always`);
    if ((dm.timeBased || []).length) bits.push(`${dm.timeBased.length} scheduled`);
    dParts.push(`<div class="cfg-summary">${bits.join(" · ")}</div>`);
  }
  const dItems = (dm.timeBased || []).map((d) => cfgItem(escHtml(d.name), `blocked ${fmtWindows(d.block)}`));
  if (dParts.length || dItems.length) {
    main.push(cfgSection("Domains", dParts.join("") + cfgList(dItems)));
  }

  return `<div class="config-card">${head}<div class="cfg-body">` +
    `<div class="cfg-col cfg-col--side">${side.join("")}</div>` +
    `<div class="cfg-col cfg-col--main">${main.join("")}</div>` +
    `</div></div>`;
}

// cfgSection wraps a titled block.
function cfgSection(title, innerHtml) {
  return `<div class="cfg-section"><h4>${escHtml(title)}</h4>${innerHtml}</div>`;
}

// cfgRows lays a set of label/value pairs in a two-track grid so every value
// aligns in one column (the label track sizes to the widest label).
function cfgRows(rows) {
  return `<div class="cfg-rows">${rows.join("")}</div>`;
}

// cfgRow is one label/value pair: two grid cells (label, then value). label and
// valueHtml are expected to be already-escaped or literal-safe.
function cfgRow(label, valueHtml) {
  return `<span class="cfg-k">${label}</span><span class="cfg-v">${valueHtml}</span>`;
}

// cfgList wraps list items (from cfgItem) into a growing vertical list, or ""
// when empty.
function cfgList(items) {
  return items.length ? `<ul class="cfg-items">${items.join("")}</ul>` : "";
}

// cfgItem is one entry in a growing list: a name over its detail line(s). name
// and detailHtml are expected to be already-escaped or literal-safe.
function cfgItem(name, detailHtml) {
  return `<li class="cfg-item"><span class="cfg-item-name">${name}</span>` +
    `<span class="cfg-item-detail">${detailHtml}</span></li>`;
}

// ── Insights ────────────────────────────────────────────
// Suggestions derived, in the browser, from the account's own decrypted data
// (violations / usage / unblocks / lifecycle) cross-checked against the synced
// config snapshots. Each insight maps to a concrete config change; we never
// surface a vague nudge, and never suggest something the config already does.

let workStart = 9;      // work-hours window start (hour, local) — adjustable in the UI
let workEnd = 17;       // work-hours window end (exclusive)
try {
  const wh = localStorage.getItem("gp_work_hours");
  if (wh) { const [a, b] = wh.split("-").map(Number); if (a >= 0 && a < 24 && b > 0 && b <= 24) { workStart = a; workEnd = b; } }
} catch { /* no localStorage -> defaults */ }

let bedStart = 23;      // bedtime window start (hour, local) — wraps past midnight
let bedEnd = 7;         // bedtime window end (exclusive)
try {
  const bt = localStorage.getItem("gp_bedtime");
  if (bt) { const [a, b] = bt.split("-").map(Number); if (a >= 0 && a <= 24 && b >= 0 && b <= 24) { bedStart = a; bedEnd = b; } }
} catch { /* no localStorage -> defaults */ }
// inBedtime tests an hour against the bedtime window, handling the usual
// overnight wrap (end ≤ start, e.g. 23:00→07:00).
const inBedtime = (hr) =>
  bedStart === bedEnd ? false : bedStart < bedEnd ? hr >= bedStart && hr < bedEnd : hr >= bedStart || hr < bedEnd;

const INSIGHT = {
  minViolations: 5,     // per-domain hits before we call it a pattern
  minWorkSamples: 25,   // ~minutes of a work-hours distraction before it's worth blocking
  minDistractionShare: 0.6, // an app must be mostly-distraction to suggest killing it
  workIdleMs: 5 * 60_000,
  minUninstalls: 3,
  minUnblocks: 3,
  minBedtimeMs: 30 * 60_000, // ≥30 min of active use before an app is a "time-sink"
  minBedtimeShare: 0.05,     // and it must be ≥5% of tracked active time
};
const LEGIT_UNINSTALL = new Set(["maintenance", "testing", "update", "upgrade", "reinstall"]);
const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

const pad2 = (n) => String(n).padStart(2, "0");
const hourBand = (h) => `${pad2(h)}:00–${pad2((h + 2) % 24)}:00`;

// peakBand finds the busiest 3-hour stretch in an hour histogram (wrapping past
// midnight) and returns its bounds, label, and what share of all hits fall in it —
// so "concentrated near X" becomes a real window with a percentage.
function peakBand(hours) {
  const total = hours.reduce((a, b) => a + b, 0) || 1;
  let bestStart = 0, bestSum = -1;
  for (let i = 0; i < 24; i++) {
    const s = hours[i] + hours[(i + 1) % 24] + hours[(i + 2) % 24];
    if (s > bestSum) { bestSum = s; bestStart = i; }
  }
  const end = (bestStart + 3) % 24;
  return {
    start: `${pad2(bestStart)}:00`, end: `${pad2(end)}:00`,
    label: `${pad2(bestStart)}:00–${pad2(end)}:00`,
    share: Math.round((bestSum / total) * 100), count: bestSum,
  };
}

// fetchDeviceConfigs returns the parsed config snapshot for each device (locked
// ones skipped), so insights can cross-check what's already enforced.
async function fetchDeviceConfigs() {
  const out = [];
  try {
    const r = await fetch("api/config", { credentials: "same-origin" });
    if (!r.ok) return out;
    for (const c of (await r.json()).configs || []) {
      const parsed = await decryptConfigBlob(c);
      if (parsed !== CONFIG_LOCKED && parsed && typeof parsed === "object") out.push(parsed);
    }
  } catch { /* no configs -> insights just can't cross-check */ }
  return out;
}

// configIndex unions what every device already enforces.
function configIndex(configs) {
  const tbDomains = new Set(), forbidden = new Set();
  const sudoAllow = [];
  // Violation-tracking posture, unioned across devices: enabled if any device has
  // it on; the loosest threshold/window seen (so "lower it" advice targets the
  // slackest device).
  let vtEnabled = false, violationMax = 0, violationWindowMin = 0;
  for (const cfg of configs) {
    for (const d of (cfg.domains && cfg.domains.timeBased) || []) if (d.name) tbDomains.add(d.name.toLowerCase());
    for (const n of (cfg.programs && cfg.programs.alwaysKilled) || []) forbidden.add(String(n).toLowerCase());
    for (const p of (cfg.programs && cfg.programs.windowed) || []) if (p.name) forbidden.add(String(p.name).toLowerCase());
    for (const w of (cfg.guardrails && cfg.guardrails.sudoAllow) || []) sudoAllow.push(w);
    const svc = cfg.services || {}, g = cfg.guardrails || {};
    if (svc.violationTracking) vtEnabled = true;
    if (g.violationMax) violationMax = Math.max(violationMax, g.violationMax);
    if (g.violationWindowMin) violationWindowMin = Math.max(violationWindowMin, g.violationWindowMin);
  }
  return { tbDomains, forbidden, sudoAllow, vtEnabled, violationMax, violationWindowMin };
}

const argmax = (arr) => arr.reduce((best, v, i, a) => (v > a[best] ? i : best), 0);

// 1. Repeated keyword hits: search/URL/content terms the monitor keeps flagging,
// clustered in time. The keyword is the signal, not the host — a banned search on
// google shouldn't suggest blocking google. The monitor only *logs* the hit, so
// the fix is to tighten the response (violation_tracking → glocklock), not to
// block the domain. Grouped by keyword so the same term surfaces once.
function insightKeywordHits(violations, idx) {
  const byKw = new Map(); // keyword -> { kw, hours[24], days:Set, count, domains:Map }
  for (const v of violations || []) {
    const kw = (v.keyword || "").trim();
    if (!kw || kw === "🔒") continue; // blank or still-encrypted
    const key = kw.toLowerCase();
    const d = new Date(v.ts);
    const rec = byKw.get(key) || { kw, hours: new Array(24).fill(0), days: new Set(), count: 0, domains: new Map() };
    rec.hours[d.getHours()]++; rec.days.add(d.getDay()); rec.count++;
    const dom = (v.domain || "").toLowerCase();
    if (dom && dom !== "🔒") rec.domains.set(dom, (rec.domains.get(dom) || 0) + 1);
    byKw.set(key, rec);
  }
  const out = [];
  for (const rec of byKw.values()) {
    if (rec.count < INSIGHT.minViolations) continue;
    const band = peakBand(rec.hours);
    // Where it mostly showed up — context only, never the thing we act on.
    const topDom = [...rec.domains.entries()].sort((a, b) => b[1] - a[1])[0];
    const where = topDom ? `, mostly on ${escHtml(topDom[0])}` : "";
    // glocklock needs the desktop user's session; templated like the other snippets.
    const lock = `sudo -u <your-user> DISPLAY=:0 /usr/local/bin/glocklock`;
    let action, config;
    if (!idx.vtEnabled) {
      action = `The monitor logs it but nothing stops it. Make repeated hits lock the screen — turn on violation tracking:`;
      config = `violation_tracking:\n  enabled: true\n  max_violations: 3\n  time_window_minutes: 60\n  command: "${lock}"`;
    } else {
      const win = idx.violationWindowMin || 60;
      const sugg = idx.violationMax ? Math.max(2, Math.ceil(idx.violationMax / 2)) : 3;
      action = `Violation tracking is on (${idx.violationMax || "?"} in ${win} min) but this still recurs. Trip the lock sooner:`;
      config = `violation_tracking:\n  enabled: true\n  max_violations: ${sugg}\n  time_window_minutes: ${win}\n  command: "${lock}"`;
    }
    out.push({
      kind: "vuln",
      title: `“${escHtml(rec.kw)}” keeps getting searched`,
      evidence: `The content monitor flagged “${escHtml(rec.kw)}” ${rec.count}× — ${band.share}% between ${band.label}${where}.`,
      action,
      config,
      weight: rec.count,
    });
  }
  return out.sort((a, b) => b.weight - a.weight).slice(0, 6);
}

// 3. Apps that are mostly distractions during work hours (per the tag registry)
// and aren't forbidden programs yet. A sample counts as a distraction when the
// rules tag it with a tag flagged "distraction" (system Glocker:* tags with the
// account's overrides applied, plus the account's own tags). An app is only
// worth killing when nearly all of its work-hours time is distraction — so
// media/game apps surface here while a browser (mixed use) is left to the
// domain-block insight above.
function insightWorkDistraction(usage, idx) {
  const compiled = compileRules(state.rules);
  const byApp = new Map(); // app -> { total, distract, tags:Set, title }
  for (const s of usage || []) {
    const a = s.active;
    if (!a || !a.class) continue;
    if (s.idleMs && s.idleMs > INSIGHT.workIdleMs) continue;
    const d = new Date(s.ts), wd = d.getDay(), hr = d.getHours();
    if (wd === 0 || wd === 6 || hr < workStart || hr >= workEnd) continue;
    const m = matchedTags({ active: a }, compiled);
    if (m.hidden) continue; // hidden windows are dropped from usage, so not distractions
    const rec = byApp.get(a.class) || { total: 0, distract: 0, tags: new Set(), title: "" };
    rec.total++;
    let isDistraction = false;
    for (const t of m.tags.values()) {
      if (t.distraction) { isDistraction = true; rec.tags.add(tagLabel(t)); }
    }
    if (isDistraction) { rec.distract++; if (!rec.title && a.title) rec.title = a.title; }
    byApp.set(a.class, rec);
  }
  const out = [];
  for (const [app, rec] of byApp) {
    if (rec.distract < INSIGHT.minWorkSamples) continue;
    if (rec.distract / rec.total < INSIGHT.minDistractionShare) continue; // mostly-distraction apps only
    if (idx.forbidden.has(app.toLowerCase())) continue;
    const tagList = [...rec.tags].join(", ");
    const eg = rec.title ? `, e.g. “${escHtml(rec.title)}”` : "";
    out.push({
      kind: "work",
      title: `${escHtml(app)} runs during work hours`,
      evidence: `About ${rec.distract} min of ${escHtml(tagList) || "distraction"} during work hours (${pad2(workStart)}:00–${pad2(workEnd)}:00, weekdays)${eg}. It isn't a forbidden program.`,
      action: `Kill ${escHtml(app)} during work hours:`,
      config: `programs:\n  enabled: true\n  list:\n    - name: ${app}\n      kill_windows:\n        - { start: "${pad2(workStart)}:00", end: "${pad2(workEnd)}:00", days: ["Mon", "Tue", "Wed", "Thu", "Fri"] }`,
      weight: rec.distract,
    });
  }
  return out.sort((a, b) => b.weight - a.weight);
}

// 4. Bedtime lockout: your heaviest-used apps (by active hours, any category)
// shouldn't be runnable past bedtime — if the app can't run, the domains inside
// it don't matter. Ranks apps by total active time in the window and suggests a
// kill window over the bedtime hours (every day). Preventive: an app qualifies on
// overall use, not just use already happening at night (that's shown as evidence).
function insightBedtimeApps(usage, idx) {
  // Attribute real active time per app (eachSampleDuration handles idle/gaps), so
  // the ranking and the hours shown match the Usage view — not a raw sample count.
  const byApp = new Map(); // class -> { ms, nightMs }
  let grand = 0;
  eachSampleDuration(usage || [], (e, dt, away) => {
    if (away) return; // idle / no focus isn't "use"
    const a = e.active;
    if (!a || !a.class) return;
    const rec = byApp.get(a.class) || { ms: 0, nightMs: 0 };
    rec.ms += dt; grand += dt;
    if (inBedtime(new Date(e.ts).getHours())) rec.nightMs += dt;
    byApp.set(a.class, rec);
  });
  if (!grand) return [];
  const days7 = `["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]`;
  const win = `{ start: "${pad2(bedStart)}:00", end: "${pad2(bedEnd)}:00", days: ${days7} }`;
  const out = [];
  for (const [app, rec] of byApp) {
    if (idx.forbidden.has(app.toLowerCase())) continue; // already killed
    const share = rec.ms / grand;
    if (rec.ms < INSIGHT.minBedtimeMs || share < INSIGHT.minBedtimeShare) continue;
    const night = rec.nightMs ? ` — ${fmtDur(rec.nightMs)} of it already after bedtime` : "";
    out.push({
      kind: "bedtime",
      title: `${escHtml(app)} is one of your biggest time-sinks`,
      evidence: `About ${fmtDur(rec.ms)} of active use in this window (${Math.round(share * 100)}% of your tracked time)${night}. Lock it out overnight so it isn't an option.`,
      action: `Kill ${escHtml(app)} during bedtime (${pad2(bedStart)}:00–${pad2(bedEnd)}:00):`,
      config: `programs:\n  enabled: true\n  list:\n    - name: ${app}\n      kill_windows:\n        - ${win}`,
      weight: rec.ms,
    });
  }
  return out.sort((a, b) => b.weight - a.weight).slice(0, 6);
}

// 5. Uninstalls clustered at a time of day (excluding legit reasons) → the sudo
// window is open then.
function insightUninstallWindow(lifecycle, idx) {
  const uns = (lifecycle || []).filter((e) => e.type === "uninstall" && !LEGIT_UNINSTALL.has((e.reason || "").toLowerCase()));
  if (uns.length < INSIGHT.minUninstalls) return [];
  const hours = new Array(24).fill(0);
  const reasons = new Map();
  for (const e of uns) {
    hours[new Date(e.ts).getHours()]++;
    const r = e.reason || "unspecified";
    reasons.set(r, (reasons.get(r) || 0) + 1);
  }
  const band = peakBand(hours);
  const topReasons = [...reasons.entries()].sort((a, b) => b[1] - a[1]).slice(0, 2).map(([r]) => escHtml(r)).join(", ");
  return [{
    kind: "uninstall",
    title: `Uninstalls cluster around ${band.label}`,
    evidence: `${uns.length} uninstalls here, ${band.share}% around ${band.label} (reasons: ${topReasons}). sudo is available then, so uninstall is possible.`,
    action: `Close the sudo window over ${band.label}:`,
    config: `sudoers:\n  enabled: true\n  allow_windows:\n    # sudo must not be open across ${band.label}\n    - { start: "09:00", end: "${band.start}", days: ["Mon", "Tue", "Wed", "Thu", "Fri"] }`,
  }];
}

// (bonus) Repeated unblocks of the same domain — a recurring weak spot.
function insightUnblockWeakSpots(unblocks) {
  const byDom = new Map();
  for (const u of unblocks || []) {
    const dom = (u.domain || "").toLowerCase();
    // program:<name> entries are `-extend` program grants, not domain unblocks —
    // handled by insightRepeatedExtensions.
    if (!dom || dom === "🔒" || dom.startsWith("program:")) continue;
    const rec = byDom.get(dom) || { count: 0, reasons: new Set() };
    rec.count++; if (u.reason && u.reason !== "🔒") rec.reasons.add(u.reason);
    byDom.set(dom, rec);
  }
  const out = [];
  for (const [dom, rec] of byDom) {
    if (rec.count < INSIGHT.minUnblocks) continue;
    const rs = [...rec.reasons].slice(0, 3).map(escHtml).join(", ");
    out.push({
      kind: "unblock",
      title: `${escHtml(dom)} is unblocked over and over`,
      evidence: `Temporarily unblocked ${rec.count}× in this window${rs ? ` (reasons: ${rs})` : ""}. A recurring weak spot.`,
      action: `Make it un-unblockable, or lower the daily cap:`,
      config: `# ${dom}: drop "unblockable: true" from its entry, or tighten\ndomains:\n  unblocking:\n    max_per_day: 1`,
      weight: rec.count,
    });
  }
  return out.sort((a, b) => b.weight - a.weight).slice(0, 4);
}

// (bonus) Forbidden programs granted one-hour `-extend` runs over and over. These
// arrive in the unblocks stream as domain "program:<name>"; the lever is the
// program's `extendible` flag, not domain unblocking.
function insightRepeatedExtensions(unblocks) {
  const byProg = new Map();
  for (const u of unblocks || []) {
    const dom = (u.domain || "").toLowerCase();
    if (!dom.startsWith("program:")) continue;
    const prog = dom.slice("program:".length);
    if (!prog || prog === "🔒") continue;
    const rec = byProg.get(prog) || { count: 0, reasons: new Set() };
    rec.count++; if (u.reason && u.reason !== "🔒") rec.reasons.add(u.reason);
    byProg.set(prog, rec);
  }
  const out = [];
  for (const [prog, rec] of byProg) {
    if (rec.count < INSIGHT.minUnblocks) continue;
    const rs = [...rec.reasons].slice(0, 3).map(escHtml).join(", ");
    out.push({
      kind: "extend",
      title: `${escHtml(prog)} keeps getting extended`,
      evidence: `Granted a one-hour extension ${rec.count}× in this window${rs ? ` (reasons: ${rs})` : ""}. A recurring bypass.`,
      action: `Stop the grant — make ${escHtml(prog)} non-extendible:`,
      config: `programs:\n  list:\n    - name: ${prog}\n      extendible: false`,
      weight: rec.count,
    });
  }
  return out.sort((a, b) => b.weight - a.weight).slice(0, 4);
}

const GROUP_CAP = 25; // cards shown per category before "+N more"

// Master–detail insight state: the computed groups (one compute per window) and
// the selected category. Category clicks re-render only the detail pane from
// this cache — never re-run renderInsights, so the timeline never redraws.
let insightGroups = [];
let insightSel = null;

async function renderInsights() {
  const emptyEl = document.getElementById("insights-empty");
  const errEl = document.getElementById("insights-error");
  const subEl = document.getElementById("insights-sub");
  errEl.hidden = true;
  renderInsightsControls();

  const data = state.data;
  if (!data) {
    errEl.textContent = "No data loaded yet.";
    errEl.hidden = false;
    return;
  }
  renderTimeline("insights-timeline"); // the shared window strip at the top

  // Everything is scoped to the brushed window.
  const b = windowBounds();
  const inWin = (ts) => within(ts, b);
  const violations = (data.violations || []).filter((v) => inWin(v.ts) && inDevice(v));
  const usage = (data.usage || []).filter((s) => inWin(s.ts) && inDevice(s));
  const lifecycle = (data.lifecycle || []).filter((e) => inWin(e.ts) && inDevice(e));
  const unblocks = (data.unblocks || []).filter((u) => inWin(u.ts) && inDevice(u));

  // Device config snapshots give the time-based/program view and the current
  // violation-tracking posture the keyword insight tunes against.
  const configs = await fetchDeviceConfigs();
  const idx = configIndex(configs);

  // Without a config snapshot the insights are flying blind — they can't tell
  // what's already enforced or whether violation tracking is on, so a suggestion
  // may repeat what's set or wrongly say "turn on tracking". Flag it.
  const noConfigEl = document.getElementById("insights-noconfig");
  if (noConfigEl) {
    const missing = configs.length === 0;
    noConfigEl.hidden = !missing;
    if (missing) {
      noConfigEl.textContent =
        "No device config has synced, so these suggestions can't check what you already enforce — enable sync on the agent (sync.enabled) for accurate insights.";
    }
  }

  const groups = [
    { kind: "vuln", label: "Repeated searches", items: insightKeywordHits(violations, idx) },
    { kind: "work", label: "Work-hours apps", items: insightWorkDistraction(usage, idx) },
    { kind: "bedtime", label: "Past bedtime", items: insightBedtimeApps(usage, idx) },
    { kind: "uninstall", label: "Uninstalls", items: insightUninstallWindow(lifecycle, idx) },
    { kind: "unblock", label: "Repeated unblocks", items: insightUnblockWeakSpots(unblocks) },
    { kind: "extend", label: "Repeated extensions", items: insightRepeatedExtensions(unblocks) },
  ];

  const total = groups.reduce((n, g) => n + g.items.length, 0);
  const clear = groups.length - groups.filter((g) => g.items.length).length;
  if (subEl) {
    subEl.textContent = total
      ? `${total} suggestion${total === 1 ? "" : "s"}` + (clear ? ` · ${clear} clear` : "")
      : "";
  }

  insightGroups = groups;
  const nav = document.getElementById("insights-nav");
  const detail = document.getElementById("insights-detail");
  if (total === 0) {
    if (nav) nav.innerHTML = "";
    if (detail) detail.innerHTML = "";
    emptyEl.hidden = false;
    return;
  }
  emptyEl.hidden = true;

  // Keep the selected category if it still has items; else fall to the first
  // non-empty one.
  if (!groups.some((g) => g.kind === insightSel && g.items.length)) {
    insightSel = (groups.find((g) => g.items.length) || {}).kind;
  }
  renderInsightNav();
  renderInsightDetail();
  wireInsightNav();
}

// renderInsightNav draws the left rail: non-empty categories (bright, clickable)
// over the "clear" ones (muted, non-interactive), each a uniform row of dot +
// label + count.
function renderInsightNav() {
  const nav = document.getElementById("insights-nav");
  if (!nav) return;
  const row = (g) => {
    const on = g.items.length > 0;
    return `<button class="insight-navrow insight--${g.kind}${g.kind === insightSel ? " active" : ""}${on ? "" : " clear"}"` +
      (on ? ` data-insel="${g.kind}"` : " disabled") + `>` +
      `<span class="insight-navdot"></span>` +
      `<span class="insight-navlabel">${escHtml(g.label)}</span>` +
      `<span class="insight-navcount">${g.items.length}</span></button>`;
  };
  const active = insightGroups.filter((g) => g.items.length);
  const clear = insightGroups.filter((g) => !g.items.length);
  nav.innerHTML = active.map(row).join("") +
    (clear.length ? `<div class="insight-navsep">Clear</div>` + clear.map(row).join("") : "");
}

// renderInsightDetail draws only the selected category's cards.
function renderInsightDetail() {
  const el = document.getElementById("insights-detail");
  if (!el) return;
  const g = insightGroups.find((x) => x.kind === insightSel);
  if (!g) { el.innerHTML = ""; return; }
  const shown = g.items.slice(0, GROUP_CAP);
  const more = g.items.length - shown.length;
  el.innerHTML =
    `<div class="insight-cards insight--${g.kind}">${shown.map(renderInsightCard).join("")}` +
    (more > 0 ? `<p class="insight-more">+${more} more</p>` : "") +
    `</div>`;
}

// wireInsightNav (delegated, once): a category click swaps only the detail pane
// and the rail's active state — no recompute, no timeline redraw.
function wireInsightNav() {
  const nav = document.getElementById("insights-nav");
  if (!nav || nav.dataset.wired) return;
  nav.addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-insel]");
    if (!btn) return;
    insightSel = btn.dataset.insel;
    renderInsightNav();
    renderInsightDetail();
  });
  nav.dataset.wired = "1";
}

// renderInsightCard lays out one suggestion as problem → details → suggestion,
// then the exact config stanza to paste.
function renderInsightCard(s) {
  return `<div class="insight-card">` +
    `<p class="ic-problem">${s.title}</p>` +
    `<div class="ic-line"><span class="ic-tag">Details</span><span class="ic-text">${s.evidence}</span></div>` +
    `<div class="ic-line"><span class="ic-tag ic-tag--fix">Suggestion</span><span class="ic-text">${s.action}</span></div>` +
    `<pre class="insight-config">${escHtml(s.config)}</pre>` +
    `</div>`;
}

// renderInsightsControls builds the Work-hours and Bedtime sliders once and wires
// them to re-run the analysis. Dynamic only — persisted in localStorage, never the
// DB.
function renderInsightsControls() {
  const el = document.getElementById("insights-controls");
  if (!el || el.dataset.ready) return;
  el.innerHTML = "";
  mountRangeSlider(el, {
    label: "Work hours", id: "wh", start: workStart, end: workEnd, wrap: false,
    onChange: (a, e) => {
      workStart = a; workEnd = e;
      try { localStorage.setItem("gp_work_hours", `${a}-${e}`); } catch { /* ignore */ }
      renderInsights();
    },
  });
  mountRangeSlider(el, {
    label: "Bedtime", id: "bt", start: bedStart, end: bedEnd, wrap: true,
    onChange: (a, e) => {
      bedStart = a; bedEnd = e;
      try { localStorage.setItem("gp_bedtime", `${a}-${e}`); } catch { /* ignore */ }
      renderInsights();
    },
  });
  el.dataset.ready = "1";
}

// mountRangeSlider appends one labelled 0–24h dual-range slider. wrap=true allows
// an overnight window (end ≤ start), used for bedtime — its highlighted band then
// wraps around the two ends (rendered as two fill segments).
function mountRangeSlider(el, { label, id, start, end, wrap, onChange }) {
  const g = document.createElement("div");
  g.className = "ins-slider-group";
  g.innerHTML = `<span class="ins-ctl-label">${label}</span>` +
    `<div class="wh-slider">` +
    `<div class="wh-track"><div class="wh-fill"></div>${wrap ? `<div class="wh-fill wh-fill2"></div>` : ""}</div>` +
    `<input type="range" id="${id}-start" min="0" max="24" step="1" value="${start}">` +
    `<input type="range" id="${id}-end" min="0" max="24" step="1" value="${end}">` +
    `</div>` +
    `<span class="wh-readout" id="${id}-readout"></span>`;
  el.appendChild(g);
  const startEl = g.querySelector(`#${id}-start`);
  const endEl = g.querySelector(`#${id}-end`);
  const fill = g.querySelector(".wh-fill:not(.wh-fill2)");
  const fill2 = g.querySelector(".wh-fill2");
  const readout = g.querySelector(`#${id}-readout`);
  const pct = (h) => (h / 24) * 100;
  // Non-wrap sliders sort the thumbs (a window is the span between them); wrap
  // sliders keep them as-is so start can sit after end (overnight).
  const vals = () => {
    const a = +startEl.value, e = +endEl.value;
    return wrap ? [a, e] : a <= e ? [a, e] : [e, a];
  };
  const paint = () => {
    const [a, e] = vals();
    if (wrap && e <= a) { // overnight: a→24 and 0→e
      fill.style.left = `${pct(a)}%`; fill.style.right = "0%";
      fill2.style.left = "0%"; fill2.style.right = `${100 - pct(e)}%`; fill2.style.display = "";
    } else {
      fill.style.left = `${pct(a)}%`; fill.style.right = `${100 - pct(e)}%`;
      if (fill2) fill2.style.display = "none";
    }
    readout.textContent = `${pad2(a)}:00 – ${pad2(e)}:00`;
  };
  const commit = () => {
    const [a, e] = vals();
    if (a === e) { paint(); return; } // ignore a zero-width window
    onChange(a, e);
  };
  startEl.addEventListener("input", paint);
  endEl.addEventListener("input", paint);
  startEl.addEventListener("change", commit);
  endEl.addEventListener("change", commit);
  paint();
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
