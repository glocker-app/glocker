// Glocker content monitor — Chrome (Manifest V3) service worker.
//
// Same job as the Firefox extension, adapted to MV3:
//   - MV3 has no blocking webRequest, so URL-keyword blocking is done with
//     declarativeNetRequest (DNR) redirect rules (which also persist across
//     service-worker restarts). A non-blocking webRequest listener reports the
//     matches to glocker (DNR can't run JS).
//   - Service workers can't hold an EventSource, so keywords are refreshed by
//     polling /keywords on a chrome.alarms timer instead of SSE.
//   - State is mirrored to chrome.storage.local so it survives SW restarts.

const GLOCKER = "http://127.0.0.1";
const REFRESH_ALARM = "glocker-refresh";
const RESOURCE_TYPES = ["main_frame", "sub_frame"];

// Fallback defaults until the first successful fetch from the agent.
let urlKeywords = ["gambling", "casino", "porn", "xxx"];
let contentKeywords = ["trigger1", "trigger2"];
let whitelist = ["github.com", "stackoverflow.com", "docs.google.com"];
let urlKeywordRegexes = [];
let whitelistRegexes = [];

const escapeRegex = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

function compileRegexes() {
  urlKeywordRegexes = urlKeywords.map((keyword) => ({
    keyword,
    regex: new RegExp(escapeRegex(keyword.toLowerCase()), "i"),
  }));
  whitelistRegexes = whitelist.map((pattern) => {
    if (pattern.startsWith("*.")) {
      const d = escapeRegex(pattern.slice(2));
      return { pattern, regex: new RegExp(`\\b[a-z0-9]([a-z0-9.-]*[a-z0-9])?\\.${d}\\b`, "i") };
    }
    return { pattern, regex: new RegExp(escapeRegex(pattern.toLowerCase()), "i") };
  });
}

function isWhitelisted(url) {
  return whitelistRegexes.some((w) => w.regex.test(url));
}

// ── declarativeNetRequest: the actual blocking ───────────────────────────────
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

  const domains = whitelist
    .map((w) => (w.startsWith("*.") ? w.slice(2) : w))
    .map((d) => d.replace(/^https?:\/\//, "").split("/")[0].trim())
    .filter(Boolean);
  if (domains.length) {
    rules.push({
      id: id++, priority: 2, action: { type: "allow" },
      condition: { requestDomains: domains, resourceTypes: RESOURCE_TYPES },
    });
  }

  for (const kw of urlKeywords) {
    const reason = encodeURIComponent(`URL contains blocked keyword: "${kw}"`);
    rules.push({
      id: id++, priority: 1,
      action: { type: "redirect", redirect: { url: `${GLOCKER}/blocked?reason=${reason}` } },
      condition: { regexFilter: escapeRegex(kw), isUrlFilterCaseSensitive: false, resourceTypes: RESOURCE_TYPES },
    });
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
  chrome.action.setTitle({ title: connected ? "Glocker: Active" : "Glocker: Disconnected" });
  chrome.action.setBadgeText({ text: connected ? "●" : "○" });
  chrome.action.setBadgeBackgroundColor({ color: connected ? "#4CAF50" : "#F44336" });
}

// ── Keyword sync ─────────────────────────────────────────────────────────────
async function persist() {
  await chrome.storage.local.set({ urlKeywords, contentKeywords, whitelist });
}

async function loadFromStorage() {
  const s = await chrome.storage.local.get(["urlKeywords", "contentKeywords", "whitelist"]);
  if (Array.isArray(s.urlKeywords)) urlKeywords = s.urlKeywords;
  if (Array.isArray(s.contentKeywords)) contentKeywords = s.contentKeywords;
  if (Array.isArray(s.whitelist)) whitelist = s.whitelist;
  compileRegexes();
}

async function fetchKeywords() {
  try {
    const res = await fetch(`${GLOCKER}/keywords`);
    if (!res.ok) throw new Error(`status ${res.status}`);
    const data = await res.json();
    if (Array.isArray(data.url_keywords)) urlKeywords = data.url_keywords;
    if (Array.isArray(data.content_keywords)) contentKeywords = data.content_keywords;
    if (Array.isArray(data.whitelist)) whitelist = data.whitelist;
    compileRegexes();
    await persist();
    await rebuildBlockingRules();
    setStatus(true);
    broadcastKeywords();
  } catch (e) {
    console.log("glocker: keyword fetch failed, keeping current set:", e.message);
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
chrome.webRequest.onBeforeRequest.addListener(
  (details) => {
    const lower = details.url.toLowerCase();
    if (lower.includes("127.0.0.1") || lower.includes("localhost")) return;
    if (isWhitelisted(lower)) return;
    for (const k of urlKeywordRegexes) {
      if (k.regex.test(lower)) {
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
    // The SW may have just woken with empty in-memory state; hydrate first.
    (async () => {
      if (!urlKeywordRegexes.length) await loadFromStorage();
      sendResponse({ contentKeywords, whitelist });
    })();
    return true; // async response
  }
});

// ── Lifecycle ────────────────────────────────────────────────────────────────
function init() {
  loadFromStorage().then(fetchKeywords);
  chrome.alarms.create(REFRESH_ALARM, { periodInMinutes: 1 });
  setStatus(false);
}

chrome.runtime.onInstalled.addListener(init);
chrome.runtime.onStartup.addListener(init);
chrome.alarms.onAlarm.addListener((a) => {
  if (a.name === REFRESH_ALARM) fetchKeywords();
});

// The SW can be spun up by an event (webRequest/message) without onStartup firing;
// hydrate in-memory state from storage so the observational reporter has keywords.
loadFromStorage();
