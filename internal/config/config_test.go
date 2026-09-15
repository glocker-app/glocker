package config

import (
	"strings"
	"testing"
)

// sync.encrypt is compulsory when sync is enabled: an unset value is rejected so
// plaintext can't silently ship to a hosted glockpeek. An explicit true or false
// is accepted, and encrypt is irrelevant (not required) when sync is disabled.
func TestValidateConfig_EncryptCompulsoryWhenSyncEnabled(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	// Enabled + unset -> error.
	err := ValidateConfig(&Config{Sync: SyncConfig{Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "sync.encrypt is required") {
		t.Fatalf("expected 'sync.encrypt is required' error, got: %v", err)
	}

	// Enabled + explicit (true or false) -> ok.
	for _, v := range []bool{true, false} {
		if err := ValidateConfig(&Config{Sync: SyncConfig{Enabled: true, Encrypt: boolPtr(v)}}); err != nil {
			t.Errorf("explicit encrypt=%v should validate, got: %v", v, err)
		}
	}

	// Disabled + unset -> ok (encrypt irrelevant).
	if err := ValidateConfig(&Config{Sync: SyncConfig{Enabled: false}}); err != nil {
		t.Errorf("encrypt should not be required when sync is disabled, got: %v", err)
	}
}

// KeyFile applies the default only when unset; Encryption() is nil-safe.
func TestSyncConfigKeyFileAndEncryption(t *testing.T) {
	var unset SyncConfig
	if unset.Encryption() {
		t.Error("unset Encrypt must read as false")
	}
	if unset.KeyFile() != DefaultEncryptKeyFile {
		t.Errorf("unset key file should default to %q, got %q", DefaultEncryptKeyFile, unset.KeyFile())
	}
	custom := SyncConfig{EncryptKeyFile: "/tmp/k.key"}
	if custom.KeyFile() != "/tmp/k.key" {
		t.Errorf("explicit key file should win, got %q", custom.KeyFile())
	}
}

func TestValidateConfig_EmptyDomainName(t *testing.T) {
	cfg := &Config{
		Domains: DomainsConfig{List: []Domain{
			{Name: ""}, // Invalid: empty domain name
		}},
	}

	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("Expected validation error for empty domain name")
	}
	if err != ErrEmptyDomainName {
		t.Errorf("Expected ErrEmptyDomainName, got: %v", err)
	}
}

func TestValidateConfig_InvalidTimeFormat(t *testing.T) {
	cfg := &Config{
		Domains: DomainsConfig{List: []Domain{
			{
				Name: "example.com",
				BlockWindows: []TimeWindow{
					{
						Start: "25:00", // Invalid hour
						End:   "17:00",
						Days:  []string{"Mon"},
					},
				},
			},
		}},
	}

	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("Expected validation error for invalid time format")
	}
}

func TestValidateConfig_EmptyDays(t *testing.T) {
	cfg := &Config{
		Domains: DomainsConfig{List: []Domain{
			{
				Name: "example.com",
				BlockWindows: []TimeWindow{
					{
						Start: "09:00",
						End:   "17:00",
						Days:  []string{}, // Invalid: no days specified
					},
				},
			},
		}},
	}

	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("Expected validation error for empty days")
	}
}

func TestValidateConfig_ValidConfig(t *testing.T) {
	cfg := &Config{
		Domains: DomainsConfig{
			HostsPath: "/etc/hosts",
			List: []Domain{
				{
					Name: "example.com", // No time windows = always blocked by default
				},
				{
					Name: "reddit.com",
					BlockWindows: []TimeWindow{
						{
							Start: "09:00",
							End:   "17:00",
							Days:  []string{"Mon", "Tue", "Wed", "Thu", "Fri"},
						},
					},
				},
			},
		},
	}

	err := ValidateConfig(cfg)
	if err != nil {
		t.Fatalf("Expected valid config, got error: %v", err)
	}
}

func TestValidateConfig_SudoersEnabled(t *testing.T) {
	cfg := &Config{
		// Invalid: sudoers enabled but the top-level user is empty.
		Sudoers: SudoersConfig{Enabled: true},
	}

	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("Expected validation error for sudoers config")
	}
}

func TestValidateConfig_ForbiddenPrograms(t *testing.T) {
	cfg := &Config{
		Programs: ProgramsConfig{
			Enabled: true,
			List: []ForbiddenProgram{
				{Name: ""}, // Invalid: empty program name
			},
		},
	}

	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("Expected validation error for forbidden programs")
	}
	if err != ErrEmptyProgramName {
		t.Errorf("Expected ErrEmptyProgramName, got: %v", err)
	}
}

func TestIsValidTime(t *testing.T) {
	tests := []struct {
		time  string
		valid bool
	}{
		{"09:00", true},
		{"23:59", true},
		{"00:00", true},
		{"9:00", true},   // Also valid (Go's time.Parse accepts this)
		{"25:00", false}, // Invalid hour
		{"12:60", false}, // Invalid minute
		{"abc", false},   // Not a time
		{"", false},      // Empty string
	}

	for _, tt := range tests {
		t.Run(tt.time, func(t *testing.T) {
			result := isValidTime(tt.time)
			if result != tt.valid {
				t.Errorf("isValidTime(%q) = %v, want %v", tt.time, result, tt.valid)
			}
		})
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	// Note: This test assumes GlockerConfigFile doesn't exist or isn't accessible
	// In a real test, we'd use dependency injection or test fixtures
	_, err := LoadConfig()
	if err == nil {
		// If this passes, the config file actually exists, which is OK
		t.Skip("Config file exists, skipping file not found test")
	}
}

func TestSetupLogging(t *testing.T) {
	tests := []struct {
		logLevel string
		// Note: We can't easily test the actual log level set,
		// but we can ensure it doesn't panic
	}{
		{"debug"},
		{"info"},
		{"warn"},
		{"warning"},
		{"error"},
		{"invalid"}, // Should default to info
		{""},        // Should default to info
	}

	for _, tt := range tests {
		t.Run(tt.logLevel, func(t *testing.T) {
			cfg := &Config{LogLevel: tt.logLevel}
			// Should not panic
			SetupLogging(cfg)
		})
	}
}
