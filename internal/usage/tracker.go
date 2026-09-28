package usage

import (
	"context"
	"time"

	"glocker/internal/config"
)

// DefaultInterval is the sampling period used when Config.Interval is unset —
// the same default as the usage_monitor config (arbtt also samples once a
// minute), taken from there so the two can't drift.
const DefaultInterval = time.Duration(config.DefaultUsageInterval) * time.Second

// Config controls a Tracker.
type Config struct {
	// Interval between samples. Defaults to DefaultInterval when <= 0.
	Interval time.Duration
	// OnError, if set, is invoked for non-fatal capture/write errors so the
	// loop can keep running (e.g. a transient X error).
	OnError func(error)
}

// Tracker periodically pulls a Sample from a Source and hands it to a Sink.
// It owns neither: the caller constructs and closes them.
type Tracker struct {
	source Source
	sink   Sink
	cfg    Config
}

// NewTracker builds a Tracker from a Source, a Sink, and a Config.
func NewTracker(source Source, sink Sink, cfg Config) *Tracker {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	return &Tracker{source: source, sink: sink, cfg: cfg}
}

// Run samples immediately, then once per Interval, until ctx is cancelled.
// It returns ctx.Err() (typically context.Canceled) on shutdown.
func (t *Tracker) Run(ctx context.Context) error {
	ticker := time.NewTicker(t.cfg.Interval)
	defer ticker.Stop()

	t.sampleOnce()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			t.sampleOnce()
		}
	}
}

func (t *Tracker) sampleOnce() {
	sample, err := t.source.Capture()
	if err != nil {
		t.reportError(err)
		return
	}
	if err := t.sink.Write(sample); err != nil {
		t.reportError(err)
	}
}

func (t *Tracker) reportError(err error) {
	if t.cfg.OnError != nil {
		t.cfg.OnError(err)
	}
}
