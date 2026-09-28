package config

import "time"

// Cadence resolvers. Each interval is set in exactly one place — its config
// field, falling back to the single Default* constant — and every consumer
// (daemon, installer, status output, syncer) resolves it through these methods
// rather than repeating the fallback inline, so the values can't drift apart.

const (
	// DefaultHeartbeatIntervalMinutes is how often glockdoc samples liveness.
	DefaultHeartbeatIntervalMinutes = 30
	// DefaultHeartbeatTimeoutSeconds bounds glockdoc's socket probe.
	DefaultHeartbeatTimeoutSeconds = 3
)

// Interval is the usage-monitor sampling cadence.
func (u UsageMonitorConfig) Interval() time.Duration {
	return secondsOr(u.IntervalSeconds, DefaultUsageInterval)
}

// Interval is the incremental sync cadence.
func (s SyncConfig) Interval() time.Duration {
	return secondsOr(s.IntervalSeconds, DefaultSyncIntervalSeconds)
}

// Interval is the glockdoc heartbeat cadence.
func (h HeartbeatConfig) Interval() time.Duration {
	m := h.IntervalMinutes
	if m <= 0 {
		m = DefaultHeartbeatIntervalMinutes
	}
	return time.Duration(m) * time.Minute
}

// Timeout is glockdoc's socket probe timeout.
func (h HeartbeatConfig) Timeout() time.Duration {
	return secondsOr(h.TimeoutSeconds, DefaultHeartbeatTimeoutSeconds)
}

func secondsOr(v, def int) time.Duration {
	if v <= 0 {
		v = def
	}
	return time.Duration(v) * time.Second
}
