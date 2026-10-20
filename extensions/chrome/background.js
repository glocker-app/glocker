// Glocker content monitor — Chrome (Manifest V3) service worker.
//
// Same job as the Firefox extension, adapted to MV3:
//   - MV3 has no blocking webRequest, so URL-keyword blocking is done with
//     declarativeNetRequest (DNR) redirect rules (which also persist across
//     service-worker restarts). A non-blocking webRequest listener reports the
//     matches to glocker (DNR can't run JS).
//   - Service workers have no EventSource, so /keywords-stream is read with a
//     streaming fetch. A chrome.alarms poll backs it up for when the worker has
//     been torn down (it reconnects the stream on every wake).
//   - State is mirrored to chrome.storage.local so it survives SW restarts.

importScripts("build-info.js");

// Debug logging — off by default. Flip to true to trace matching in the console.
const DEBUG = false;
function dbg() { if (DEBUG) console.log.apply(console, arguments); }

const GLOCKER = "http://127.0.0.1";
const REFRESH_ALARM = "glocker-refresh";
const RESOURCE_TYPES = ["main_frame", "sub_frame"];
const REPORT_CACHE_SIZE = 1000;

// Fallback defaults until the first successful fetch from the agent.
let urlKeywords = ["gambling", "casino", "porn", "xxx"];
let contentKeywords = ["trigger1", "trigger2"];
let whitelist = ["github.com", "stackoverflow.com", "docs.google.com"];
let urlKeywordRegexes = [];
let whitelistDomains = [];

