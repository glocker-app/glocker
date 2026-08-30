package install

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"glocker/internal/utils"
)

// extensionURL is where the signed, AMO-published xpi is served. A fresh clone
// (or any machine without a locally built artifact) fetches it from here, so the
// force-installed extension is a Mozilla-signed build release Firefox will
// actually load.
const extensionURL = "https://glockerapp.com/glocker.xpi"

// installedXpiPath is where the xpi is placed for the enterprise policy's
// file:// install_url to point at.
const installedXpiPath = "/usr/local/share/glocker/glocker.xpi"

// CreateFirefoxExtension puts the signed extension xpi at installedXpiPath, ready
// for InstallFirefoxExtension's policy to force-install it. It prefers a locally
// built signed artifact (author/dev box), otherwise downloads the signed build
// from the site.
//
// It never falls back to building an unsigned xpi: release Firefox refuses to
// install an unsigned extension even via enterprise policy, so an unsigned copy
// would only fail silently later. If no signed xpi can be obtained this returns
// an error; the caller reports it and continues (enforcement never depends on the
// extension).
func CreateFirefoxExtension() error {
	if err := os.MkdirAll(filepath.Dir(installedXpiPath), 0o755); err != nil {
		return fmt.Errorf("create xpi dir: %w", err)
	}

	// 1. A locally built, signed artifact wins (author/dev machine).
	if src, ok := localSignedXpi(); ok {
		if err := utils.CopyFile(src, installedXpiPath); err != nil {
			return fmt.Errorf("copy signed xpi %s: %w", src, err)
		}
		log.Printf("✓ Firefox extension installed from %s", src)
		return nil
	}

	// 2. Otherwise fetch the signed build published on the site.
	if err := downloadFile(extensionURL, installedXpiPath); err != nil {
		// 3. Last resort: an xpi already in place (manually downloaded per the
		//    README, or left by a previous install) lets an offline re-run work.
		if fileExists(installedXpiPath) {
			log.Printf("✓ Using existing Firefox extension at %s (download failed: %v)", installedXpiPath, err)
			return nil
		}
		return fmt.Errorf("fetch signed extension from %s: %w", extensionURL, err)
	}
	log.Printf("✓ Firefox extension downloaded from %s", extensionURL)
	return nil
}

// localSignedXpi returns the newest signed .xpi in web-ext-artifacts, if any.
func localSignedXpi() (string, bool) {
	dir := "extensions/firefox/web-ext-artifacts"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	newest, found := "", false
	var newestMod int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".xpi") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !found || info.ModTime().UnixNano() > newestMod {
			newest, newestMod, found = filepath.Join(dir, e.Name()), info.ModTime().UnixNano(), true
		}
	}
	return newest, found
}

// downloadFile fetches url to dest atomically (temp file + rename). It fails on a
// non-200 status or an empty body so a captive-portal HTML page can't masquerade
// as the xpi.
func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}

	tmp := dest + ".download"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n == 0 {
		os.Remove(tmp)
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return fmt.Errorf("empty download")
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// InstallFirefoxExtension installs the Firefox extension via Firefox policies.
// This forces the extension to be installed for all Firefox users on the system.
func InstallFirefoxExtension() error {
	log.Println("Installing Firefox extension via policies...")

	// Create Firefox policies directory
	policiesDir := "/etc/firefox/policies"
	if err := os.MkdirAll(policiesDir, 0755); err != nil {
		return fmt.Errorf("failed to create policies directory: %w", err)
	}

	// Create policies.json content
	policiesContent := `{
  "policies": {
    "ExtensionSettings": {
      "glocker@nibrahim.net.in": {
        "installation_mode": "force_installed",
        "install_url": "file:///usr/local/share/glocker/glocker.xpi",
        "allowed_in_private_browsing": true
      }
    },
    "ExtensionUpdate": false,
    "DisablePrivateBrowsing": true
  }
}`

	// Write policies.json file
	policiesPath := filepath.Join(policiesDir, "policies.json")
	if err := os.WriteFile(policiesPath, []byte(policiesContent), 0644); err != nil {
		return fmt.Errorf("failed to write policies.json: %w", err)
	}

	log.Printf("✓ Firefox policies installed at %s", policiesPath)
	log.Println("✓ Firefox extension will be automatically installed on next Firefox launch")
	return nil
}

// UninstallFirefoxExtension removes the Firefox extension policies and XPI file.
func UninstallFirefoxExtension() error {
	// Remove Firefox policies file
	policiesPath := "/etc/firefox/policies/policies.json"
	if err := os.Remove(policiesPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove policies.json: %w", err)
	}

	// Remove policies directory if empty
	policiesDir := "/etc/firefox/policies"
	if err := os.Remove(policiesDir); err != nil && !os.IsNotExist(err) {
		// Directory might not be empty, that's okay
	}

	// Remove XPI file
	xpiPath := "/usr/local/share/glocker/glocker.xpi"
	if err := os.Remove(xpiPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove XPI file: %w", err)
	}

	// Remove glocker directory if empty
	glockerDir := "/usr/local/share/glocker"
	if err := os.Remove(glockerDir); err != nil && !os.IsNotExist(err) {
		// Directory might not be empty, that's okay
	}

	return nil
}
