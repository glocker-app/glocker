package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"glocker/internal/utils"

	"github.com/google/renameio/v2"
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

// Firefox reads exactly one system policies file, and the machine may already use
// it for its own policies. So glocker doesn't own the file: it merges its keys in,
// records what they replaced, and on uninstall puts those values back.
var (
	firefoxPoliciesPath    = "/etc/firefox/policies/policies.json"
	firefoxPolicyStatePath = "/var/lib/glocker/firefox-policies.json"
)

const firefoxExtensionID = "glocker@nibrahim.net.in"

// firefoxPolicies are the top-level policies glocker sets, besides its own
// ExtensionSettings entry.
var firefoxPolicies = map[string]json.RawMessage{
	"DisablePrivateBrowsing": json.RawMessage(`true`),
}

// firefoxExtensionEntry force-installs the xpi. updates_disabled is per-extension
// (the global ExtensionUpdate policy would also freeze every other add-on).
var firefoxExtensionEntry = json.RawMessage(`{
  "installation_mode": "force_installed",
  "install_url": "file://` + installedXpiPath + `",
  "allowed_in_private_browsing": true,
  "updates_disabled": true
}`)

// firefoxPolicyState is what install recorded. Prior holds the value each key
// glocker set had before install; a key missing from it didn't exist and is
// deleted on uninstall. glocker's ExtensionSettings entry is keyed
// "ExtensionSettings/<id>".
type firefoxPolicyState struct {
	Prior map[string]json.RawMessage `json:"prior"`
}

const firefoxEntryKey = "ExtensionSettings/" + firefoxExtensionID

// InstallFirefoxExtension merges glocker's force-install policy into the system
// Firefox policies, for all Firefox users on the machine.
func InstallFirefoxExtension() error {
	log.Println("Installing Firefox extension via policies...")
	if err := mergeFirefoxPolicies(firefoxPoliciesPath, firefoxPolicyStatePath); err != nil {
		return err
	}
	log.Printf("✓ Firefox policies merged into %s", firefoxPoliciesPath)
	log.Println("✓ Firefox extension will be automatically installed on next Firefox launch")
	return nil
}

func mergeFirefoxPolicies(policiesPath, statePath string) error {
	top, policies, settings, err := readFirefoxPolicies(policiesPath)
	if err != nil {
		return err
	}

	// A reinstall keeps the state from the first install: the file now holds
	// glocker's own values, which aren't the ones to restore.
	state, err := readFirefoxPolicyState(statePath)
	if err != nil {
		return err
	}
	if state == nil {
		state = &firefoxPolicyState{Prior: map[string]json.RawMessage{}}
		if _, legacy := settings[firefoxExtensionID]; legacy {
			// Written by a glocker that overwrote the whole file: whatever was
			// there before is already gone, and its global ExtensionUpdate=false
			// is glocker's, not the machine's.
			dropLegacyExtensionUpdate(policies)
		} else {
			for name := range firefoxPolicies {
				if v, ok := policies[name]; ok {
					state.Prior[name] = v
				}
			}
			if v, ok := settings[firefoxExtensionID]; ok {
				state.Prior[firefoxEntryKey] = v
			}
		}
		// State before the policies: if the second write fails, uninstall
		// restores the priors onto an unchanged file, which is harmless.
		if err := writeJSONAtomic(statePath, state); err != nil {
			return fmt.Errorf("record prior Firefox policies: %w", err)
		}
	}

	for name, v := range firefoxPolicies {
		policies[name] = v
	}
	settings[firefoxExtensionID] = firefoxExtensionEntry
	if err := setRaw(policies, "ExtensionSettings", settings); err != nil {
		return err
	}
	if err := setRaw(top, "policies", policies); err != nil {
		return err
	}
	if err := writeJSONAtomic(policiesPath, top); err != nil {
		return fmt.Errorf("write %s: %w", policiesPath, err)
	}
	return nil
}

