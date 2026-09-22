// Debug logging — off by default. Flip to true to trace matching in the console.
const DEBUG = false;
function dbg() { if (DEBUG) console.log.apply(console, arguments); }

// Performance optimization configuration
const PERFORMANCE_CONFIG = {
  // File extensions to skip (resources that don't need keyword checking)
  SKIP_FILE_EXTENSIONS: [
    'jpg', 'jpeg', 'png', 'gif', 'svg', 'webp', 'bmp', 'ico', 'tiff',
    'css', 'js', 'woff', 'woff2', 'ttf', 'eot', 'otf',
    'mp4', 'mp3', 'wav', 'avi', 'mov', 'wmv', 'flv',
    'pdf', 'doc', 'docx', 'xls', 'xlsx', 'zip', 'rar'
  ],
  
  // URL patterns to skip (API endpoints, tracking, etc.)
  SKIP_URL_PATTERNS: [
    '/api/', '/log', '/beacon', '/collect', '/track', '/analytics',
    '/metrics', '/telemetry', '/ping', '/health', '/status',
    '/voyager/api/', '/sensorcollect/', '/webchannel/', '/rgstr',
    '/traces', '/sentry', '/bootstrap/', '/usage'
  ],
  
  // Request types to check (only main pages and frames)
  ALLOWED_REQUEST_TYPES: ['main_frame', 'sub_frame'],
  
  // Cache size for URL check results
  URL_CACHE_SIZE: 1000
};

// URL check result cache to prevent redundant processing
const urlCheckCache = new Map();

// Keyword storage - will be populated from server
let urlKeywords = ['gambling', 'casino', 'porn', 'xxx']; // fallback defaults
let contentKeywords = ['trigger1', 'trigger2']; // fallback defaults
let whitelist = ['github.com', 'stackoverflow.com', 'docs.google.com']; // fallback defaults

// Cached compiled regex patterns for performance
let urlKeywordRegexes = [];
let contentKeywordRegexes = [];
let whitelistRegexes = [];

// Global cleanup state for background script
let backgroundCleanedUp = false;

// Status tracking
let glockerConnected = false;

// Compile keywords into regex patterns for performance
function compileKeywordRegexes() {
  // Compile URL keyword regexes - less restrictive, no word boundaries
  urlKeywordRegexes = urlKeywords.map(keyword => {
    const escapedKeyword = keyword.toLowerCase().replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    return {
      keyword: keyword,
      regex: new RegExp(escapedKeyword, 'i')
    };
  });
  
  // Compile content keyword regexes - keep word boundaries for content
  contentKeywordRegexes = contentKeywords.map(keyword => {
    const escapedKeyword = keyword.toLowerCase().replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    return {
      keyword: keyword,
      regex: new RegExp('\\b' + escapedKeyword + '\\b', 'i')
    };
  });
  
  // Compile whitelist regexes
  whitelistRegexes = whitelist.map(pattern => {
    let regex;

    // Check if pattern is a wildcard subdomain pattern (e.g., "*.atlassian.net")
    if (pattern.startsWith('*.')) {
      // Remove the "*." prefix
      const domain = pattern.substring(2);
      // Escape special regex characters in the domain
      const escapedDomain = domain.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

      // Create regex to match any subdomain of the domain
      // Pattern: one or more subdomain levels + . + domain
      // Example: *.atlassian.net matches jira.atlassian.net, app.prod.atlassian.net, etc.
      // but NOT atlassian.net itself
      regex = new RegExp(`\\b[a-z0-9]([a-z0-9.-]*[a-z0-9])?\\.${escapedDomain}\\b`, 'i');
    } else {
      // Exact match - escape the entire pattern
      const escapedPattern = pattern.toLowerCase().replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
      regex = new RegExp(escapedPattern, 'i');
    }

    return {
      pattern: pattern,
      regex: regex
    };
  });
  
  dbg('Compiled regex patterns for', urlKeywordRegexes.length, 'URL keywords,', contentKeywordRegexes.length, 'content keywords, and', whitelistRegexes.length, 'whitelist patterns');
}

// buildTag reports which build is running: manifest version + the commit stamped
// in at sign time (build-info.js), e.g. "v2.5 (a1b2c3d)". "dev" for unsigned runs.
function buildTag() {
  const v = browser.runtime.getManifest().version;
  const c = (typeof GLOCKER_BUILD !== 'undefined' && GLOCKER_BUILD.commit) ? GLOCKER_BUILD.commit : 'dev';
  return `v${v} (${c})`;
}

// Update browser action icon based on connection status
function updateStatusIcon() {
  let title, badgeText, badgeColor;

  if (glockerConnected) {
    title = 'Glocker: Active';
    badgeText = '●';
    badgeColor = '#4CAF50'; // Green for active
  } else {
    title = 'Glocker: Disconnected';
    badgeText = '○';
    badgeColor = '#F44336'; // Red for disconnected
  }

  browser.browserAction.setTitle({ title: title + ' — ' + buildTag() });
  browser.browserAction.setBadgeText({ text: badgeText });
  browser.browserAction.setBadgeBackgroundColor({ color: badgeColor });
}

