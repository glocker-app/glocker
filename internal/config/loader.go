package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadConfig reads and parses the glocker configuration from the installed
// config file, resolving any !include directives. Returns an error if the file
// doesn't exist or cannot be parsed.
func LoadConfig() (*Config, error) {
	if _, err := os.Stat(GlockerConfigFile); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s\n\nThis usually means glocker is not properly installed.\nPlease check:\n  1. Is glocker installed? Run: ls -la %s\n  2. Is the glocker service running? Run: systemctl status glocker.service\n  3. If not installed, run: sudo glocker -install\n\nOriginal error: %w", GlockerConfigFile, InstallPath, err)
		}
		return nil, fmt.Errorf("config file access error at %s: %w", GlockerConfigFile, err)
	}

	slog.Debug("Loading config from external file", "path", GlockerConfigFile)
	return LoadFile(GlockerConfigFile)
}

// LoadFile reads and parses a glocker config file, resolving !include directives
// relative to the file's own directory. Use this instead of a raw
// yaml.Unmarshal so includes work from every caller (daemon, installer,
// configcheck).
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	return ParseConfig(data, filepath.Dir(path))
}

// ParseConfig parses config bytes, resolving !include directives against
// baseDir, and decodes the result into a Config.
func ParseConfig(data []byte, baseDir string) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	if err := resolveIncludes(&root, baseDir, 0); err != nil {
		return nil, err
	}
	// An empty file decodes to a zero Config, matching the old behaviour.
	if root.Kind == 0 {
		return &Config{}, nil
	}
	// Fail loud on a stale flat-format config. YAML decode silently ignores
	// unknown keys, so an old `enable_hosts: true` / `forbidden_programs:` would
	// vanish and blocking would quietly switch off. Reject those explicitly.
	if err := checkRetiredKeys(&root); err != nil {
		return nil, err
	}
	var cfg Config
	if err := root.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}
	return &cfg, nil
}

// retiredTopLevelKeys maps agent config keys removed in the nesting reorg to
// where they now live, so a stale config gets a pointed migration error instead
// of silently losing settings.
var retiredTopLevelKeys = map[string]string{
	"enable_hosts":              "domains.enabled",
	"hosts_path":                "domains.hosts_path",
	"kill_on_block":             "domains.kill_on_block",
	"unblocking":                "domains.unblocking",
	"enable_forbidden_programs": "removed — use programs.enabled",
	"forbidden_programs":        "programs (its `programs:` list is now `list:`)",
	"web_tracking":              "domains.blocked_access_command",
	"content_monitoring":        "content.monitoring",
	"extension_keywords":        "content.keywords",
	"panic_command":             "panic.command",
	"enforce_interval_seconds":  "removed — the enforcement cadence is a fixed 60s",
	"notification_command":      "removed — the agent no longer sends desktop notifications",
	"enable_self_healing":       "removed — redundant with the immutable-flag + sudo-deny controls",
	"tamper_detection":          "removed — the heartbeat gap on glockpeek is the real tamper signal",
}

// checkRetiredKeys errors if the top-level config mapping still carries any key
// from the pre-nesting flat format.
func checkRetiredKeys(root *yaml.Node) error {
	m := root
	if m.Kind == yaml.DocumentNode && len(m.Content) > 0 {
		m = m.Content[0]
	}
	if m.Kind != yaml.MappingNode {
		return nil
	}
	var found []string
	for i := 0; i+1 < len(m.Content); i += 2 {
		if newLoc, ok := retiredTopLevelKeys[m.Content[i].Value]; ok {
			found = append(found, fmt.Sprintf("  %q → %s", m.Content[i].Value, newLoc))
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("config uses retired top-level keys from the old flat format — move them to their nested homes (see conf/conf.yaml.sample):\n%s", strings.Join(found, "\n"))
	}
	return nil
}

// SetupLogging initializes the structured logging system based on the config.
// Sets the log level from config and configures the default slog logger.
func SetupLogging(cfg *Config) {
	var level slog.Level

	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	handler := slog.NewTextHandler(os.Stdout, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	slog.Debug("Logging initialized", "level", level.String())
}