// UninstallFirefoxExtension removes glocker's Firefox policies (restoring what
// they replaced) and the XPI file.
func UninstallFirefoxExtension() error {
	// The XPI goes even if the policies can't be unmerged (an unparseable file is
	// left for its owner): a dangling install_url is harmless.
	policyErr := unmergeFirefoxPolicies(firefoxPoliciesPath, firefoxPolicyStatePath)

	// Remove XPI file
	xpiPath := "/usr/local/share/glocker/glocker.xpi"
	if err := os.Remove(xpiPath); err != nil && !os.IsNotExist(err) {
		return errors.Join(policyErr, fmt.Errorf("failed to remove XPI file: %w", err))
	}

	// Remove glocker directory if empty
	glockerDir := "/usr/local/share/glocker"
	if err := os.Remove(glockerDir); err != nil && !os.IsNotExist(err) {
		// Directory might not be empty, that's okay
	}

	return policyErr
}

func unmergeFirefoxPolicies(policiesPath, statePath string) error {
	if !fileExists(policiesPath) {
		return removeIfExists(statePath)
	}
	top, policies, settings, err := readFirefoxPolicies(policiesPath)
	if err != nil {
		return err
	}
	state, err := readFirefoxPolicyState(statePath)
	if err != nil {
		return err
	}
	if state == nil {
		// Installed by a glocker that kept no state: nothing to restore.
		state = &firefoxPolicyState{}
		dropLegacyExtensionUpdate(policies)
	}

	for name := range firefoxPolicies {
		restoreOrDelete(policies, name, state.Prior)
	}
	if v, ok := state.Prior[firefoxEntryKey]; ok {
		settings[firefoxExtensionID] = v
	} else {
		delete(settings, firefoxExtensionID)
	}
	if len(settings) == 0 {
		delete(policies, "ExtensionSettings")
	} else if err := setRaw(policies, "ExtensionSettings", settings); err != nil {
		return err
	}

	if len(policies) == 0 && len(top) == 1 {
		if err := os.Remove(policiesPath); err != nil {
			return fmt.Errorf("remove %s: %w", policiesPath, err)
		}
		os.Remove(filepath.Dir(policiesPath)) // only succeeds if empty
	} else {
		if err := setRaw(top, "policies", policies); err != nil {
			return err
		}
		if err := writeJSONAtomic(policiesPath, top); err != nil {
			return fmt.Errorf("write %s: %w", policiesPath, err)
		}
	}
	return removeIfExists(statePath)
}

// readFirefoxPolicies returns the file's top level, its "policies" object and
// that object's ExtensionSettings, each empty if absent. A file that isn't valid
// policy JSON is an error rather than something to overwrite: it's the machine's.
func readFirefoxPolicies(path string) (top, policies, settings map[string]json.RawMessage, err error) {
	top, policies, settings = map[string]json.RawMessage{}, map[string]json.RawMessage{}, map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return top, policies, settings, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	bad := func(what string, err error) error {
		return fmt.Errorf("%s: %s is not valid JSON (%v); fix or remove it, glocker won't overwrite it", path, what, err)
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, nil, nil, bad("file", err)
	}
	if raw, ok := top["policies"]; ok {
		if err := json.Unmarshal(raw, &policies); err != nil {
			return nil, nil, nil, bad(`"policies"`, err)
		}
	}
	if raw, ok := policies["ExtensionSettings"]; ok {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, nil, nil, bad(`"ExtensionSettings"`, err)
		}
	}
	return top, policies, settings, nil
}

// readFirefoxPolicyState returns nil when install recorded nothing.
func readFirefoxPolicyState(path string) (*firefoxPolicyState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s firefoxPolicyState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Prior == nil {
		s.Prior = map[string]json.RawMessage{}
	}
	return &s, nil
}

// dropLegacyExtensionUpdate removes the global ExtensionUpdate=false that
// glockers before the merge wrote.
func dropLegacyExtensionUpdate(policies map[string]json.RawMessage) {
	if string(policies["ExtensionUpdate"]) == "false" {
		delete(policies, "ExtensionUpdate")
	}
}

func restoreOrDelete(m map[string]json.RawMessage, name string, prior map[string]json.RawMessage) {
	if v, ok := prior[name]; ok {
		m[name] = v
	} else {
		delete(m, name)
	}
}

func setRaw(m map[string]json.RawMessage, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m[key] = raw
	return nil
}

// writeJSONAtomic writes v indented, via a temp file + rename, so a crash never
// leaves a truncated file behind.
func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return renameio.WriteFile(path, buf.Bytes(), 0o644)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
