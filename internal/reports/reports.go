// Package reports provides parsing and querying of glocker log files.
package reports

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"

	"glocker/internal/config"
)

// UnblockEntry represents a single unblock log entry.
type UnblockEntry struct {
	UnblockTime time.Time `json:"unblock_time"`
	RestoreTime time.Time `json:"restore_time"`
	Reason      string    `json:"reason"`
	Domain      string    `json:"domain"`
}

// ReportType indicates whether a report was triggered by URL or content keyword.
type ReportType string

const (
	ReportTypeURL     ReportType = "url-keyword"
	ReportTypeContent ReportType = "content-keyword"
	// ReportTypeWebAccess is a direct hit on a blocked domain (the local web server
	// caught a request to it — e.g. curl or a browser). Recorded here so it reaches
	// glockpeek, not just the in-memory count the lock threshold uses.
	ReportTypeWebAccess ReportType = "web_access"
)

// ReportEntry represents a single content/URL report entry.
type ReportEntry struct {
	Timestamp time.Time
	Type      ReportType
	Keyword   string
	URL       string
	Domain    string
}

// ParseUnblocksLog reads and parses the unblocks log file.
func ParseUnblocksLog(path string) ([]UnblockEntry, error) {
	if path == "" {
		path = config.DefaultUnblocksLogFile
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []UnblockEntry
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry UnblockEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Skip malformed lines
			continue
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return entries, err
	}

	return entries, nil
}

// reportLineRegex matches: [2025-11-17 15:35:46] | type:keyword | url | domain
var reportLineRegex = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\] \| (url-keyword|content-keyword|web_access):([^ |]+) \| ([^ |]+)(?: \| (.+))?$`)

// ParseReportsLog reads and parses the reports log file.
func ParseReportsLog(path string) ([]ReportEntry, error) {
	if path == "" {
		path = config.DefaultReportsLogFile
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []ReportEntry
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		entry, ok := parseReportLine(line)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return entries, err
	}

	return entries, nil
}

func parseReportLine(line string) (ReportEntry, bool) {
	matches := reportLineRegex.FindStringSubmatch(line)
	if matches == nil {
		return ReportEntry{}, false
	}

	timestamp, err := time.ParseInLocation("2006-01-02 15:04:05", matches[1], time.Local)
	if err != nil {
		return ReportEntry{}, false
	}

	entry := ReportEntry{
		Timestamp: timestamp,
		Type:      ReportType(matches[2]),
		Keyword:   matches[3],
		URL:       matches[4],
	}

	if len(matches) > 5 && matches[5] != "" {
		entry.Domain = matches[5]
	}

	return entry, true
}

// FilterUnblocks filters unblock entries based on criteria.
type UnblockFilter struct {
	Domain    string     // Filter by domain (substring match)
	Reason    string     // Filter by reason (exact match)
	StartTime *time.Time // Filter entries after this time
	EndTime   *time.Time // Filter entries before this time
}

// FilterUnblocks returns entries matching the filter criteria.
func FilterUnblocks(entries []UnblockEntry, filter UnblockFilter) []UnblockEntry {
	var result []UnblockEntry

	for _, e := range entries {
		if filter.Domain != "" && !strings.Contains(e.Domain, filter.Domain) {
			continue
		}
		if filter.Reason != "" && e.Reason != filter.Reason {
			continue
		}
		if filter.StartTime != nil && e.UnblockTime.Before(*filter.StartTime) {
			continue
		}
		if filter.EndTime != nil && e.UnblockTime.After(*filter.EndTime) {
			continue
		}
		result = append(result, e)
	}

	return result
}

// ReportFilter filters report entries based on criteria.
type ReportFilter struct {
	Type      ReportType // Filter by report type
	Keyword   string     // Filter by keyword (substring match)
	Domain    string     // Filter by domain (substring match)
	URL       string     // Filter by URL (substring match)
	StartTime *time.Time // Filter entries after this time
	EndTime   *time.Time // Filter entries before this time
}

// FilterReports returns entries matching the filter criteria.
func FilterReports(entries []ReportEntry, filter ReportFilter) []ReportEntry {
	var result []ReportEntry

	for _, e := range entries {
		if filter.Type != "" && e.Type != filter.Type {
			continue
		}
		if filter.Keyword != "" && !strings.Contains(e.Keyword, filter.Keyword) {
			continue
		}
		if filter.Domain != "" && !strings.Contains(e.Domain, filter.Domain) {
			continue
		}
		if filter.URL != "" && !strings.Contains(e.URL, filter.URL) {
			continue
		}
		if filter.StartTime != nil && e.Timestamp.Before(*filter.StartTime) {
			continue
		}
		if filter.EndTime != nil && e.Timestamp.After(*filter.EndTime) {
			continue
		}
		result = append(result, e)
	}

	return result
}

// LifecycleEntry represents a single install/uninstall log entry.
type LifecycleEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`             // "install" or "uninstall"
	Reason    string    `json:"reason,omitempty"` // Only for uninstalls
	Note      string    `json:"note,omitempty"`   // Free-form context for uninstalls
}

