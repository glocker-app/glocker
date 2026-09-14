package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"glocker/internal/config"
	"glocker/internal/state"
)

func TestHandleKeywordsRequest(t *testing.T) {
	cfg := &config.Config{
		Content: config.ContentConfig{Keywords: config.ExtensionKeywordsConfig{
			URLKeywords:     []string{"gambling", "gaming"},
			ContentKeywords: []string{"adult", "explicit"},
			Whitelist:       []string{"work.com", "news.com"},
		}},
	}

	req := httptest.NewRequest("GET", "/keywords", nil)
	w := httptest.NewRecorder()

	HandleKeywordsRequest(cfg, w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	urlKeywords := response["url_keywords"].([]interface{})
	if len(urlKeywords) != 2 {
		t.Errorf("Expected 2 URL keywords, got %d", len(urlKeywords))
	}

	// 2 configured entries + the hardcoded hosted dashboard (always whitelisted).
	whitelist := response["whitelist"].([]interface{})
	if len(whitelist) != 3 {
		t.Errorf("Expected 3 whitelist entries (2 configured + hardcoded dashboard), got %d", len(whitelist))
	}
	if whitelist[0] != hostedDashboardDomain {
		t.Errorf("Expected %q to be whitelisted by default, got %v", hostedDashboardDomain, whitelist[0])
	}
}

func TestHandleKeywordsRequest_MethodNotAllowed(t *testing.T) {
	cfg := &config.Config{}
	req := httptest.NewRequest("POST", "/keywords", nil)
	w := httptest.NewRecorder()

	HandleKeywordsRequest(cfg, w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", w.Code)
	}
}

func TestHandleReportRequest(t *testing.T) {
	// Create temporary log file
	tmpFile, err := os.CreateTemp("", "glocker-reports-*.log")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	cfg := &config.Config{
		Content: config.ContentConfig{Monitoring: config.ContentMonitoringConfig{
			Enabled: true,
			LogFile: tmpFile.Name(),
		}},
		ViolationTracking: config.ViolationTrackingConfig{
			Enabled: false,
		},
	}

	report := state.ContentReport{
		URL:       "https://example.com/page",
		Domain:    "example.com",
		Trigger:   "url_keyword_match",
		Timestamp: time.Now().UnixMilli(),
	}

	body, _ := json.Marshal(report)
	req := httptest.NewRequest("POST", "/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	HandleReportRequest(cfg, w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Verify log file was written
	content, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	if !strings.Contains(string(content), "example.com") {
		t.Error("Log file should contain domain name")
	}
}

func TestHandleReportRequest_IgnoresDashboard(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "glocker-reports-*.log")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	cfg := &config.Config{
		Content:           config.ContentConfig{Monitoring: config.ContentMonitoringConfig{Enabled: true, LogFile: tmpFile.Name()}},
		ViolationTracking: config.ViolationTrackingConfig{Enabled: false},
	}

	// A content report for the dashboard itself must be ignored, not logged —
	// /stats displays the very keywords it records.
	report := state.ContentReport{
		URL:       "http://glocker.localhost/stats",
		Domain:    "glocker.localhost",
		Trigger:   "content_keyword_match",
		Timestamp: time.Now().UnixMilli(),
	}
	body, _ := json.Marshal(report)
	req := httptest.NewRequest("POST", "/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	HandleReportRequest(cfg, w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	content, _ := os.ReadFile(tmpFile.Name())
	if strings.TrimSpace(string(content)) != "" {
		t.Errorf("dashboard report should not be logged, got: %q", content)
	}
}

func TestHandleReportRequest_Disabled(t *testing.T) {
	cfg := &config.Config{
		Content: config.ContentConfig{Monitoring: config.ContentMonitoringConfig{
			Enabled: false,
		}},
	}

	req := httptest.NewRequest("POST", "/report", nil)
	w := httptest.NewRecorder()

	HandleReportRequest(cfg, w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}
}

func TestHandleBlockedPageRequest(t *testing.T) {
	req := httptest.NewRequest("GET", "/blocked?domain=example.com&matched=example.com", nil)
	w := httptest.NewRecorder()

	HandleBlockedPageRequest(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "example.com") {
		t.Error("Blocked page should contain domain name")
	}

	if !strings.Contains(body, "Site Blocked") {
		t.Error("Blocked page should contain 'Site Blocked' title")
	}

	if !strings.Contains(body, "Glocker") {
		t.Error("Blocked page should mention Glocker")
	}
}

func TestHandleBlockedPageRequest_DefaultValues(t *testing.T) {
	req := httptest.NewRequest("GET", "/blocked", nil)
	w := httptest.NewRecorder()

	HandleBlockedPageRequest(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "this site") {
		t.Error("Should use default domain 'this site'")
	}
}

func TestGetBlockingReason_PermanentByDefault(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{
				Name: "example.com", // No time windows = permanent by default
			},
		}},
	}

	reason := GetBlockingReason(cfg, "example.com", time.Now())

	if !strings.Contains(reason, "always blocked") {
		t.Errorf("Expected 'always blocked', got '%s'", reason)
	}
	if !strings.Contains(reason, "permanent") {
		t.Errorf("Expected 'permanent' in reason, got '%s'", reason)
	}
}

