package config

// Input validation for names accepted over the socket protocol. Runtime
// commands (-block, -block-app, -add-keyword) apply their changes in memory
// only; nothing here writes the config file, so the installed config
// (/etc/glocker/config.yaml) stays the single source of truth.

import (
	"fmt"
	"regexp"
)

// validDomainRe matches valid domain names: alphanumeric, dots, hyphens.
var validDomainRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?)*$`)

// ValidateDomainName checks that a domain name contains only safe characters.
func ValidateDomainName(name string) error {
	if name == "" {
		return ErrEmptyDomainName
	}
	if len(name) > 253 {
		return fmt.Errorf("domain name too long: %d characters (max 253)", len(name))
	}
	if !validDomainRe.MatchString(name) {
		return fmt.Errorf("invalid domain name %q: must contain only alphanumeric characters, dots, and hyphens", name)
	}
	return nil
}

// ValidateProgramName checks that a forbidden-program name is safe to embed in
// the socket protocol (colon/comma are delimiters). Program names are matched as
// a case-insensitive substring against each process's `comm`, so we only need to
// keep out control characters and our delimiters.
func ValidateProgramName(name string) error {
	if name == "" {
		return fmt.Errorf("program name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("program name too long: %d characters (max 64)", len(name))
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid program name %q: contains a control character", name)
		}
		if r == ':' || r == ',' {
			return fmt.Errorf("invalid program name %q: ':' and ',' are not allowed", name)
		}
	}
	return nil
}