const escapeRegex = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// The whitelist as bare domains. DNR's requestDomains matches a domain and its
// subdomains, so "*.atlassian.net" and "atlassian.net" collapse to the same entry.
function toDomains(list) {
  return list
    .map((w) => (w.startsWith("*.") ? w.slice(2) : w))
    .map((d) => d.replace(/^https?:\/\//, "").split("/")[0].trim().toLowerCase())
    .filter(Boolean);
}

// A URL keyword must start a word: follow the start of the URL, a separator
// (/ ? = . - _ …) or a %XX escape, so it can't fire inside a random token like a
// tracking id or base64 blob. It may run on into the word: "porn" matches
// "pornhub". Same rule as the Firefox extension.
const WORD_START = "(?:[^a-z0-9]|%[0-9a-f]{2})";

function compileRegexes() {
  urlKeywordRegexes = urlKeywords.map((keyword) => ({
    keyword,
    regex: new RegExp(`(?:^|${WORD_START})${escapeRegex(keyword.toLowerCase())}`, "i"),
  }));
  whitelistDomains = toDomains(whitelist);
}

// Same test as the DNR allow rule, so a URL is reported exactly when it's blocked.
function isWhitelisted(url) {
  let host;
  try { host = new URL(url).hostname; } catch (e) { return false; }
  return whitelistDomains.some((d) => host === d || host.endsWith("." + d));
}

function buildTag() {
  const v = chrome.runtime.getManifest().version;
  const c = (typeof GLOCKER_BUILD !== "undefined" && GLOCKER_BUILD.commit) ? GLOCKER_BUILD.commit : "dev";
  return `v${v} (${c})`;
}

// ── declarativeNetRequest: the actual blocking ───────────────────────────────
// Each keyword gets two rules: one for the keyword inside the host, one for it
// after the host. Each regex consumes the whole URL and captures the hostname
// (\1), so the substitution can hand the blocked page its "Access to X" domain
// like Firefox does. Either way the keyword must start a word (WORD_START);
// inside the host that means after a "." or "-". They're separate rules because
// one regex with both alternatives exceeds Chrome's 2KB compiled-regex limit for
// any keyword over ~3 characters.
function keywordRegexes(kw) {
  const k = escapeRegex(kw);
  const scheme = "^[a-z]+://";
  return [
    `${scheme}((?:[^/?#:]*[.-])?${k}[^/?#:]*).*`,
    `${scheme}([^/?#:]*)[:/?#](?:.*${WORD_START})?${k}.*`,
  ];
}

// Rebuild the dynamic rule set from the current keywords/whitelist. Priorities:
// 3 = never touch the local dashboard, 2 = whitelisted domains pass, 1 = redirect
// any page URL containing a blocked keyword to glocker's blocked page.
async function rebuildBlockingRules() {
  const rules = [];
  let id = 1;

  for (const host of ["127.0.0.1", "localhost"]) {
    rules.push({
      id: id++, priority: 3, action: { type: "allow" },
      condition: { urlFilter: host, resourceTypes: RESOURCE_TYPES },
    });
  }

  if (whitelistDomains.length) {
    rules.push({
      id: id++, priority: 2, action: { type: "allow" },
      condition: { requestDomains: whitelistDomains, resourceTypes: RESOURCE_TYPES },
    });
  }

  for (const kw of urlKeywords) {
    const reason = encodeURIComponent(`URL contains blocked keyword: "${kw}"`);
    for (const regexFilter of keywordRegexes(kw)) {
      // One rejected regex (RE2 memory cap) would fail the whole update; drop it
      // alone. The flags must match the rule's, or the check passes regexes the
      // update then rejects.
      const { isSupported, reason: why } = await chrome.declarativeNetRequest.isRegexSupported({
        regex: regexFilter, isCaseSensitive: false, requireCapturing: true,
      });
      if (!isSupported) {
        console.log(`glocker: skipping url keyword "${kw}": ${why}`);
        continue;
      }
      rules.push({
        id: id++, priority: 1,
        action: { type: "redirect", redirect: { regexSubstitution: `${GLOCKER}/blocked?reason=${reason}&domain=\\1` } },
        condition: { regexFilter, isUrlFilterCaseSensitive: false, resourceTypes: RESOURCE_TYPES },
      });
    }
  }

  try {
    const old = await chrome.declarativeNetRequest.getDynamicRules();
    await chrome.declarativeNetRequest.updateDynamicRules({
      removeRuleIds: old.map((r) => r.id),
      addRules: rules,
    });
  } catch (e) {
    console.log("glocker: failed to update blocking rules:", e);
  }
}

// ── Status badge ─────────────────────────────────────────────────────────────
function setStatus(connected) {
  const title = connected ? "Glocker: Active" : "Glocker: Disconnected";
  chrome.action.setTitle({ title: title + " — " + buildTag() });
  chrome.action.setBadgeText({ text: connected ? "●" : "○" });
  chrome.action.setBadgeBackgroundColor({ color: connected ? "#4CAF50" : "#F44336" });
}

// ── Keyword sync ─────────────────────────────────────────────────────────────
async function loadFromStorage() {
  const s = await chrome.storage.local.get(["urlKeywords", "contentKeywords", "whitelist"]);
  if (Array.isArray(s.urlKeywords)) urlKeywords = s.urlKeywords;
  if (Array.isArray(s.contentKeywords)) contentKeywords = s.contentKeywords;
  if (Array.isArray(s.whitelist)) whitelist = s.whitelist;
  compileRegexes();
}

// Apply a keyword payload (from /keywords or the stream). The rules are only
// rebuilt when the url keywords or whitelist actually changed — the poll lands
// every 30s and nearly always carries the same set.
//
// Updates run one at a time: on wake the /keywords fetch and the stream's first
// message land together, and two overlapping rebuilds each read the old rule IDs
// and then add the same new ones ("does not have a unique ID").
let applyQueue = Promise.resolve();
function applyKeywords(data) {
  applyQueue = applyQueue.then(() => applyKeywordsNow(data), () => applyKeywordsNow(data));
  return applyQueue;
}

async function applyKeywordsNow(data) {
  const before = JSON.stringify([urlKeywords, whitelist]);
  if (Array.isArray(data.url_keywords)) urlKeywords = data.url_keywords;
  if (Array.isArray(data.content_keywords)) contentKeywords = data.content_keywords;
  if (Array.isArray(data.whitelist)) whitelist = data.whitelist;
  compileRegexes();
  await chrome.storage.local.set({ urlKeywords, contentKeywords, whitelist });
  const rules = await chrome.declarativeNetRequest.getDynamicRules();
  if (!rules.length || JSON.stringify([urlKeywords, whitelist]) !== before) {
    reportCache.clear();
    await rebuildBlockingRules();
  }
  broadcastKeywords();
}

async function fetchKeywords() {
  try {
    const res = await fetch(`${GLOCKER}/keywords`);
    if (!res.ok) throw new Error(`status ${res.status}`);
    await applyKeywords(await res.json());
    setStatus(true);
  } catch (e) {
    dbg("glocker: keyword fetch failed, keeping current set:", e.message);
    setStatus(false);
  }
}

// Live updates from /keywords-stream. Each chunk (an update or the server's
// keepalive comment) makes an extension API call, which resets the worker's 30s
// idle timer, so the stream keeps the worker alive the way Firefox's persistent
// background page holds its EventSource open.
let streaming = false;
async function connectStream() {
  if (streaming) return;
  streaming = true;
  try {
    const res = await fetch(`${GLOCKER}/keywords-stream`, { cache: "no-store" });
    if (!res.ok || !res.body) throw new Error(`status ${res.status}`);
    setStatus(true);
    const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      chrome.runtime.getPlatformInfo();
      buf += value;
      let end;
      while ((end = buf.indexOf("\n\n")) >= 0) {
        const event = buf.slice(0, end);
        buf = buf.slice(end + 2);
        const data = event.split("\n").filter((l) => l.startsWith("data:")).map((l) => l.slice(5).trim()).join("\n");
        if (!data) continue;
        try {
          await applyKeywords(JSON.parse(data));
        } catch (e) {
          dbg("glocker: bad stream message:", e);
        }
      }
    }
  } catch (e) {
    dbg("glocker: keyword stream failed:", e.message);
  } finally {
    streaming = false;
    setStatus(false);
  }
}

// Push content keywords + whitelist to any open content scripts (they also pull
// on load via GET_KEYWORDS, so this is just for live updates).
function broadcastKeywords() {
  chrome.tabs.query({}, (tabs) => {
    for (const tab of tabs) {
      if (tab.id != null) {
        chrome.tabs.sendMessage(tab.id, { type: "KEYWORDS_UPDATE", contentKeywords, whitelist }).catch(() => {});
      }
    }
  });
}

// ── Reporting: DNR blocks, this observes and reports (non-blocking) ───────────
// Each blocked URL is reported once, as Firefox's URL cache does: revisiting it
// (or a page re-requesting it) redirects again but doesn't log a new hit.
const reportCache = new Set();

chrome.webRequest.onBeforeRequest.addListener(
  (details) => {
    const lower = details.url.toLowerCase();
    if (lower.includes("127.0.0.1") || lower.includes("localhost")) return;
    if (reportCache.has(lower) || isWhitelisted(lower)) return;
    for (const k of urlKeywordRegexes) {
      if (k.regex.test(lower)) {
        if (reportCache.size >= REPORT_CACHE_SIZE) reportCache.delete(reportCache.values().next().value);
        reportCache.add(lower);
        fetch(`${GLOCKER}/report`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ url: details.url, trigger: `url-keyword:${k.keyword}`, timestamp: Date.now() }),
        }).catch(() => {});
        break;
      }
    }
  },
  { urls: ["<all_urls>"], types: RESOURCE_TYPES }
  // no "blocking" — MV3 observation only; the DNR rule does the redirect.
);

// Content scripts ask for the latest content keywords + whitelist.
chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message.type === "GET_KEYWORDS") {
    hydrated.then(() => sendResponse({ contentKeywords, whitelist }));
    return true; // async response
  }
});

// ── Lifecycle ────────────────────────────────────────────────────────────────
// Every wake (install, browser start, alarm, a webRequest or message event) runs
// this file top to bottom: hydrate from storage, then refresh and reopen the
// stream. 30s is the shortest alarm Chrome allows.
chrome.alarms.create(REFRESH_ALARM, { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((a) => {
  if (a.name === REFRESH_ALARM) {
    fetchKeywords();
    connectStream();
  }
});

// One always-on line so you can confirm which build is live (the rest of the
// logging is DEBUG-gated).
console.info("Glocker extension " + buildTag());

compileRegexes();
setStatus(false);
const hydrated = loadFromStorage();
hydrated.then(() => {
  fetchKeywords();
  connectStream();
});