func TestGetBlockingReason_Unblockable(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{
				Name:        "example.com",
				Unblockable: true, // Can be temporarily unblocked
			},
		}},
	}

	reason := GetBlockingReason(cfg, "example.com", time.Now())

	if !strings.Contains(reason, "always blocked") {
		t.Errorf("Expected 'always blocked' in reason, got '%s'", reason)
	}
	if !strings.Contains(reason, "temporarily unblocked") {
		t.Errorf("Expected 'temporarily unblocked' in reason, got '%s'", reason)
	}
}

func TestGetBlockingReason_TimeWindow(t *testing.T) {
	now := time.Now()
	currentDay := now.Weekday().String()[:3]

	cfg := &config.Config{
		Domains: config.DomainsConfig{List: []config.Domain{
			{
				Name: "example.com",
				BlockWindows: []config.TimeWindow{
					{
						Days:  []string{currentDay},
						Start: "00:00",
						End:   "23:59",
					},
				},
			},
		}},
	}

	reason := GetBlockingReason(cfg, "example.com", now)

	if !strings.Contains(reason, "time-based block") {
		t.Errorf("Expected 'time-based block', got '%s'", reason)
	}

	if !strings.Contains(reason, currentDay) {
		t.Errorf("Expected current day '%s' in reason, got '%s'", currentDay, reason)
	}
}

func TestLogContentReport(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "glocker-test-*.log")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	cfg := &config.Config{
		Content: config.ContentConfig{Monitoring: config.ContentMonitoringConfig{
			LogFile: tmpFile.Name(),
		}},
	}

	report := &state.ContentReport{
		URL:       "https://example.com/bad",
		Domain:    "example.com",
		Trigger:   "keyword_match",
		Timestamp: time.Now().UnixMilli(),
	}

	err = LogContentReport(cfg, report)
	if err != nil {
		t.Fatalf("LogContentReport failed: %v", err)
	}

	content, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	logStr := string(content)
	if !strings.Contains(logStr, "keyword_match") {
		t.Error("Log should contain trigger")
	}
	if !strings.Contains(logStr, "example.com") {
		t.Error("Log should contain domain")
	}
}

func TestLogUnblockEntry(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "glocker-unblock-*.log")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	cfg := &config.Config{
		Domains: config.DomainsConfig{Unblocking: config.UnblockingConfig{
			LogFile: tmpFile.Name(),
		}},
	}

	unblockTime := time.Now()
	restoreTime := unblockTime.Add(30 * time.Minute)

	err = LogUnblockEntry(cfg, "example.com", "work", unblockTime, restoreTime)
	if err != nil {
		t.Fatalf("LogUnblockEntry failed: %v", err)
	}

	content, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	// Verify JSON structure
	var entry state.UnblockLogEntry
	if err := json.Unmarshal(content, &entry); err != nil {
		t.Fatalf("Failed to parse log entry: %v", err)
	}

	if entry.Domain != "example.com" {
		t.Errorf("Expected domain 'example.com', got '%s'", entry.Domain)
	}
	if entry.Reason != "work" {
		t.Errorf("Expected reason 'work', got '%s'", entry.Reason)
	}
}

