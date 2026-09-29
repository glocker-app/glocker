package config

import (
	"errors"
	"fmt"
	"time"
)

// Common validation errors
var (
	ErrInvalidTimeWindow  = errors.New("invalid time window")
	ErrEmptyDomainName    = errors.New("domain name cannot be empty")
	ErrEmptyProgramName   = errors.New("forbidden program name cannot be empty")
	ErrEmptyTimeWindowDay = errors.New("time window must specify at least one day")
)

// ValidateConfig validates the entire configuration structure.
// Returns an error if any configuration field is invalid or missing required values.
func ValidateConfig(config *Config) error {
	// The sample ships `user: "yourusername"` as a stub. Installing with it
	// unchanged would point sudoers restriction and desktop/usage tracking at a
	// user that doesn't exist — the single most-missed edit — so refuse it outright.
	if config.User == PlaceholderUser {
		return fmt.Errorf("the top-level `user` is still the sample placeholder %q — set it to your Linux login name (try `glocker -init`)", config.User)
	}

	// Validate domains
	for _, domain := range config.Domains.List {
		if domain.Name == "" {
			return ErrEmptyDomainName
		}
		for _, window := range domain.BlockWindows {
			if !isValidTime(window.Start) || !isValidTime(window.End) {
				return fmt.Errorf("invalid time format for domain %s block_windows (use HH:MM): %w", domain.Name, ErrInvalidTimeWindow)
			}
			if len(window.Days) == 0 {
				return fmt.Errorf("block window for %s: %w", domain.Name, ErrEmptyTimeWindowDay)
			}
		}
	}

	// Validate sudoers config
	if config.Sudoers.Enabled {
		if config.User == "" {
			return fmt.Errorf("the top-level `user` cannot be empty when sudoers is enabled")
		}
		if config.Sudoers.AllowedSudoersLine == "" {
			return fmt.Errorf("sudoers.allowed_sudoers_line cannot be empty when sudoers is enabled")
		}
		if config.Sudoers.BlockedSudoersLine == "" {
			return fmt.Errorf("sudoers.blocked_sudoers_line cannot be empty when sudoers is enabled")
		}
		for _, window := range config.Sudoers.AllowWindows {
			if !isValidTime(window.Start) || !isValidTime(window.End) {
				return fmt.Errorf("invalid time format in sudoers allow_windows (use HH:MM): %w", ErrInvalidTimeWindow)
			}
			if len(window.Days) == 0 {
				return fmt.Errorf("sudoers allow_windows entry: %w", ErrEmptyTimeWindowDay)
			}
		}
	}

	// Validate forbidden programs config
	if config.Programs.Enabled {
		for _, program := range config.Programs.List {
			if program.Name == "" {
				return ErrEmptyProgramName
			}
			for _, window := range program.KillWindows {
				if !isValidTime(window.Start) || !isValidTime(window.End) {
					return fmt.Errorf("invalid time format for forbidden program %s kill_windows (use HH:MM): %w", program.Name, ErrInvalidTimeWindow)
				}
				if len(window.Days) == 0 {
					return fmt.Errorf("kill window for forbidden program %s: %w", program.Name, ErrEmptyTimeWindowDay)
				}
			}
			for _, window := range program.AllowWindows {
				if !isValidTime(window.Start) || !isValidTime(window.End) {
					return fmt.Errorf("invalid time format for forbidden program %s allow_windows (use HH:MM): %w", program.Name, ErrInvalidTimeWindow)
				}
				if len(window.Days) == 0 {
					return fmt.Errorf("allow window for forbidden program %s: %w", program.Name, ErrEmptyTimeWindowDay)
				}
			}
		}
	}

	// Encryption is a deliberate, explicit choice: when sync is enabled you must
	// state sync.encrypt (true or false). Refusing an unset value prevents the
	// footgun of silently shipping plaintext to a hosted glockpeek because the key
	// was forgotten.
	if config.Sync.Enabled && config.Sync.Encrypt == nil {
		return fmt.Errorf("sync.encrypt is required when sync is enabled: set it to true (encrypt records before upload — required for a hosted glockpeek) or false (plaintext — only for a local/loopback glockpeek)")
	}

	return nil
}

// isValidTime checks if a time string is in valid HH:MM format.
func isValidTime(timeStr string) bool {
	_, err := time.Parse("15:04", timeStr)
	return err == nil
}
