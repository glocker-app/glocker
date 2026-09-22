// Page content analysis - will be populated from background script
let contentKeywords = ['trigger1', 'trigger2']; // fallback defaults
let whitelist = ['github.com', 'stackoverflow.com', 'docs.google.com']; // fallback defaults

// Cached compiled regex patterns for performance
let contentKeywordRegexes = [];
let whitelistRegexes = [];

// Global cleanup state
let isCleanedUp = false;

// Content hash tracking to prevent redundant analysis
let lastContentHash = '';

// Debounce timer reference for cleanup
let contentDebounceTimer = null;

// Compile content keywords into regex patterns for performance
function compileContentKeywordRegexes() {
  contentKeywordRegexes = contentKeywords.map(keyword => {
    const escapedKeyword = keyword.toLowerCase().replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    return {
      keyword: keyword,
      regex: new RegExp('\\b' + escapedKeyword + '\\b', 'i')
    };
  });
  console.log('Compiled regex patterns for', contentKeywordRegexes.length, 'content keywords');
}

// Compile whitelist patterns into regex patterns for performance
function compileWhitelistRegexes() {
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
  console.log('Compiled regex patterns for', whitelistRegexes.length, 'whitelist patterns');
}

// Request keywords from background script
async function fetchKeywords() {
  console.log('Requesting keywords from background script...');
  try {
    const response = await browser.runtime.sendMessage({
      type: 'GET_KEYWORDS'
    });

    if (response) {
      if (response.contentKeywords && Array.isArray(response.contentKeywords)) {
        contentKeywords = response.contentKeywords;
        compileContentKeywordRegexes();
        console.log('Updated content keywords from background:', contentKeywords);
      } else {
        console.log('No valid content_keywords in response, using defaults');
      }

      if (response.whitelist && Array.isArray(response.whitelist)) {
        whitelist = response.whitelist;
        compileWhitelistRegexes();
        console.log('Updated whitelist from background:', whitelist);
      } else {
        console.log('No valid whitelist in response, using defaults');
      }

      return response;
    }
  } catch (error) {
    console.log('Keywords request error:', error);
  }
  return null;
}

// Listen for keyword updates from background script
browser.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message.type === 'KEYWORDS_UPDATE') {
    // Skip processing if already cleaned up
    if (isCleanedUp) return;

    console.log('Received keyword update from background:', message.contentKeywords);
    contentKeywords = message.contentKeywords;
    compileContentKeywordRegexes();

    if (message.whitelist && Array.isArray(message.whitelist)) {
      console.log('Received whitelist update from background:', message.whitelist);
      whitelist = message.whitelist;
      compileWhitelistRegexes();
    }

    // Re-analyze current page with new keywords
    if (document.readyState === 'complete' && !isCleanedUp) {
      console.log('Re-analyzing page content with updated keywords');
      analyzeContent();
    }
  }
});

// Comprehensive cleanup function
function cleanup() {
  if (isCleanedUp) {
    console.log('Already cleaned up, skipping');
    return;
  }
  
  console.log('Cleaning up glocker extension resources');
  isCleanedUp = true;
  
  // Cancel any pending debounced analysis
  if (contentDebounceTimer) {
    clearTimeout(contentDebounceTimer);
    contentDebounceTimer = null;
  }

  // Disconnect and clear observer
  if (window.glockerObserver) {
    window.glockerObserver.disconnect();
    window.glockerObserver = null;
  }

  // Clear keyword arrays to free memory
  contentKeywords = null;
  
  console.log('Cleanup completed');
}

// Set up cleanup on multiple events to ensure it runs
window.addEventListener('beforeunload', cleanup, { once: true });
window.addEventListener('unload', cleanup, { once: true });
window.addEventListener('pagehide', cleanup, { once: true });


// Generate a simple hash of content for comparison
function generateContentHash(text) {
  let hash = 0;
  for (let i = 0; i < text.length; i++) {
    const char = text.charCodeAt(i);
    hash = ((hash << 5) - hash) + char;
    hash = hash & hash; // Convert to 32-bit integer
  }
  return hash.toString();
}

// Check if current URL is whitelisted
function isCurrentURLWhitelisted() {
  const currentURL = window.location.href.toLowerCase();
  const currentHostname = window.location.hostname.toLowerCase();
  
  for (let whitelistData of whitelistRegexes) {
    if (whitelistData.regex.test(currentURL) || whitelistData.regex.test(currentHostname)) {
      console.log("Current URL is whitelisted:", currentURL, "matched pattern:", whitelistData.pattern);
      return true;
    }
  }
  return false;
}