func TestParseUnblockLog(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "glocker-unblock-*.log")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	cfg := &config.Config{
		Domains: config.DomainsConfig{Unblocking: config.UnblockingConfig{
			LogFile: tmpFile.Name(),
		}},
	}

	// Write test entries
	now := time.Now()
	entry1 := state.UnblockLogEntry{
		UnblockTime: now,
		RestoreTime: now.Add(30 * time.Minute),
		Reason:      "work",
		Domain:      "reddit.com",
	}
	entry2 := state.UnblockLogEntry{
		UnblockTime: now.AddDate(0, 0, -1), // Yesterday
		RestoreTime: now.AddDate(0, 0, -1).Add(30 * time.Minute),
		Reason:      "research",
		Domain:      "youtube.com",
	}

	file, _ := os.OpenFile(tmpFile.Name(), os.O_APPEND|os.O_WRONLY, 0644)
	json1, _ := json.Marshal(entry1)
	json2, _ := json.Marshal(entry2)
	file.WriteString(string(json1) + "\n")
	file.WriteString(string(json2) + "\n")
	file.Close()

	// Parse log
	stats, err := ParseUnblockLog(cfg)
	if err != nil {
		t.Fatalf("ParseUnblockLog failed: %v", err)
	}

	if stats.TotalCount != 2 {
		t.Errorf("Expected total count 2, got %d", stats.TotalCount)
	}

	if stats.TodayCount != 1 {
		t.Errorf("Expected today count 1, got %d", stats.TodayCount)
	}

	if stats.ReasonCounts["work"] != 1 {
		t.Error("Expected 1 'work' reason")
	}

	if stats.DomainCounts["reddit.com"] != 1 {
		t.Error("Expected 1 'reddit.com' domain")
	}
}

func TestParseUnblockLog_NonExistent(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{Unblocking: config.UnblockingConfig{
			LogFile: "/nonexistent/file.log",
		}},
	}

	stats, err := ParseUnblockLog(cfg)
	if err != nil {
		t.Fatalf("Should not error on non-existent file: %v", err)
	}

	if stats.TotalCount != 0 {
		t.Errorf("Expected 0 total count, got %d", stats.TotalCount)
	}
}

func TestIsValidUnblockReason_NoConfig(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{Unblocking: config.UnblockingConfig{
			Reasons: []string{},
		}},
	}

	// Should allow any reason when none configured
	if !IsValidUnblockReason(cfg, "anything") {
		t.Error("Should allow any reason when none configured")
	}
}

func TestIsValidUnblockReason_WithConfig(t *testing.T) {
	cfg := &config.Config{
		Domains: config.DomainsConfig{Unblocking: config.UnblockingConfig{
			Reasons: []string{"work", "research", "emergency"},
		}},
	}

	if !IsValidUnblockReason(cfg, "work") {
		t.Error("Should allow valid reason 'work'")
	}

	if !IsValidUnblockReason(cfg, "WORK") {
		t.Error("Should be case-insensitive")
	}

	if IsValidUnblockReason(cfg, "gaming") {
		t.Error("Should reject invalid reason 'gaming'")
	}
}

func TestIsDashboardHost_HostedDashboardAlwaysExempt(t *testing.T) {
	exempt := []string{
		"glockerapp.com", "www.glockerapp.com", "GlockerApp.com", "peek.glockerapp.com",
		"127.0.0.1", "localhost", "glocker.localhost",
	}
	for _, h := range exempt {
		if !isDashboardHost(h) {
			t.Errorf("isDashboardHost(%q) = false, want true (always exempt)", h)
		}
	}
	for _, h := range []string{"glockerapp.com.evil.com", "notglockerapp.com", "example.com"} {
		if isDashboardHost(h) {
			t.Errorf("isDashboardHost(%q) = true, want false", h)
		}
	}
}

func TestEffectiveWhitelist_HardcodesDashboard(t *testing.T) {
	cfg := &config.Config{}
	if wl := effectiveWhitelist(cfg); len(wl) == 0 || wl[0] != hostedDashboardDomain {
		t.Fatalf("effectiveWhitelist should always include %q, got %v", hostedDashboardDomain, wl)
	}
	cfg.Content.Keywords.Whitelist = []string{"example.com"}
	wl := effectiveWhitelist(cfg)
	if len(wl) != 2 || wl[0] != hostedDashboardDomain || wl[1] != "example.com" {
		t.Errorf("effectiveWhitelist = %v, want [%s example.com]", wl, hostedDashboardDomain)
	}
	if len(cfg.Content.Keywords.Whitelist) != 1 {
		t.Errorf("config whitelist was mutated: %v", cfg.Content.Keywords.Whitelist)
	}
}
