// Command glocklock is glocker's screen locker: a full-screen, timeout-based
// lock that unlocks automatically after a duration (it does not ask for a
// password). It selects an X11 or Wayland backend for the current session.
//
// Usage:
//
//	# Time-based lock using config defaults
//	glocklock
//
//	# Time-based lock with a custom duration and message
//	glocklock -duration 5m -message "Taking a break"
//
//	# Use a custom config file
//	glocklock -conf /path/to/config.yaml
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"glocker/internal/config"
	"glocker/internal/lock"
)

const defaultDuration = 1 * time.Minute

func main() {
	confPath := flag.String("conf", config.GlockerConfigFile, "Path to config file")
	duration := flag.Duration("duration", 0, "Lock duration (overrides config)")
	message := flag.String("message", "Screen locked", "Message to display")
	background := flag.String("background", "", "Background image path (overrides config)")
	flag.Parse()

	// Load config (errors are non-fatal, we just use defaults)
	cfg := loadConfig(*confPath)

	// Determine effective duration
	effectiveDuration := defaultDuration
	if cfg != nil && cfg.ViolationTracking.LockDuration != "" {
		if d, err := parseDuration(cfg.ViolationTracking.LockDuration); err == nil {
			effectiveDuration = d
		}
	}
	if *duration != 0 {
		effectiveDuration = *duration // Command-line flag overrides config
	}

	// Background image: config default, overridable by the flag.
	var backgroundImage string
	if cfg != nil && cfg.ViolationTracking.Background != "" {
		backgroundImage = cfg.ViolationTracking.Background
	}
	if *background != "" {
		backgroundImage = *background
	}

	// Pick the backend for the current session (X11 or Wayland) and lock. When
	// launched via `sudo -u <user>` the session env is stripped, so recover it
	// first — otherwise a Wayland desktop gets a bypassable X11 lock.
	lock.AdoptSessionEnv()
	locker, err := lock.Select(lock.Config{
		Duration:        effectiveDuration,
		Message:         *message,
		BackgroundImage: backgroundImage,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error selecting locker: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Locking screen for %v via %s backend...\n", effectiveDuration, locker.Name())
	fmt.Println("The screen will automatically unlock when the timer expires.")

	if err := locker.Lock(); err != nil {
		fmt.Fprintf(os.Stderr, "Error locking screen: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Screen unlocked.")
}

// loadConfig attempts to load the config file.
// Returns nil if the config cannot be loaded (file missing, invalid, etc.)
func loadConfig(path string) *config.Config {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return nil
	}
	return cfg
}

// parseDuration parses a duration string.
// Accepts Go duration format ("10s", "1m") or plain numbers (interpreted as seconds).
func parseDuration(s string) (time.Duration, error) {
	// First try Go's duration format
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Try parsing as plain number (seconds)
	if secs, err := strconv.Atoi(s); err == nil {
		return time.Duration(secs) * time.Second, nil
	}

	return 0, fmt.Errorf("invalid duration: %s", s)
}
