package web

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"glocker/internal/config"
	"glocker/internal/monitoring"
	"glocker/internal/state"
	"glocker/internal/utils"
)

// blockedDomainCache stores domains that have been checked, populated on first access.
// This keeps memory usage low while making repeat violations fast.
type blockedDomainCache struct {
	mu      sync.RWMutex
	domains map[string]*config.Domain // domain name -> full domain config (nil if not blocked)
}

var domainCache = &blockedDomainCache{
	domains: make(map[string]*config.Domain),
}

// HandleWebTrackingRequest processes incoming web tracking requests and enforces blocking.
// It checks if the requested host is in the blocked domains list and takes appropriate action.
func HandleWebTrackingRequest(cfg *config.Config, w http.ResponseWriter, r *http.Request) {
	// Check for blocked page first, before any other processing
	if r.URL.Path == "/blocked" {
		HandleBlockedPageRequest(w, r)
		return
	}

	host := r.Host
	if host == "" {
		host = r.Header.Get("Host")
	}

	// Remove port if present
	if colonIndex := strings.Index(host, ":"); colonIndex != -1 {
		host = host[:colonIndex]
	}

	slog.Debug("Web tracking request received", "host", host, "url", r.URL.String(), "method", r.Method)

	// Check if this host is blocked (uses lazy-loaded cache)
	isBlocked, matchedDomain := isHostBlocked(host)

	slog.Debug("Host blocking check", "host", host, "is_blocked", isBlocked, "matched_domain", matchedDomain)

	if isBlocked {
		// Determine the blocking reason
		blockingReason := GetBlockingReason(cfg, matchedDomain, time.Now())
		log.Printf("BLOCKED SITE ACCESS: %s -> matched domain: %s -> reason: %s", host, matchedDomain, blockingReason)

		// Record violation. RecordViolation is in-memory only (feeds -status and the
		// lock threshold); LogBlockedAccess persists it to the reports log so the
		// syncer ships it to glockpeek too — otherwise blocked-domain hits never
		// reach the dashboard (only the extension's keyword reports do).
		if cfg.ViolationTracking.Enabled {
			monitoring.RecordViolation(cfg, "web_access", host, r.URL.String())
		}
		if err := LogBlockedAccess(cfg, host, r.URL.String()); err != nil {
			slog.Warn("failed to log blocked access to reports", "host", host, "err", err)
		}

		// Execute the configured command
		if cfg.Domains.BlockedAccessCommand != "" {
			go executeWebTrackingCommand(cfg, host, r)
		}

		// Redirect to localhost blocked page to avoid double violation
		blockedURL := fmt.Sprintf("http://127.0.0.1/blocked?domain=%s&matched=%s&url=%s", host, matchedDomain, r.URL.String())
		http.Redirect(w, r, blockedURL, http.StatusFound)
	} else {
		// Not a blocked domain, return a simple response
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}
}

// isHostBlocked checks if a host is blocked using a lazy-loaded cache.
// First access loads from config and caches result. Subsequent accesses are instant.
// Returns (isBlocked, matchedDomain).
func isHostBlocked(host string) (bool, string) {
	// Build list of domains to check (host and all parent domains)
	domainsToCheck := []string{host}

	// Strip www. prefix if present
	hostWithoutWWW := host
	if strings.HasPrefix(host, "www.") {
		hostWithoutWWW = host[4:]
		domainsToCheck = append(domainsToCheck, hostWithoutWWW)
	}

	// Add parent domains (e.g., for "api.elevenlabs.io", check "elevenlabs.io")
	parts := strings.Split(hostWithoutWWW, ".")
	for i := 1; i < len(parts)-1; i++ {
		parentDomain := strings.Join(parts[i:], ".")
		domainsToCheck = append(domainsToCheck, parentDomain)
	}

	// Check cache first (fast path)
	domainCache.mu.RLock()
	for _, checkDomain := range domainsToCheck {
		if domainConfig, exists := domainCache.domains[checkDomain]; exists {
			domainCache.mu.RUnlock()
			if domainConfig != nil {
				// Domain is blocked and cached
				slog.Debug("Cache hit: domain is blocked", "host", host, "matched", checkDomain)
				return true, checkDomain
			}
			// Domain was checked before and is not blocked
			slog.Debug("Cache hit: domain not blocked", "host", host)
			return false, ""
		}
	}
	domainCache.mu.RUnlock()

	// Cache miss - need to load from config (slow path, only happens once per domain)
	slog.Debug("Cache miss: loading domain from config", "host", host)

	freshCfg, err := config.LoadConfig()
	if err != nil {
		log.Printf("Failed to load config for domain check: %v", err)
		return false, ""
	}

	now := time.Now()
	currentDay := now.Weekday().String()[:3]
	currentTime := now.Format("15:04")

	// Check each potential domain match
	domainCache.mu.Lock()
	defer domainCache.mu.Unlock()

	for _, checkDomain := range domainsToCheck {
		// Find in config
		for _, configDomain := range freshCfg.Domains.List {
			if configDomain.Name == checkDomain {
				// Check if domain is currently blocked
				// Domains without time windows are permanently blocked by default
				isBlocked := false

				if len(configDomain.BlockWindows) == 0 {
					// No block windows = always blocked
					isBlocked = true
				} else {
					// Check block windows
					for _, window := range configDomain.BlockWindows {
						if slices.Contains(window.Days, currentDay) && utils.IsInTimeWindow(currentTime, window.Start, window.End) {
							isBlocked = true
							break
						}
					}
				}

				if isBlocked {
					// Cache the domain config
					cachedDomain := configDomain // Copy
					domainCache.domains[checkDomain] = &cachedDomain
					slog.Debug("Cached blocked domain", "domain", checkDomain)
					return true, checkDomain
				}
			}
		}

		// Cache negative result (domain not blocked)
		domainCache.domains[checkDomain] = nil
	}

	slog.Debug("Domain not blocked", "host", host)
	return false, ""
}

