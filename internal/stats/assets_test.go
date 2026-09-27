package stats

import (
	"strings"
	"testing"
)

func TestAssetManifestLocal(t *testing.T) {
	buildAssetManifest("")
	for _, name := range []string{"styles.css", "chart.umd.min.js", "app.js", "e2e-crypto.js"} {
		a := assetOf(name)
		if a.URL != "/"+name {
			t.Errorf("%s local URL = %q, want /%s", name, a.URL, name)
		}
		if a.Integrity != "" {
			t.Errorf("%s local should have no SRI, got %q", name, a.Integrity)
		}
	}
}

func TestAssetManifestCDN(t *testing.T) {
	base := "https://cdn.example.com"
	buildAssetManifest(base)
	defer buildAssetManifest("") // reset for other tests

	// CDN-eligible leaf assets: content-hashed URL under the base + sha384 SRI.
	for _, name := range []string{"styles.css", "chart.umd.min.js"} {
		a := assetOf(name)
		if !strings.HasPrefix(a.URL, base+"/") {
			t.Errorf("%s CDN URL = %q, want prefix %s/", name, a.URL, base)
		}
		if a.URL == base+"/"+name {
			t.Errorf("%s CDN URL is not content-hashed: %q", name, a.URL)
		}
		if !strings.HasPrefix(a.Integrity, "sha384-") {
			t.Errorf("%s missing SRI, got %q", name, a.Integrity)
		}
	}

	// The E2E crypto stays same-origin even with a CDN configured.
	for _, name := range []string{"app.js", "e2e-crypto.js", "encryption-setup.js"} {
		a := assetOf(name)
		if a.URL != "/"+name || a.Integrity != "" {
			t.Errorf("crypto asset %s must stay local, got URL=%q SRI=%q", name, a.URL, a.Integrity)
		}
	}
}
