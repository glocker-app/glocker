// Package lock implements a timeout-based screen locker. Unlike password-based
// lockers, it unlocks automatically after a configurable timeout.
//
// The package is display-server agnostic: a Backend does the actual locking, and
// Select picks the right one for the current session (X11 or Wayland). New
// backends (e.g. a different Wayland protocol) can be added without touching
// callers — see select.go.
package lock

import "time"

// Backend renders a full-screen, input-blocking lock for a fixed duration and
// returns when the duration elapses or Stop is called. Implementations are
// display-server specific; obtain one with Select.
type Backend interface {
	// Lock shows the lock and blocks until the duration elapses or Stop runs.
	Lock() error
	// Stop ends the lock early.
	Stop()
	// Name is a short backend identifier for logs (e.g. "x11", "wayland").
	Name() string
}

// Config holds configuration options common to every backend.
type Config struct {
	// Duration specifies how long the screen should be locked.
	Duration time.Duration
	// Message is displayed on the lock screen above the countdown.
	Message string
	// BackgroundColor is the lock screen background (0xRRGGBB).
	BackgroundColor uint32
	// BackgroundImage is the path to a PNG/JPG background image.
	BackgroundImage string
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		Duration:        60 * time.Second,
		Message:         "Screen locked",
		BackgroundColor: DefaultBackgroundColor, // Dark green
	}
}

// withDefaults fills in any zero-valued fields from DefaultConfig.
func withDefaults(cfg Config) Config {
	d := DefaultConfig()
	if cfg.Duration <= 0 {
		cfg.Duration = d.Duration
	}
	if cfg.Message == "" {
		cfg.Message = d.Message
	}
	if cfg.BackgroundColor == 0 {
		cfg.BackgroundColor = d.BackgroundColor
	}
	return cfg
}

// LockFor is a convenience that locks the screen for the given duration using
// the session-appropriate backend. It blocks until the timeout expires.
func LockFor(duration time.Duration) error {
	return LockWithMessage(duration, "")
}

// LockWithMessage locks the screen for the given duration with a custom message.
func LockWithMessage(duration time.Duration, message string) error {
	backend, err := Select(Config{Duration: duration, Message: message})
	if err != nil {
		return err
	}
	return backend.Lock()
}
