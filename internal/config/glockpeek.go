package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// GlockpeekConfigFile is where the standalone glockpeek dashboard reads its own
// configuration — deliberately separate from the agent's config. The agent never
// reads this file; glockpeek never reads the agent's. Keeping them apart means
// each machine's file holds only what that machine actually runs: a hosted
// glockpeek box has no agent settings, an agent laptop has no server settings.
const GlockpeekConfigFile = "/etc/glocker/glockpeek.yaml"

const (
	// DefaultDatabaseDriver / DefaultDatabaseDSN are glockpeek's store defaults
	// when the database stanza is unset: an sqlite file under /var/lib (mutable
	// state, not /etc). Swap to "postgres" + a connection-string DSN for a hosted
	// instance.
	DefaultDatabaseDriver = "sqlite"
	DefaultDatabaseDSN    = "/var/lib/glocker/glockpeek.db"

	// GlockpeekMode values (see GlockpeekConfig.Mode). Empty defaults to local.
	GlockpeekModeLocal  = "local"
	GlockpeekModeHosted = "hosted"
)

// GlockpeekConfig is the dashboard server's configuration — every setting only
// the glockpeek process uses. Loaded from GlockpeekConfigFile under a single
// top-level `glockpeek:` key.
type GlockpeekConfig struct {
	// Mode selects how the dashboard runs:
	//   local  - personal desktop. Binds loopback only (unreachable from other
	//            hosts), no login/registration, and ingest is open to same-machine
	//            clients (no token).
	//   hosted - shared/remote instance: per-account logins + ingest tokens +
	//            isolation. Bind address honored as configured.
	// Empty defaults to local.
	Mode string `yaml:"mode"`
	// Listen is the address the process binds. Behind a TLS-terminating proxy this
	// is typically loopback (e.g. 127.0.0.1:4317). Empty falls back to the default
	// port. In local mode the bind is forced to loopback regardless.
	Listen string `yaml:"listen"`
	// SecureCookies marks the dashboard's session cookie Secure — set true when
	// served over HTTPS, including behind a TLS-terminating proxy. Leave false for
	// plain-http local use.
	SecureCookies bool `yaml:"secure_cookies"`
	// AppURL is the dashboard's public base URL (e.g. https://my.glockerapp.com),
	// used to build absolute links in verification emails. No trailing slash. This
	// is the address the world reaches, distinct from Listen (the internal bind).
	AppURL string `yaml:"app_url"`
	// AdminEmail names the account granted user-management powers in the dashboard.
	// Hosted mode only; empty means no admin account.
	AdminEmail string `yaml:"admin_email"`
	// Captcha turns on the proof-of-work captcha on the signup endpoint (hosted).
	Captcha bool `yaml:"captcha"`
	// RequireEncryption, when true, makes the ingest endpoint REJECT any batch not
	// marked encrypted (the agent stamps the batch from its sync.encrypt setting).
	// A server-side backstop so a misconfigured agent can't silently record
	// cleartext. Default false (accept both), so turning it on is an explicit
	// opt-in.
	RequireEncryption bool `yaml:"require_encryption"`
	// Database selects the store backend: sqlite locally, postgres for a hosted
	// instance. Empty fields fall back to the Default* values above.
	Database DatabaseConfig `yaml:"database"`
	// Mail is outbound transactional email (account verification), used in hosted
	// mode.
	Mail MailConfig `yaml:"mail"`
}

// glockpeekFile is the on-disk wrapper: a single top-level `glockpeek:` stanza.
type glockpeekFile struct {
	Glockpeek GlockpeekConfig `yaml:"glockpeek"`
}

// LoadGlockpeekConfig reads GlockpeekConfigFile. A missing file is not an error —
// it returns the zero config, and the caller applies defaults (local mode,
// sqlite), which is the sensible personal-desktop fallback. Real read/parse
// errors are returned so a malformed file is noticed rather than silently
// defaulted.
func LoadGlockpeekConfig() (GlockpeekConfig, error) {
	return loadGlockpeekConfigFrom(GlockpeekConfigFile)
}

func loadGlockpeekConfigFrom(path string) (GlockpeekConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return GlockpeekConfig{}, nil
		}
		return GlockpeekConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var f glockpeekFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return GlockpeekConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return f.Glockpeek, nil
}
