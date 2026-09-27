package stats

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// cdnAssets are the leaf, non-crypto static files safe to serve from a CDN: no
// inter-module imports, no secrets. The E2E crypto entry point and its imports
// (app.js, e2e-crypto.js, encryption-setup.js) are deliberately NOT here — they
// import each other by relative path and hold the zero-knowledge code, so they
// stay same-origin, served by glockpeek over its own TLS.
var cdnAssets = map[string]bool{
	"styles.css":       true,
	"chart.umd.min.js": true,
}

type assetInfo struct {
	URL       string // where the browser loads it: CDN when configured, else same-origin
	Integrity string // sha384 SRI, set only for cross-origin (CDN) assets
}

var (
	assetCDNBase  string // e.g. https://…cdn.digitaloceanspaces.com ; "" = everything local
	assetManifest = map[string]assetInfo{}
)

// hashedName is the content-addressed filename an asset gets on the CDN
// (cache-busting: new bytes → new name, so a deploy is never shadowed by stale
// edge cache). The same scheme is used by the runtime manifest and by
// ExportCDNAssets, so the HTML and the uploaded object always agree.
func hashedName(name string, b []byte) string {
	sum := sha256.Sum256(b)
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext) + "." + hex.EncodeToString(sum[:])[:12] + ext
}

// buildAssetManifest hashes the embedded assets once, at startup. CDN-eligible
// assets get a content-hashed URL under base + an SRI hash; everything else stays
// same-origin at /<name> with no SRI. base "" keeps every asset local (the
// default / local / dev behaviour).
func buildAssetManifest(base string) {
	assetCDNBase = strings.TrimRight(base, "/")
	assetManifest = map[string]assetInfo{}
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return
	}
	entries, _ := fs.ReadDir(sub, ".")
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if assetCDNBase == "" || !cdnAssets[name] {
			assetManifest[name] = assetInfo{URL: "/" + name}
			continue
		}
		b, err := fs.ReadFile(sub, name)
		if err != nil {
			assetManifest[name] = assetInfo{URL: "/" + name}
			continue
		}
		s384 := sha512.Sum384(b)
		assetManifest[name] = assetInfo{
			URL:       assetCDNBase + "/" + hashedName(name, b),
			Integrity: "sha384-" + base64.StdEncoding.EncodeToString(s384[:]),
		}
	}
}

// assetOf is the `asset` template func: {{ (asset "styles.css").URL }} +
// {{ (asset "styles.css").Integrity }}. Unknown names fall back to same-origin.
func assetOf(name string) assetInfo {
	if a, ok := assetManifest[name]; ok {
		return a
	}
	return assetInfo{URL: "/" + name}
}

// ExportCDNAssets writes the CDN-eligible assets into dir under their
// content-hashed names, for `aws s3 sync` to the bucket. Names match
// buildAssetManifest, so the served HTML points at exactly these objects.
func ExportCDNAssets(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return err
	}
	for name := range cdnAssets {
		b, err := fs.ReadFile(sub, name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, hashedName(name, b)), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