// Fetch keywords from glocker server
async function fetchKeywords() {
  try {
    const response = await fetch('http://127.0.0.1/keywords');
    if (response.ok) {
      const data = await response.json();
      if (data.url_keywords && Array.isArray(data.url_keywords)) {
        urlKeywords = data.url_keywords;
        dbg('Updated URL keywords from server:', urlKeywords);
      }
      if (data.content_keywords && Array.isArray(data.content_keywords)) {
        contentKeywords = data.content_keywords;
        dbg('Updated content keywords from server:', contentKeywords);
      }
      if (data.whitelist && Array.isArray(data.whitelist)) {
        whitelist = data.whitelist;
        dbg('Updated whitelist from server:', whitelist);
      }
      
      // Recompile regex patterns with new keywords
      compileKeywordRegexes();
      
      // Update connection status
      glockerConnected = true;
      updateStatusIcon();
      
      // Broadcast updated keywords to all content scripts
      broadcastKeywordsToContentScripts();
      
      return data;
    } else {
      glockerConnected = false;
      updateStatusIcon();
    }
  } catch (error) {
    dbg('Failed to fetch keywords from server, using defaults:', error);
    glockerConnected = false;
    updateStatusIcon();
  }
  return null;
}

// Broadcast keywords to all content scripts
function broadcastKeywordsToContentScripts() {
  browser.tabs.query({}, (tabs) => {
    tabs.forEach((tab) => {
      browser.tabs.sendMessage(tab.id, {
        type: 'KEYWORDS_UPDATE',
        contentKeywords: contentKeywords,
        whitelist: whitelist
      }).catch(() => {
        // Ignore errors for tabs that don't have content scripts
      });
    });
  });
}

// Set up SSE connection for real-time keyword updates
function setupSSEConnection() {
  dbg('Setting up centralized SSE connection for keyword updates...');
  
  // Clean up existing connection if any
  if (window.backgroundSSE) {
    window.backgroundSSE.close();
    window.backgroundSSE = null;
  }
  
  const eventSource = new EventSource('http://127.0.0.1/keywords-stream');
  
  eventSource.onopen = function(event) {
    dbg('SSE connection opened');
    glockerConnected = true;
    updateStatusIcon();
  };
  
  eventSource.onmessage = function(event) {
    // Skip processing if cleaned up
    if (backgroundCleanedUp) return;
    
    dbg('SSE message received:', event.data);
    try {
      const data = JSON.parse(event.data);
      let updated = false;
      
      if (data.url_keywords && Array.isArray(data.url_keywords)) {
        urlKeywords = data.url_keywords;
        dbg('Updated URL keywords via SSE:', urlKeywords);
        updated = true;
      }
      
      if (data.content_keywords && Array.isArray(data.content_keywords)) {
        contentKeywords = data.content_keywords;
        dbg('Updated content keywords via SSE:', contentKeywords);
        updated = true;
      }
      
      if (data.whitelist && Array.isArray(data.whitelist)) {
        whitelist = data.whitelist;
        dbg('Updated whitelist via SSE:', whitelist);
        updated = true;
      }
      
      // Recompile regex patterns and broadcast updates
      if (updated) {
        compileKeywordRegexes();
        broadcastKeywordsToContentScripts();
      }
    } catch (error) {
      dbg('Failed to parse SSE message:', error);
    }
  };
  
  eventSource.onerror = function(event) {
    dbg('SSE connection error:', event);
    glockerConnected = false;
    updateStatusIcon();
    // Connection will automatically retry unless cleaned up
    if (backgroundCleanedUp) {
      eventSource.close();
    }
  };
  
  // Store reference for cleanup
  window.backgroundSSE = eventSource;
}


// Handle messages from content scripts
browser.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message.type === 'GET_KEYWORDS') {
    // Send current keywords and whitelist to requesting content script
    sendResponse({
      contentKeywords: contentKeywords,
      whitelist: whitelist
    });
  }
});

// Background script cleanup function
function cleanupBackground() {
  if (backgroundCleanedUp) return;
  
  dbg('Cleaning up background script resources');
  backgroundCleanedUp = true;
  
  // Close SSE connection
  if (window.backgroundSSE) {
    window.backgroundSSE.close();
    window.backgroundSSE = null;
  }
  
  // Clear keyword arrays
  urlKeywords = null;
  contentKeywords = null;
  
  // Clear URL cache
  urlCheckCache.clear();
  
  dbg('Background cleanup completed');
}

// Set up cleanup for background script
browser.runtime.onSuspend.addListener(cleanupBackground);

