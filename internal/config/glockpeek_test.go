package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The glockpeek server settings moved to their own file. An agent config that
// still carries the old inline keys (glockpeek_listen, database, mail, …) must
// keep loading — the decode is lenient, so the now-unknown keys are ignored and
// the rest of the config (notably sync) is unaffected. This guards the upgrade
// path for already-installed configs.
func TestAgentConfigIgnoresLegacyGlockpeekKeys(t *testing.T) {
	legacy := []byte(`
dev: false
log_level: "info"
glockpeek_listen: "127.0.0.1:4317"
glockpeek_mode: "hosted"
glockpeek_secure_cookies: true
database:
  driver: "postgres"
  dsn: "host=x dbname=y"
mail:
  enabled: true
  api_key: "secret"
app_url: "https://my.glockerapp.com"
admin_email: "a@b.com"
captcha: true
sync:
  enabled: true
  glockpeek_url: "https://my.glockerapp.com"
`)
	cfg, err := ParseConfig(legacy, t.TempDir())
	if err != nil {
		t.Fatalf("legacy config must still load, got: %v", err)
	}
	if !cfg.Sync.Enabled || cfg.Sync.GlockpeekURL != "https://my.glockerapp.com" {
		t.Errorf("sync stanza not parsed through the legacy keys: %+v", cfg.Sync)
	}
}

// LoadGlockpeekConfig reads the top-level `glockpeek:` stanza; a missing file is
// the zero config (caller applies local/sqlite defaults), and a real file
// round-trips its fields.
func TestLoadGlockpeekConfig(t *testing.T) {
	// Missing file -> zero config, no error.
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if gp, err := loadGlockpeekConfigFrom(missing); err != nil || gp != (GlockpeekConfig{}) {
		t.Fatalf("missing file should be zero config, no error; got %+v err=%v", gp, err)
	}

	// A real file parses under the glockpeek: key.
	path := filepath.Join(t.TempDir(), "glockpeek.yaml")
	body := `glockpeek:
  mode: "hosted"
  listen: "127.0.0.1:4317"
  secure_cookies: true
  app_url: "https://my.glockerapp.com"
  admin_email: "a@b.com"
  captcha: true
  database:
    driver: "postgres"
    dsn: "host=x dbname=y"
  mail:
    enabled: true
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	gp, err := loadGlockpeekConfigFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if gp.Mode != "hosted" || gp.Listen != "127.0.0.1:4317" || !gp.SecureCookies ||
		gp.AppURL != "https://my.glockerapp.com" || gp.AdminEmail != "a@b.com" || !gp.Captcha ||
		gp.Database.Driver != "postgres" || !gp.Mail.Enabled {
		t.Errorf("glockpeek config not fully parsed: %+v", gp)
	}
}
