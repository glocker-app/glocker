package state

import (
	"fmt"
	"sync"
	"time"

	"glocker/internal/config"
)

// FileChecksum represents a file's checksum for tamper detection.
type FileChecksum struct {
	Path     string
	Checksum string
	Exists   bool
}

func (f FileChecksum) String() string {
	return fmt.Sprintf("Path : %s, Checksum : %s, Exists : %v", f.Path, f.Checksum, f.Exists)
}

// TempUnblock represents a temporarily unblocked domain with expiration time.
type TempUnblock struct {
	Domain    string
	ExpiresAt time.Time
}

// ContentReport represents a content monitoring violation from the browser extension.
type ContentReport struct {
	URL       string `json:"url"`
	Domain    string `json:"domain,omitempty"`
	Trigger   string `json:"trigger"`
	Timestamp int64  `json:"timestamp"`
}

// UnblockLogEntry represents a logged unblock event.
type UnblockLogEntry struct {
	UnblockTime time.Time `json:"unblock_time"`
	RestoreTime time.Time `json:"restore_time"`
	Reason      string    `json:"reason"`
	Domain      string    `json:"domain"`
}

// UnblockStats contains statistics about unblock events.
type UnblockStats struct {
	TodayCount   int               `json:"today_count"`
	TotalCount   int               `json:"total_count"`
	TodayEntries []UnblockLogEntry `json:"today_entries"`
	ReasonCounts map[string]int    `json:"reason_counts"`
	DomainCounts map[string]int    `json:"domain_counts"`
}

// LifecycleLogEntry represents a logged install/uninstall event.
type LifecycleLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`   // "install" or "uninstall"
	Reason    string    `json:"reason,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// ProcessInfo contains information about a running process.
type ProcessInfo struct {
	PID         string
	Name        string
	CommandLine string
	ParentPID   string
}

// Violation represents a tracked violation event.
type Violation struct {
	Timestamp time.Time
	Host      string
	URL       string
	Type      string // "web_access", "content_report", "forbidden_program"
}

// Global state variables (private, accessed via functions)
var (
	// Panic mode state
	panicUntil      time.Time
	lastSuspendTime time.Time
	panicMutex      sync.RWMutex

	// Email rate limiting
	lastEmailTimes = make(map[string]time.Time)
	emailMutex     sync.RWMutex

	// Tamper detection
	globalChecksums      []FileChecksum
	globalFilesToMonitor []string
	globalConfig         *config.Config
	checksumMutex        sync.RWMutex

	// Temporary unblocks
	tempUnblocks      []TempUnblock
	tempUnblocksMutex sync.RWMutex

	// SSE clients (for browser extension updates)
	sseClients      []chan string
	sseClientsMutex sync.RWMutex

	// Violation tracking
	violations         []Violation
	violationsMutex    sync.RWMutex
	lastViolationReset time.Time
)

// Panic mode functions

// GetPanicUntil returns the time until which panic mode is active.
func GetPanicUntil() time.Time {
	panicMutex.RLock()
	defer panicMutex.RUnlock()
	return panicUntil
}

// SetPanicUntil sets the time until which panic mode should be active.
// Round(0) strips the monotonic clock reading so later Before/After comparisons
// against `time.Now()` fall back to wall clock — important because the
// monotonic clock pauses across system suspend on Linux, which is the exact
// scenario panic mode lives in.
func SetPanicUntil(t time.Time) {
	panicMutex.Lock()
	defer panicMutex.Unlock()
	panicUntil = t.Round(0)
}

// GetLastSuspendTime returns the last time the system was suspended.
func GetLastSuspendTime() time.Time {
	panicMutex.RLock()
	defer panicMutex.RUnlock()
	return lastSuspendTime
}

// SetLastSuspendTime sets the last time the system was suspended.
// See SetPanicUntil for why we strip monotonic; this value is consumed by the
// same suspend-aware grace-period logic.
func SetLastSuspendTime(t time.Time) {
	panicMutex.Lock()
	defer panicMutex.Unlock()
	lastSuspendTime = t.Round(0)
}

// Email rate limiting functions

// GetLastEmailTime returns the last time an email was sent for a specific event type.
func GetLastEmailTime(eventType string) (time.Time, bool) {
	emailMutex.RLock()
	defer emailMutex.RUnlock()
	t, ok := lastEmailTimes[eventType]
	return t, ok
}

