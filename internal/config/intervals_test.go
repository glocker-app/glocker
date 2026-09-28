package config

import (
	"testing"
	"time"
)

func TestIntervalResolvers(t *testing.T) {
	var zero Config
	if got := zero.UsageMonitor.Interval(); got != DefaultUsageInterval*time.Second {
		t.Errorf("usage default = %v", got)
	}
	if got := zero.Sync.Interval(); got != DefaultSyncIntervalSeconds*time.Second {
		t.Errorf("sync default = %v", got)
	}
	if got := zero.Heartbeat.Interval(); got != DefaultHeartbeatIntervalMinutes*time.Minute {
		t.Errorf("heartbeat default = %v", got)
	}
	if got := zero.Heartbeat.Timeout(); got != DefaultHeartbeatTimeoutSeconds*time.Second {
		t.Errorf("heartbeat timeout default = %v", got)
	}

	set := Config{
		UsageMonitor: UsageMonitorConfig{IntervalSeconds: 10},
		Sync:         SyncConfig{IntervalSeconds: 15},
		Heartbeat:    HeartbeatConfig{IntervalMinutes: 5, TimeoutSeconds: 7},
	}
	if set.UsageMonitor.Interval() != 10*time.Second || set.Sync.Interval() != 15*time.Second ||
		set.Heartbeat.Interval() != 5*time.Minute || set.Heartbeat.Timeout() != 7*time.Second {
		t.Errorf("configured values not honoured: %v %v %v %v", set.UsageMonitor.Interval(),
			set.Sync.Interval(), set.Heartbeat.Interval(), set.Heartbeat.Timeout())
	}
}
