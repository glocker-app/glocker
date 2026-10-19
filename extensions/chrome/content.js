// Glocker content monitor — content script (Chrome MV3). Scans page text for
// blocked keywords and, on a match, reports to glocker and redirects to the
// blocked page. Ported from the Firefox content script (browser.* -> chrome.*);
// the DOM/scan logic is unchanged.

let contentKeywords = ["trigger1", "trigger2"];
let whitelist = ["github.com", "stackoverflow.com", "docs.google.com"];
let contentKeywordRegexes = [];
let whitelistRegexes = [];
let isCleanedUp = false;
let lastContentHash = "";
let debounceTimer = null;

const escapeRegex = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

function compileContentRegexes() {
  contentKeywordRegexes = contentKeywords.map((keyword) => ({
    keyword,
    regex: new RegExp("\\b" + escapeRegex(keyword.toLowerCase()) + "\\b", "i"),
  }));
}

function compileWhitelistRegexes() {
  whitelistRegexes = whitelist.map((pattern) => {
    if (pattern.startsWith("*.")) {
      const d = escapeRegex(pattern.slice(2));
      return { pattern, regex: new RegExp(`\\b[a-z0-9]([a-z0-9.-]*[a-z0-9])?\\.${d}\\b`, "i") };
    }
    return { pattern, regex: new RegExp(escapeRegex(pattern.toLowerCase()), "i") };
  });
}

// Ask the service worker for the current content keywords + whitelist.
async function fetchKeywords() {
  try {
    const response = await chrome.runtime.sendMessage({ type: "GET_KEYWORDS" });
    if (response) {
      if (Array.isArray(response.contentKeywords)) { contentKeywords = response.contentKeywords; compileContentRegexes(); }
      if (Array.isArray(response.whitelist)) { whitelist = response.whitelist; compileWhitelistRegexes(); }
    }
    return response;
  } catch {
    return null;
  }
}

// Live updates pushed from the service worker.
chrome.runtime.onMessage.addListener((message) => {
  if (message.type === "KEYWORDS_UPDATE" && !isCleanedUp) {
    contentKeywords = message.contentKeywords || contentKeywords;
    compileContentRegexes();
    if (Array.isArray(message.whitelist)) { whitelist = message.whitelist; compileWhitelistRegexes(); }
    if (document.readyState === "complete") analyzeContent();
  }
});

function cleanup() {
  if (isCleanedUp) return;
  isCleanedUp = true;
  if (debounceTimer) { clearTimeout(debounceTimer); debounceTimer = null; }
  if (window.glockerObserver) { window.glockerObserver.disconnect(); window.glockerObserver = null; }
  contentKeywords = null;
}
window.addEventListener("beforeunload", cleanup, { once: true });
window.addEventListener("pagehide", cleanup, { once: true });

function contentHash(text) {
  let hash = 0;
  for (let i = 0; i < text.length; i++) {
    hash = ((hash << 5) - hash + text.charCodeAt(i)) & 0xffffffff;
  }
  return String(hash);
}

function currentURLWhitelisted() {
  const url = window.location.href.toLowerCase();
  const host = window.location.hostname.toLowerCase();
  return whitelistRegexes.some((w) => w.regex.test(url) || w.regex.test(host));
}

function analyzeContent() {
  if (isCleanedUp || !contentKeywords) return;

  // Skip localhost/dashboard: prevents redirect loops and stops the glocker
  // dashboard from catching the keywords it displays.
  const host = window.location.hostname.toLowerCase();
  if (host === "127.0.0.1" || host === "localhost" || host.endsWith(".localhost")) return;
  if (currentURLWhitelisted()) return;

  // The regexes are case-insensitive, so scan the raw textContent — no need to
  // allocate a lowercased copy of the whole page.
  const text = document.body ? document.body.textContent : "";
  const hash = contentHash(text);
  if (hash === lastContentHash) return;
  lastContentHash = hash;

  for (const k of contentKeywordRegexes) {
    if (k.regex.test(text)) {
      fetch("http://127.0.0.1/report", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          url: window.location.href,
          domain: window.location.hostname,
          trigger: `content-keyword:${k.keyword}`,
          timestamp: Date.now(),
        }),
      }).catch(() => {});
      const reason = encodeURIComponent(`Page content contains blocked keyword: "${k.keyword}"`);
      const domain = encodeURIComponent(window.location.hostname);
      window.location.replace(`http://127.0.0.1/blocked?reason=${reason}&domain=${domain}`);
      break; // report once per page
    }
  }
}

function hasTextContent(node) {
  if (node.nodeType === Node.TEXT_NODE) return node.textContent.trim().length > 0;
  if (node.nodeType === Node.ELEMENT_NODE) {
    const tag = node.tagName ? node.tagName.toLowerCase() : "";
    if (["script", "style", "noscript", "meta", "link", "title"].includes(tag)) return false;
    return node.textContent.trim().length > 0;
  }
  return false;
}

function hasTextChanges(mutations) {
  for (const m of mutations) {
    if (m.type === "characterData" && m.target.textContent.trim().length > 0) return true;
    if (m.type === "childList") {
      for (const n of m.addedNodes) if (hasTextContent(n)) return true;
      for (const n of m.removedNodes) if (hasTextContent(n)) return true;
    }
  }
  return false;
}

function setupContentMonitoring() {
  if (isCleanedUp) return;
  if (window.glockerObserver) { window.glockerObserver.disconnect(); window.glockerObserver = null; }
  analyzeContent();

  const DEBOUNCE_MS = 1000;
  const observer = new MutationObserver((mutations) => {
    if (isCleanedUp || !hasTextChanges(mutations)) return;
    if (debounceTimer) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      debounceTimer = null;
      if (!isCleanedUp) analyzeContent();
    }, DEBOUNCE_MS);
  });
  observer.observe(document.body || document.documentElement, { childList: true, subtree: true, characterData: true });
  window.glockerObserver = observer;
}

compileContentRegexes();
compileWhitelistRegexes();
fetchKeywords().finally(() => {
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", setupContentMonitoring);
  } else {
    setupContentMonitoring();
  }
});
