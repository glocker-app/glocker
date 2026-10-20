package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// chromeExtensionID is fixed by the signing key (extensions/chrome-signing.pem,
// kept out of git): a new key means a new ID, so every release uses the same key.
const chromeExtensionID = "apcpegncpmcbncnaoabndpbgolnlmifa"

// chromeUpdateURL is the update manifest `make deploy-chrome-extension`
// publishes. Chrome fetches the extension from it, then re-checks it every few
// hours for a newer version.
var chromeUpdateURL = "https://glockerapp.com/chrome/update.xml"

// chromePolicyDirs are the managed-policy dirs of the Chromium-family browsers.
// Each reads every *.json in its dir, so glocker owns one file per browser and
// never edits anyone else's. Written for all of them, installed or not, so a
// browser installed later starts out covered.
var chromePolicyDirs = []string{
	"/etc/opt/chrome/policies/managed", // Google Chrome
	"/etc/chromium/policies/managed",   // Chromium
	"/etc/brave/policies/managed",      // Brave
	"/etc/opt/edge/policies/managed",   // Microsoft Edge
}

const chromePolicyFile = "glocker.json"

// chromePolicies force-install the extension and close the browser-level ways
// around it.
var chromePolicies = map[string]any{
	"ExtensionSettings": map[string]any{
		chromeExtensionID: map[string]any{
			"installation_mode": "force_installed",
			"update_url":        chromeUpdateURL,
		},
	},
	// Chrome can't force an extension into incognito, so incognito goes.
	"IncognitoModeAvailability": 1,
	// Guest windows run no extensions.
	"BrowserGuestModeEnabled": false,
	// Secure DNS resolves past /etc/hosts.
	"DnsOverHttpsMode": "off",
	// Brave's Tor windows resolve past /etc/hosts too; other browsers ignore it.
	"TorDisabled": true,
}

// CheckChromeUpdateURL confirms the update manifest is reachable and names
// glocker's extension. If it isn't, Chrome still has the policy and installs the
// extension once it can fetch it.
func CheckChromeUpdateURL() error {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(chromeUpdateURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", chromeUpdateURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), chromeExtensionID) {
		return fmt.Errorf("%s doesn't list extension %s", chromeUpdateURL, chromeExtensionID)
	}
	return nil
}

// InstallChromePolicies writes glocker's policy file into every Chromium-family
// policy dir.
func InstallChromePolicies() error {
	log.Println("Installing Chrome/Chromium policies...")
	if err := writeChromePolicies(chromePolicyDirs); err != nil {
		return err
	}
	log.Println("✓ Chrome/Chromium/Brave/Edge will force-install the extension on next launch")
	return nil
}

func writeChromePolicies(dirs []string) error {
	var errs []error
	for _, dir := range dirs {
		warnChromePolicyConflicts(dir)
		if err := writeJSONAtomic(filepath.Join(dir, chromePolicyFile), chromePolicies); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
		}
	}
	return errors.Join(errs...)
}

// warnChromePolicyConflicts flags other policy files in dir that set a policy
// glocker sets. Chrome doesn't merge a policy across files — one file's value
// wins outright — so either glocker's or theirs is silently dropped.
func warnChromePolicyConflicts(dir string) {
	for _, c := range chromePolicyConflicts(dir) {
		log.Printf("⚠️  %s also sets %s; only one file's value applies — merge it into %s or remove it",
			c.file, strings.Join(c.keys, ", "), chromePolicyFile)
	}
}

type chromePolicyConflict struct {
	file string
	keys []string
}

func chromePolicyConflicts(dir string) []chromePolicyConflict {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []chromePolicyConflict
	for _, f := range files {
		if filepath.Base(f) == chromePolicyFile {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var theirs map[string]json.RawMessage
		if json.Unmarshal(data, &theirs) != nil {
			continue
		}
		var keys []string
		for k := range theirs {
			if _, ours := chromePolicies[k]; ours {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			out = append(out, chromePolicyConflict{file: f, keys: keys})
		}
	}
	return out
}

// UninstallChromeExtension removes glocker's policy files; the browsers then
// remove the extension.
func UninstallChromeExtension() error {
	return removeChromePolicies(chromePolicyDirs)
}

// removeChromePolicies removes only glocker's file. The dirs stay: some ship with
// the browser package, and empty ones are harmless.
func removeChromePolicies(dirs []string) error {
	var errs []error
	for _, dir := range dirs {
		if err := removeIfExists(filepath.Join(dir, chromePolicyFile)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