// SetLastEmailTime sets the last time an email was sent for a specific event type.
func SetLastEmailTime(eventType string, t time.Time) {
	emailMutex.Lock()
	defer emailMutex.Unlock()
	lastEmailTimes[eventType] = t
}

// Tamper detection functions

// GetGlobalChecksums returns a copy of the global checksums.
func GetGlobalChecksums() []FileChecksum {
	checksumMutex.RLock()
	defer checksumMutex.RUnlock()
	// Return a copy to prevent external modification
	result := make([]FileChecksum, len(globalChecksums))
	copy(result, globalChecksums)
	return result
}

// SetGlobalChecksums sets the global checksums.
func SetGlobalChecksums(checksums []FileChecksum) {
	checksumMutex.Lock()
	defer checksumMutex.Unlock()
	globalChecksums = checksums
}

// UpdateChecksum updates a specific file's checksum in the global list.
func UpdateChecksum(filePath string, checksum string, exists bool) {
	checksumMutex.Lock()
	defer checksumMutex.Unlock()
	for i, c := range globalChecksums {
		if c.Path == filePath {
			globalChecksums[i].Checksum = checksum
			globalChecksums[i].Exists = exists
			return
		}
	}
	// If not found, add it
	globalChecksums = append(globalChecksums, FileChecksum{
		Path:     filePath,
		Checksum: checksum,
		Exists:   exists,
	})
}

// GetGlobalFilesToMonitor returns the list of files being monitored.
func GetGlobalFilesToMonitor() []string {
	checksumMutex.RLock()
	defer checksumMutex.RUnlock()
	result := make([]string, len(globalFilesToMonitor))
	copy(result, globalFilesToMonitor)
	return result
}

// SetGlobalFilesToMonitor sets the list of files to monitor.
func SetGlobalFilesToMonitor(files []string) {
	checksumMutex.Lock()
	defer checksumMutex.Unlock()
	globalFilesToMonitor = files
}

// GetGlobalConfig returns the global config pointer.
func GetGlobalConfig() *config.Config {
	checksumMutex.RLock()
	defer checksumMutex.RUnlock()
	return globalConfig
}

// SetGlobalConfig sets the global config pointer.
func SetGlobalConfig(cfg *config.Config) {
	checksumMutex.Lock()
	defer checksumMutex.Unlock()
	globalConfig = cfg
}

// Temporary unblock functions

// GetTempUnblocks returns a copy of the temporary unblocks list.
func GetTempUnblocks() []TempUnblock {
	tempUnblocksMutex.RLock()
	defer tempUnblocksMutex.RUnlock()
	result := make([]TempUnblock, len(tempUnblocks))
	copy(result, tempUnblocks)
	return result
}

// AddTempUnblock adds a temporary unblock entry. The expiry is stored without
// a monotonic clock reading so wall-clock comparison survives system suspend
// (see SetPanicUntil for the full rationale).
func AddTempUnblock(domain string, expiresAt time.Time) {
	tempUnblocksMutex.Lock()
	defer tempUnblocksMutex.Unlock()
	tempUnblocks = append(tempUnblocks, TempUnblock{
		Domain:    domain,
		ExpiresAt: expiresAt.Round(0),
	})
}

// SetTempUnblocks replaces the temporary unblocks list. Each entry's expiry
// is normalised to wall-clock-only (Round(0)) so comparisons survive suspend.
func SetTempUnblocks(unblocks []TempUnblock) {
	tempUnblocksMutex.Lock()
	defer tempUnblocksMutex.Unlock()
	for i := range unblocks {
		unblocks[i].ExpiresAt = unblocks[i].ExpiresAt.Round(0)
	}
	tempUnblocks = unblocks
}

// SSE client functions

// AddSSEClient adds a new SSE client channel.
func AddSSEClient(ch chan string) {
	sseClientsMutex.Lock()
	defer sseClientsMutex.Unlock()
	sseClients = append(sseClients, ch)
}

// RemoveSSEClient removes an SSE client channel.
func RemoveSSEClient(ch chan string) {
	sseClientsMutex.Lock()
	defer sseClientsMutex.Unlock()
	for i, client := range sseClients {
		if client == ch {
			sseClients = append(sseClients[:i], sseClients[i+1:]...)
			return
		}
	}
}

// BroadcastSSE sends a message to all connected SSE clients.
func BroadcastSSE(message string) {
	sseClientsMutex.RLock()
	defer sseClientsMutex.RUnlock()
	for _, client := range sseClients {
		select {
		case client <- message:
		default:
			// Client not ready to receive, skip
		}
	}
}