// Initialize on startup. One always-on line so you can confirm which build is
// live (the rest of the logging is DEBUG-gated).
console.info('Glocker extension ' + buildTag());

// Initialize status icon and keywords on startup
updateStatusIcon();

// Compile initial regex patterns
compileKeywordRegexes();

fetchKeywords().then(() => {
  // Set up centralized SSE connection for real-time updates
  setupSSEConnection();
}).catch(() => {
  // Still set up SSE connection even if initial fetch failed
  setupSSEConnection();
});

// Check if URL should be skipped for performance reasons
function shouldSkipURL(url, requestType) {
  // Only check main frames and sub-frames (actual web pages)
  if (!PERFORMANCE_CONFIG.ALLOWED_REQUEST_TYPES.includes(requestType)) {
    return true;
  }
  
  // Skip file extensions that don't need keyword checking
  const fileExtensionRegex = new RegExp(`\\.(${PERFORMANCE_CONFIG.SKIP_FILE_EXTENSIONS.join('|')})(\\?|$)`, 'i');
  if (fileExtensionRegex.test(url)) {
    return true;
  }
  
  // Skip URL patterns (API endpoints, tracking, etc.)
  for (const pattern of PERFORMANCE_CONFIG.SKIP_URL_PATTERNS) {
    if (url.includes(pattern)) {
      return true;
    }
  }
  
  return false;
}

// hostOf returns the hostname of a URL for the blocked page's "Access to X" line
// (empty on a parse failure — the page then falls back to "this site").
function hostOf(u) {
  try { return new URL(u).hostname; } catch (e) { return ""; }
}

// Check if URL is whitelisted
function isWhitelisted(url) {
  for (let whitelistData of whitelistRegexes) {
    if (whitelistData.regex.test(url)) {
      dbg("URL is whitelisted:", url, "matched pattern:", whitelistData.pattern);
      return true;
    }
  }
  return false;
}

// Manage URL check cache
function getCachedResult(url) {
  return urlCheckCache.get(url);
}

function setCachedResult(url, result) {
  // Limit cache size to prevent memory issues
  if (urlCheckCache.size >= PERFORMANCE_CONFIG.URL_CACHE_SIZE) {
    // Remove oldest entry
    const firstKey = urlCheckCache.keys().next().value;
    urlCheckCache.delete(firstKey);
  }
  urlCheckCache.set(url, result);
}

browser.webRequest.onBeforeRequest.addListener(
  function(details) {
    const url = details.url.toLowerCase();
    
    // Skip checking localhost/127.0.0.1 URLs to prevent redirect loops
    if (url.includes('127.0.0.1') || url.includes('localhost')) {
      return;
    }
    
    // Performance optimization: Skip non-webpage resources and API calls
    if (shouldSkipURL(url, details.type)) {
      return;
    }
    
    // Use the full URL including query parameters for keyword matching
    let urlToCheck = url;
    
    // Check cache first to avoid redundant processing
    const cachedResult = getCachedResult(urlToCheck);
    if (cachedResult !== undefined) {
      if (cachedResult.blocked) {
        dbg("URL blocked (cached):", urlToCheck, "keyword:", cachedResult.keyword);
        const reason = encodeURIComponent(`URL contains blocked keyword: "${cachedResult.keyword}"`);
        const domain = encodeURIComponent(hostOf(urlToCheck));
        return {redirectUrl: `http://127.0.0.1/blocked?reason=${reason}&domain=${domain}`};
      }
      return; // URL was checked before and not blocked
    }
    
    // Check if URL is whitelisted - if so, skip all blocking logic
    if (isWhitelisted(urlToCheck)) {
      dbg("URL is whitelisted, skipping blocking logic:", urlToCheck);
      setCachedResult(urlToCheck, { blocked: false });
      return;
    }
    
    dbg("Checking URL:", urlToCheck, "against", urlKeywordRegexes.length, "patterns");
    
    for (let keywordData of urlKeywordRegexes) {
      if (keywordData.regex.test(urlToCheck)) {
        dbg("Found ", keywordData.keyword, " in ", urlToCheck);
        
        // Cache the blocked result
        setCachedResult(urlToCheck, { blocked: true, keyword: keywordData.keyword });
        
        // Report to glocker
        fetch('http://127.0.0.1/report', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({
            url: details.url,
            trigger: `url-keyword:${keywordData.keyword}`,
            timestamp: Date.now()
          })
        }).catch(() => {}); // Ignore failures
        
        // Redirect to blocked page with reason
        const reason = encodeURIComponent(`URL contains blocked keyword: "${keywordData.keyword}"`);
        const domain = encodeURIComponent(hostOf(urlToCheck));
        return {redirectUrl: `http://127.0.0.1/blocked?reason=${reason}&domain=${domain}`};
      }
    }
    
    // Cache the non-blocked result
    setCachedResult(urlToCheck, { blocked: false });
  },
  {urls: ["<all_urls>"]},
  ["blocking"]
);
