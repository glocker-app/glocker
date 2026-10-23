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
	// TermsURL is the operator's privacy & terms page. When set, the dashboard
	// links it from the sidebar and the signup form; empty shows no link.
	TermsURL string `yaml:"terms_url"`
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
	// Billing wires the hosted paid tier to a Merchant-of-Record. Disabled by
	// default; only a hosted instance selling subscriptions configures it.
	Billing BillingConfig `yaml:"billing"`
	// CDNBase, when set, serves the leaf static assets (styles.css, Chart.js) from
	// this base URL (content-hashed + SRI) instead of same-origin. Upload them with
	// `glockpeek -export-assets <dir>` + `aws s3 sync`. Empty = all assets local.
	CDNBase string `yaml:"cdn_base"`
}

// BillingConfig wires the hosted paid tier to a Merchant-of-Record (Polar). The
// MoR is the legal seller of record: it holds all payer PII (name/email/card)
// and handles global sales tax, while glockpeek only ever learns "is this
// account's subscription active?" via a signed webhook — so payment identity
// stays severed from usage data. Every field comes from the Polar dashboard.
type BillingConfig struct {
	// Enabled turns on the /api/billing endpoints. Off by default.
	Enabled bool `yaml:"enabled"`
	// Provider selects the Merchant-of-Record. Only "polar" is implemented.
	Provider string `yaml:"provider"`
	// APIToken authenticates server-to-Polar calls (creating checkout sessions).
	APIToken string `yaml:"api_token"`
	// WebhookSecret verifies incoming webhooks. Standard Webhooks format
	// (base64, typically "whsec_..."); create a fresh secret so it uses the
	// Standard Webhooks scheme rather than legacy Polar HMAC.
	WebhookSecret string `yaml:"webhook_secret"`
	// ProductID is the Polar subscription product customers check out against
	// (the annual plan). ProductIDMonthly is the optional monthly plan; empty
	// means only the annual plan is offered.
	ProductID        string `yaml:"product_id"`
	ProductIDMonthly string `yaml:"product_id_monthly"`
	// APIBase overrides the Polar API base URL. Empty = production
	// (https://api.polar.sh); set the sandbox (https://sandbox-api.polar.sh) for
	// testing.
	APIBase string `yaml:"api_base"`
	// PaidDeviceLimit is the device (API-token) cap granted while a subscription
	// is active. 0 (unset) resolves to unlimited — a paid tier capped at zero
	// devices would be nonsensical; set a positive number for a finite cap.
	PaidDeviceLimit int `yaml:"paid_device_limit"`
	// SuccessURL is where Polar redirects after a completed checkout. Empty falls
	// back to AppURL (the dashboard).
	SuccessURL string `yaml:"success_url"`
}

// DefaultPolarAPIBase is the production Polar API root used when
// BillingConfig.APIBase is empty.
const DefaultPolarAPIBase = "https://api.polar.sh"

// ResolvedAPIBase returns the configured Polar API base, defaulting to
// production when unset.
func (b BillingConfig) ResolvedAPIBase() string {
	if b.APIBase == "" {
		return DefaultPolarAPIBase
	}
	return b.APIBase
}

// ResolvedPaidLimit returns the device cap to grant an active subscriber. An
// unset (0) value means unlimited (-1), matching store.EffectiveDeviceLimit's
// "negative = no limit" convention.
func (b BillingConfig) ResolvedPaidLimit() int {
	if b.PaidDeviceLimit == 0 {
		return -1
	}
	return b.PaidDeviceLimit
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