// GetSSEClientCount returns the number of connected SSE clients.
func GetSSEClientCount() int {
	sseClientsMutex.RLock()
	defer sseClientsMutex.RUnlock()
	return len(sseClients)
}

// Violation tracking functions

// GetViolations returns a copy of the violations list.
func GetViolations() []Violation {
	violationsMutex.RLock()
	defer violationsMutex.RUnlock()
	result := make([]Violation, len(violations))
	copy(result, violations)
	return result
}

// AddViolation adds a new violation to the list. The timestamp is stored
// without monotonic so the rolling-60-minute window check can't be tricked
// by system suspend (the monotonic clock pauses across suspend on Linux, but
// users expect "60 minutes" to mean wall-clock time).
func AddViolation(v Violation) {
	violationsMutex.Lock()
	defer violationsMutex.Unlock()
	v.Timestamp = v.Timestamp.Round(0)
	violations = append(violations, v)
}

// ClearViolations clears all violations.
func ClearViolations() {
	violationsMutex.Lock()
	defer violationsMutex.Unlock()
	violations = nil
	lastViolationReset = time.Now()
}

// GetLastViolationReset returns the last time violations were reset.
func GetLastViolationReset() time.Time {
	violationsMutex.RLock()
	defer violationsMutex.RUnlock()
	return lastViolationReset
}

// SetLastViolationReset sets the last violation reset time.
func SetLastViolationReset(t time.Time) {
	violationsMutex.Lock()
	defer violationsMutex.Unlock()
	lastViolationReset = t
}

// ── Syncer status (glocker -> glockpeek) ────────────────
// SyncSummary is what the syncer reports for `glocker -status`: when it last
// pushed a batch, the size of that batch, and cumulative totals for the session.

// SyncSummary holds the syncer's last-push info for status reporting.
type SyncSummary struct {
	LastSyncAt time.Time
	Last       map[string]int   // counts in the most recent batch
	Total      map[string]int   // cumulative counts pushed this session
	Cursors    map[string]int64 // per-source high-water TS (ms) after the last push
	// LastError is the most recent push failure (e.g. a 422 rejection), "" when the
	// last push succeeded. LastErrorAt is when it occurred. Cleared on next success.
	LastError   string
	LastErrorAt time.Time
}

var (
	syncSummary SyncSummary
	syncMutex   sync.RWMutex
)

// RecordSync updates the syncer status after a successful push. counts is the
// batch just sent, keyed by source (violations/unblocks/lifecycle/usage/heartbeat);
// cursors is the per-source high-water TS after the push, used to report how many
// records have since accumulated. Both are copied.
func RecordSync(counts map[string]int, cursors map[string]int64) {
	syncMutex.Lock()
	defer syncMutex.Unlock()
	syncSummary.LastSyncAt = time.Now()
	syncSummary.Last = counts
	if syncSummary.Total == nil {
		syncSummary.Total = map[string]int{}
	}
	for k, v := range counts {
		syncSummary.Total[k] += v
	}
	syncSummary.Cursors = make(map[string]int64, len(cursors))
	for k, v := range cursors {
		syncSummary.Cursors[k] = v
	}
	syncSummary.LastError = "" // a successful push clears any prior failure
	syncSummary.LastErrorAt = time.Time{}
}

// RecordSyncError records the most recent failed push (e.g. the server rejecting
// plaintext with a 422) so `glocker -status` can surface it. Cleared by the next
// successful RecordSync.
func RecordSyncError(msg string) {
	syncMutex.Lock()
	defer syncMutex.Unlock()
	syncSummary.LastError = msg
	syncSummary.LastErrorAt = time.Now()
}

// GetSyncSummary returns a copy of the current syncer status. LastSyncAt is zero
// if nothing has been synced this session.
func GetSyncSummary() SyncSummary {
	syncMutex.RLock()
	defer syncMutex.RUnlock()
	out := SyncSummary{LastSyncAt: syncSummary.LastSyncAt, Last: map[string]int{}, Total: map[string]int{}, Cursors: map[string]int64{}, LastError: syncSummary.LastError, LastErrorAt: syncSummary.LastErrorAt}
	for k, v := range syncSummary.Last {
		out.Last[k] = v
	}
	for k, v := range syncSummary.Total {
		out.Total[k] = v
	}
	for k, v := range syncSummary.Cursors {
		out.Cursors[k] = v
	}
	return out
}