// ClearDomainCache clears the domain cache. Called after config reload.
func ClearDomainCache() {
	domainCache.mu.Lock()
	defer domainCache.mu.Unlock()
	domainCache.domains = make(map[string]*config.Domain)
	slog.Debug("Domain cache cleared")
}

// executeWebTrackingCommand executes the configured command when a blocked site is accessed.
func executeWebTrackingCommand(cfg *config.Config, host string, r *http.Request) {
	if cfg.Domains.BlockedAccessCommand == "" {
		return
	}

	slog.Debug("Executing web tracking command", "host", host, "command", cfg.Domains.BlockedAccessCommand)

	// Split command into parts for proper execution
	parts := strings.Fields(cfg.Domains.BlockedAccessCommand)
	if len(parts) == 0 {
		return
	}

	cmd := exec.Command(parts[0], parts[1:]...)

	// Set environment variables with information about the blocked access attempt
	cmd.Env = append(os.Environ(),
		"GLOCKER_BLOCKED_HOST="+host,
		"GLOCKER_BLOCKED_URL="+r.URL.String(),
		"GLOCKER_BLOCKED_METHOD="+r.Method,
		"GLOCKER_BLOCKED_USER_AGENT="+r.Header.Get("User-Agent"),
		"GLOCKER_BLOCKED_REMOTE_ADDR="+r.RemoteAddr,
		"GLOCKER_BLOCKED_TIME="+time.Now().Format("2006-01-02 15:04:05"),
	)

	if err := cmd.Run(); err != nil {
		log.Printf("Failed to execute domains.blocked_access_command %q: %v (it runs as root with no desktop session; see conf.yaml.sample)", cfg.Domains.BlockedAccessCommand, err)
	} else {
		slog.Debug("Web tracking command executed successfully", "host", host)
	}
}

