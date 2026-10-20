package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func policyPaths(t *testing.T) (policies, state string) {
	dir := t.TempDir()
	return filepath.Join(dir, "etc", "policies.json"), filepath.Join(dir, "var", "firefox-policies.json")
}

func writePolicies(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readPolicies decodes the file's "policies" object into plain Go values.
func readPolicies(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("invalid JSON written: %v\n%s", err, data)
	}
	return top["policies"]
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func glockerEntry(p map[string]any) any {
	settings, _ := p["ExtensionSettings"].(map[string]any)
	return settings[firefoxExtensionID]
}

// No existing file: install creates one with glocker's policies; uninstall
// removes it again.
func TestFirefoxPolicies_FreshInstallAndUninstall(t *testing.T) {
	pol, st := policyPaths(t)
	if err := mergeFirefoxPolicies(pol, st); err != nil {
		t.Fatal(err)
	}
	p := readPolicies(t, pol)
	if p["DisablePrivateBrowsing"] != true {
		t.Errorf("DisablePrivateBrowsing = %v, want true", p["DisablePrivateBrowsing"])
	}
	if _, ok := p["ExtensionUpdate"]; ok {
		t.Error("global ExtensionUpdate must not be set")
	}
	entry, _ := glockerEntry(p).(map[string]any)
	if entry["installation_mode"] != "force_installed" || entry["updates_disabled"] != true {
		t.Errorf("glocker entry = %v", entry)
	}

	if err := unmergeFirefoxPolicies(pol, st); err != nil {
		t.Fatal(err)
	}
	if fileExists(pol) || fileExists(st) {
		t.Error("policies file and state should both be gone")
	}
}

// The machine's own policies survive install, and the values glocker replaced
// come back on uninstall.
func TestFirefoxPolicies_PreservesAndRestoresExisting(t *testing.T) {
	pol, st := policyPaths(t)
	orig := `{"policies": {
		"DisableTelemetry": true,
		"DisablePrivateBrowsing": false,
		"Homepage": {"URL": "https://example.com/?a=1&b=2"},
		"ExtensionSettings": {"other@ext": {"installation_mode": "allowed"}}
	}}`
	writePolicies(t, pol, orig)

	if err := mergeFirefoxPolicies(pol, st); err != nil {
		t.Fatal(err)
	}
	p := readPolicies(t, pol)
	if p["DisableTelemetry"] != true || p["DisablePrivateBrowsing"] != true {
		t.Errorf("merged policies wrong: %v", p)
	}
	if settings := p["ExtensionSettings"].(map[string]any); settings["other@ext"] == nil || settings[firefoxExtensionID] == nil {
		t.Errorf("ExtensionSettings should hold both extensions: %v", settings)
	}
	if data, _ := os.ReadFile(pol); strings.Contains(string(data), `&`) {
		t.Error("URLs should be written unescaped")
	}

	// A reinstall must not record glocker's own values as the ones to restore.
	if err := mergeFirefoxPolicies(pol, st); err != nil {
		t.Fatal(err)
	}

	if err := unmergeFirefoxPolicies(pol, st); err != nil {
		t.Fatal(err)
	}
	if got, want := readPolicies(t, pol), decode(t, orig)["policies"]; !reflect.DeepEqual(got, want) {
		t.Errorf("after uninstall:\n got %v\nwant %v", got, want)
	}
	if fileExists(st) {
		t.Error("state file should be removed")
	}
}

// A file left by a glocker that overwrote it has no state: install drops the old
// global ExtensionUpdate=false, uninstall removes glocker's keys.
func TestFirefoxPolicies_LegacyFile(t *testing.T) {
	pol, st := policyPaths(t)
	writePolicies(t, pol, `{"policies": {
		"ExtensionSettings": {"glocker@nibrahim.net.in": {"installation_mode": "force_installed"}},
		"ExtensionUpdate": false,
		"DisablePrivateBrowsing": true
	}}`)

	if err := mergeFirefoxPolicies(pol, st); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPolicies(t, pol)["ExtensionUpdate"]; ok {
		t.Error("legacy ExtensionUpdate should be dropped")
	}
	if err := unmergeFirefoxPolicies(pol, st); err != nil {
		t.Fatal(err)
	}
	if fileExists(pol) {
		t.Error("file held only glocker's policies; it should be removed")
	}
}

// Uninstall of a legacy install that never ran the new merge.
func TestFirefoxPolicies_LegacyUninstall(t *testing.T) {
	pol, st := policyPaths(t)
	writePolicies(t, pol, `{"policies": {
		"ExtensionSettings": {"glocker@nibrahim.net.in": {}},
		"ExtensionUpdate": false,
		"DisablePrivateBrowsing": true,
		"DisableTelemetry": true
	}}`)
	if err := unmergeFirefoxPolicies(pol, st); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"DisableTelemetry": true}
	if got := readPolicies(t, pol); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An unparseable file is the machine's: install refuses rather than clobber it.
func TestFirefoxPolicies_InvalidFileUntouched(t *testing.T) {
	pol, st := policyPaths(t)
	bad := `{"policies": {"DisableTelemetry": true,}}`
	writePolicies(t, pol, bad)

	if err := mergeFirefoxPolicies(pol, st); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
	if data, _ := os.ReadFile(pol); string(data) != bad {
		t.Error("invalid file was modified")
	}
	if fileExists(st) {
		t.Error("no state should be recorded")
	}
}

// Uninstall with the policies file already gone just clears the state.
func TestFirefoxPolicies_UninstallWithoutFile(t *testing.T) {
	pol, st := policyPaths(t)
	writePolicies(t, st, `{"prior": {}}`)
	if err := unmergeFirefoxPolicies(pol, st); err != nil {
		t.Fatal(err)
	}
	if fileExists(st) {
		t.Error("state should be removed")
	}
}