// ParseLifecycleLog reads and parses the lifecycle log file.
func ParseLifecycleLog(path string) ([]LifecycleEntry, error) {
	if path == "" {
		path = config.DefaultLifecycleLogFile
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []LifecycleEntry
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry LifecycleEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Skip malformed lines
			continue
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return entries, err
	}

	return entries, nil
}

// RecentUninstalls returns uninstall entries at or after `since`. It is used to
// gauge how habitual recent teardowns have been so the mindful uninstall gate
// can escalate. A missing log file is treated as no history (nil, nil).
// Entries are returned in log order, so the last element is the most recent.
func RecentUninstalls(path string, since time.Time) ([]LifecycleEntry, error) {
	entries, err := ParseLifecycleLog(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []LifecycleEntry
	for _, e := range entries {
		if e.Type != "uninstall" {
			continue
		}
		if e.Timestamp.Before(since) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// LastLifecycleEntry returns the most recent entry in the lifecycle log, or
// ok=false if the log is empty or absent. Entries are in log order, so the last
// one is the newest.
func LastLifecycleEntry(path string) (LifecycleEntry, bool) {
	entries, err := ParseLifecycleLog(path)
	if err != nil || len(entries) == 0 {
		return LifecycleEntry{}, false
	}
	return entries[len(entries)-1], true
}

// HeartbeatSample is one liveness observation from the glockdoc watchdog log.
type HeartbeatSample struct {
	Timestamp time.Time `json:"timestamp"`
	Alive     bool      `json:"alive"`
}

// DowntimePeriod is a contiguous span during which the watchdog observed
// glocker to be down (a run of alive:false samples). Open means glocker was
// still down as of the most recent sample.
type DowntimePeriod struct {
	Start time.Time
	End   time.Time
	Open  bool
}

// ParseHeartbeatLog reads and parses the glockdoc heartbeat log (one JSON
// sample per line). Malformed lines are skipped.
func ParseHeartbeatLog(path string) ([]HeartbeatSample, error) {
	if path == "" {
		path = config.DefaultHeartbeatLogFile
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var samples []HeartbeatSample
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var s HeartbeatSample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			continue
		}
		samples = append(samples, s)
	}
	if err := scanner.Err(); err != nil {
		return samples, err
	}
	return samples, nil
}

// DowntimePeriods collapses runs of consecutive alive:false samples into spans.
// A span starts at the first down sample and ends when the next up sample is
// observed (the "recovered by" bound); a run still down at the end of the log is
// left Open and extended to now. Spans shorter than minDuration are dropped as
// restart blips. Samples are assumed in log (chronological) order.
func DowntimePeriods(samples []HeartbeatSample, now time.Time, minDuration time.Duration) []DowntimePeriod {
	out := []DowntimePeriod{}
	var start *time.Time
	for i := range samples {
		s := samples[i]
		if !s.Alive {
			if start == nil {
				t := s.Timestamp
				start = &t
			}
			continue
		}
		// Alive: close any open down-run at this recovery sample.
		if start != nil {
			if s.Timestamp.Sub(*start) >= minDuration {
				out = append(out, DowntimePeriod{Start: *start, End: s.Timestamp})
			}
			start = nil
		}
	}
	if start != nil && now.Sub(*start) >= minDuration {
		out = append(out, DowntimePeriod{Start: *start, End: now, Open: true})
	}
	return out
}

// LifecycleFilter filters lifecycle entries based on criteria.
type LifecycleFilter struct {
	Type      string     // Filter by type ("install" or "uninstall")
	Reason    string     // Filter by reason (substring match)
	StartTime *time.Time // Filter entries after this time
	EndTime   *time.Time // Filter entries before this time
}

// FilterLifecycle returns entries matching the filter criteria.
func FilterLifecycle(entries []LifecycleEntry, filter LifecycleFilter) []LifecycleEntry {
	var result []LifecycleEntry

	for _, e := range entries {
		if filter.Type != "" && e.Type != filter.Type {
			continue
		}
		if filter.Reason != "" && !strings.Contains(e.Reason, filter.Reason) {
			continue
		}
		if filter.StartTime != nil && e.Timestamp.Before(*filter.StartTime) {
			continue
		}
		if filter.EndTime != nil && e.Timestamp.After(*filter.EndTime) {
			continue
		}
		result = append(result, e)
	}

	return result
}