// HandleKeywordsRequest returns the current keyword configuration for browser extensions.
func HandleKeywordsRequest(cfg *config.Config, w http.ResponseWriter, r *http.Request) {
	// Only accept GET requests
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Set CORS headers to allow browser extension access
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")

	// Combine content keywords with URL keywords
	combinedContentKeywords := make([]string, 0, len(cfg.Content.Keywords.ContentKeywords)+len(cfg.Content.Keywords.URLKeywords))
	combinedContentKeywords = append(combinedContentKeywords, cfg.Content.Keywords.ContentKeywords...)
	combinedContentKeywords = append(combinedContentKeywords, cfg.Content.Keywords.URLKeywords...)

	// Create response with keywords
	response := map[string]interface{}{
		"url_keywords":     cfg.Content.Keywords.URLKeywords,
		"content_keywords": combinedContentKeywords,
		"whitelist":        effectiveWhitelist(cfg),
	}

	// Encode and send response
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Debug("Failed to encode keywords response", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	slog.Debug("Keywords request served", "url_keywords_count", len(cfg.Content.Keywords.URLKeywords), "content_keywords_count", len(combinedContentKeywords))
}

// HandleReportRequest processes content monitoring reports from browser extensions.
// hostOf extracts the hostname from a URL, or "" if it can't be parsed.
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// hostedDashboardDomain is the public glockpeek dashboard. It is ALWAYS exempt
// from monitoring — hardcoded, not config-driven — because the dashboard shows
// the very keywords it records (your flagged URLs/searches), so scanning it
// would flag itself. It must never be blocked or reported.
const hostedDashboardDomain = "glockerapp.com"

// isDashboardHost reports whether a host is always exempt from content
// monitoring: loopback / the local glocker dashboard (glocker.localhost, any
// *.localhost, 127.0.0.1, ::1, localhost) or the hosted glockpeek dashboard
// (glockerapp.com and its subdomains).
func isDashboardHost(h string) bool {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "127.0.0.1" || h == "::1" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	return h == hostedDashboardDomain || strings.HasSuffix(h, "."+hostedDashboardDomain)
}

// effectiveWhitelist is the configured extension whitelist plus the hardcoded
// always-allowed hosted dashboard, sent to the extension so it never scans it.
func effectiveWhitelist(cfg *config.Config) []string {
	return append([]string{hostedDashboardDomain}, cfg.Content.Keywords.Whitelist...)
}

func HandleReportRequest(cfg *config.Config, w http.ResponseWriter, r *http.Request) {
	slog.Info("Got a request here", "method", r.Method, "value", http.MethodPost)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Check if content monitoring is enabled
	if !cfg.Content.Monitoring.Enabled {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// Parse JSON body
	var report state.ContentReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		slog.Debug("Failed to parse report JSON", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Never record the glocker dashboard as a violation: it displays the very
	// keywords it records, so a stray scan of glocker.localhost/stats would loop.
	// (The extension also skips *.localhost; this is a server-side backstop.)
	if isDashboardHost(report.Domain) || isDashboardHost(hostOf(report.URL)) {
		slog.Debug("Ignoring content report for the glocker dashboard", "url", report.URL)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		return
	}

	// Record violation
	if cfg.ViolationTracking.Enabled {
		monitoring.RecordViolation(cfg, "content_report", report.Domain, report.URL)
	}

	// Log the report
	if err := LogContentReport(cfg, &report); err != nil {
		slog.Debug("Failed to log content report", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	slog.Debug("Content report logged", "url", report.URL, "trigger", report.Trigger)
	log.Printf("CONTENT REPORT: %s - %s", report.Trigger, report.URL)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// HandleSSERequest manages server-sent events connections for real-time keyword updates.
func HandleSSERequest(cfg *config.Config, w http.ResponseWriter, r *http.Request) {
	// Only accept GET requests
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Create a channel for this client
	clientChan := make(chan string, 10)

	// Add client to the list
	state.AddSSEClient(clientChan)

	slog.Debug("SSE client connected", "total_clients", state.GetSSEClientCount())

	// Combine content keywords with URL keywords for initial send
	combinedContentKeywords := make([]string, 0, len(cfg.Content.Keywords.ContentKeywords)+len(cfg.Content.Keywords.URLKeywords))
	combinedContentKeywords = append(combinedContentKeywords, cfg.Content.Keywords.ContentKeywords...)
	combinedContentKeywords = append(combinedContentKeywords, cfg.Content.Keywords.URLKeywords...)

	// Send initial keywords
	initialKeywords := map[string]interface{}{
		"url_keywords":     cfg.Content.Keywords.URLKeywords,
		"content_keywords": combinedContentKeywords,
		"whitelist":        effectiveWhitelist(cfg),
	}
	if keywordsJSON, err := json.Marshal(initialKeywords); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", keywordsJSON)
		w.(http.Flusher).Flush()
	}

	// Handle client disconnect
	defer func() {
		state.RemoveSSEClient(clientChan)
		close(clientChan)
		slog.Debug("SSE client disconnected", "remaining_clients", state.GetSSEClientCount())
	}()

	// Keep connection alive and send updates
	for {
		select {
		case message := <-clientChan:
			fmt.Fprintf(w, "data: %s\n\n", message)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		case <-time.After(20 * time.Second):
			// Keepalive ping. Under Chrome's 30s service-worker idle limit: each
			// chunk the Chrome extension reads resets that timer, so this is what
			// keeps its stream (and worker) alive.
			fmt.Fprintf(w, ": keepalive\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
}

// GetBlockingReason returns a human-readable reason for why a domain is blocked.
// Checks cfg.Domains.List if populated (tests), otherwise uses cache or loads from disk.
func GetBlockingReason(cfg *config.Config, domain string, now time.Time) string {
	currentDay := now.Weekday().String()[:3]
	currentTime := now.Format("15:04")

	var configDomain config.Domain
	found := false

	// If cfg.Domains.List is populated (e.g., in tests), use it directly
	if len(cfg.Domains.List) > 0 {
		for _, d := range cfg.Domains.List {
			if d.Name == domain {
				configDomain = d
				found = true
				break
			}
		}
	} else {
		// In normal runtime, cfg.Domains.List is cleared for memory optimization
		// Check if we have this domain cached
		domainCache.mu.RLock()
		cachedDomain, exists := domainCache.domains[domain]
		domainCache.mu.RUnlock()

		if exists && cachedDomain != nil {
			// Use cached domain config
			configDomain = *cachedDomain
			found = true
		} else {
			// Not cached, load from disk
			freshCfg, err := config.LoadConfig()
			if err != nil {
				log.Printf("Failed to reload config for blocking reason: %v", err)
				return "blocked by glocker"
			}

			// Find the domain in the config
			for _, d := range freshCfg.Domains.List {
				if d.Name == domain {
					configDomain = d
					found = true
					break
				}
			}
		}
	}

	if !found {
		return "blocked by glocker"
	}

	// NEW BEHAVIOR: Domains without block windows are always blocked (permanent by default)
	if len(configDomain.BlockWindows) == 0 {
		if configDomain.Unblockable {
			return "always blocked (can be temporarily unblocked)"
		}
		return "always blocked (permanent)"
	}

	// Check which block window is active
	for _, window := range configDomain.BlockWindows {
		if slices.Contains(window.Days, currentDay) && utils.IsInTimeWindow(currentTime, window.Start, window.End) {
			return fmt.Sprintf("time-based block (active %s-%s on %s)", window.Start, window.End, strings.Join(window.Days, ","))
		}
	}

	return "blocked by glocker"
}
