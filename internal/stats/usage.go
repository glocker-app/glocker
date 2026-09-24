package stats

// usageActive is the focused window of a sample, as sent to the dashboard.
type usageActive struct {
	Class    string `json:"class"`
	Instance string `json:"instance,omitempty"`
	Title    string `json:"title"`
}

// usageSample mirrors the shape the frontend expects from /api/data (see
// lib/parse.js parseUsage): the active window + idle time, with ts in epoch ms.
// The full window list from the raw log is dropped upstream.
type usageSample struct {
	TS          int64        `json:"ts"`
	DeviceID    uint         `json:"deviceId"`
	IdleMS      int64        `json:"idleMs"`
	Active      *usageActive `json:"active"`
	WindowCount int          `json:"windowCount"`
}