function analyzeContent() {
  // Skip if already cleaned up or no keywords
  if (isCleanedUp || !contentKeywords) {
    console.log('Skipping analysis - cleaned up or no keywords');
    return;
  }
  
  console.log('analyzeContent() called for URL:', window.location.href);
  
  // Skip localhost pages: prevents redirect loops, and stops the glocker
  // dashboard (glocker.localhost/stats) from catching itself — it displays the
  // very keywords it records. Covers 127.0.0.1, localhost, and any *.localhost.
  const host = window.location.hostname.toLowerCase();
  if (host === '127.0.0.1' || host === 'localhost' || host.endsWith('.localhost')) {
    console.log('Skipping localhost/dashboard analysis:', host);
    return;
  }
  
  // Check if current URL is whitelisted - if so, skip all content analysis
  if (isCurrentURLWhitelisted()) {
    console.log('Current URL is whitelisted, skipping content analysis');
    return;
  }
  
  const text = document.body ? document.body.textContent.toLowerCase() : '';
  
  // Check if content has changed since last analysis
  const contentHash = generateContentHash(text);
  if (contentHash === lastContentHash) {
    console.log('Content unchanged since last analysis, skipping');
    return;
  }
  lastContentHash = contentHash;
  
  console.log('Analyzing content, text length:', text.length);
  console.log('Current keywords to check:', contentKeywords);
  
  for (let keywordData of contentKeywordRegexes) {
    console.log('Checking for keyword:', keywordData.keyword);
    if (keywordData.regex.test(text)) {
      console.log('KEYWORD MATCH FOUND:', keywordData.keyword);
      const reportData = {
        url: window.location.href,
        domain: window.location.hostname,
        trigger: `content-keyword:${keywordData.keyword}`,
        timestamp: Date.now()
      };
      
      console.log('Sending report:', reportData);
      fetch('http://127.0.0.1/report', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(reportData)
      }).catch((error) => {
        console.log('Report send failed:', error);
      });
      
      // Redirect to blocked page with reason
      const reason = encodeURIComponent(`Page content contains blocked keyword: "${keywordData.keyword}"`);
      const domain = encodeURIComponent(window.location.hostname);
      console.log('Redirecting to blocked page with reason:', reason);
      window.location.replace(`http://127.0.0.1/blocked?reason=${reason}&domain=${domain}`);
      
      break; // Only report once per page
    }
  }
  console.log('Content analysis complete, no matches found');
}

// Helper function to check if a node contains meaningful text content
function hasTextContent(node) {
  if (node.nodeType === Node.TEXT_NODE) {
    return node.textContent.trim().length > 0;
  }
  if (node.nodeType === Node.ELEMENT_NODE) {
    // Skip script, style, and other non-visible elements
    const tagName = node.tagName ? node.tagName.toLowerCase() : '';
    if (['script', 'style', 'noscript', 'meta', 'link', 'title'].includes(tagName)) {
      return false;
    }
    return node.textContent.trim().length > 0;
  }
  return false;
}

// Helper function to check if mutations contain text changes
function hasTextChanges(mutations) {
  for (let mutation of mutations) {
    // Check for text content changes
    if (mutation.type === 'characterData') {
      if (mutation.target.textContent.trim().length > 0) {
        console.log('Text content changed:', mutation.target.textContent.substring(0, 50) + '...');
        return true;
      }
    }
    
    // Check for new nodes with text content
    if (mutation.type === 'childList') {
      // Check added nodes
      for (let node of mutation.addedNodes) {
        if (hasTextContent(node)) {
          console.log('Text content added via new node:', node.textContent.substring(0, 50) + '...');
          return true;
        }
      }
      
      // Check if removed nodes had significant text (for dynamic content replacement)
      for (let node of mutation.removedNodes) {
        if (hasTextContent(node)) {
          console.log('Text content removed, may indicate dynamic replacement');
          return true;
        }
      }
    }
  }
  return false;
}

// Set up MutationObserver to watch for dynamically loaded content
function setupContentMonitoring() {
  // Skip if already cleaned up
  if (isCleanedUp) {
    console.log('Skipping content monitoring setup - cleaned up');
    return;
  }
  
  console.log('Setting up content monitoring...');
  
  // Clean up existing observer if any
  if (window.glockerObserver) {
    window.glockerObserver.disconnect();
    window.glockerObserver = null;
  }
  
  // Initial content analysis
  analyzeContent();
  
  const DEBOUNCE_MS = 1000;

  // Watch for text content changes (debounced to avoid excessive CPU usage)
  const observer = new MutationObserver((mutations) => {
    // Skip if cleaned up
    if (isCleanedUp) return;

    // Only schedule analysis if there are actual text changes
    if (hasTextChanges(mutations)) {
      if (contentDebounceTimer) {
        clearTimeout(contentDebounceTimer);
      }
      contentDebounceTimer = setTimeout(() => {
        contentDebounceTimer = null;
        if (!isCleanedUp) {
          console.log('Debounced content analysis triggered');
          analyzeContent();
        }
      }, DEBOUNCE_MS);
    }
  });
  
  // Start observing changes to the entire document
  const targetNode = document.body || document.documentElement;
  console.log('Starting MutationObserver on:', targetNode.tagName);
  observer.observe(targetNode, {
    childList: true,
    subtree: true,
    characterData: true
  });
  
  // Store observer reference for cleanup
  window.glockerObserver = observer;
  console.log('Content monitoring setup complete');
}

console.log("Content script starting on:", window.location.href);
console.log("Document ready state:", document.readyState);

// Compile initial regex patterns
compileContentKeywordRegexes();
compileWhitelistRegexes();

// Initialize keywords on startup
fetchKeywords().then((data) => {
  console.log('Keywords fetch completed, data:', data);
  
  // Run content analysis after keywords are loaded
  if (document.readyState === 'loading') {
    console.log('Document still loading, waiting for DOMContentLoaded');
    document.addEventListener('DOMContentLoaded', setupContentMonitoring);
  } else {
    console.log('Document already loaded, setting up monitoring immediately');
    setupContentMonitoring();
  }
}).catch((error) => {
  console.log('Keywords fetch failed, using defaults:', error);
  
  // Still try to analyze with default keywords
  setupContentMonitoring();
});
