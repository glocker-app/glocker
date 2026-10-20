package install

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func chromeDirs(t *testing.T) []string {
	root := t.TempDir()
	return []string{filepath.Join(root, "chrome", "managed"), filepath.Join(root, "chromium", "managed")}
}

// Install writes glocker.json into every dir (creating them), leaves other policy
// files alone, and uninstall removes only glocker's file.
func TestChromePolicies_WriteAndRemove(t *testing.T) {
	dirs := chromeDirs(t)
	other := filepath.Join(dirs[1], "corp.json")
	writePolicies(t, other, `{"HomepageLocation": "https://intranet"}`)

	if err := writeChromePolicies(dirs); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		data, err := os.ReadFile(filepath.Join(d, chromePolicyFile))
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		entry := got["ExtensionSettings"].(map[string]any)[chromeExtensionID].(map[string]any)
		if entry["installation_mode"] != "force_installed" || entry["update_url"] != chromeUpdateURL {
			t.Errorf("%s: extension entry = %v", d, entry)
		}
		if got["IncognitoModeAvailability"] != float64(1) || got["BrowserGuestModeEnabled"] != false || got["DnsOverHttpsMode"] != "off" {
			t.Errorf("%s: policies = %v", d, got)
		}
	}

	if err := removeChromePolicies(dirs); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if fileExists(filepath.Join(d, chromePolicyFile)) {
			t.Errorf("%s: glocker.json not removed", d)
		}
	}
	if !fileExists(other) {
		t.Error("another policy file was removed")
	}
	// Uninstalling again is a no-op.
	if err := removeChromePolicies(dirs); err != nil {
		t.Errorf("second uninstall: %v", err)
	}
}

func TestChromePolicyConflicts(t *testing.T) {
	dir := chromeDirs(t)[0]
	writePolicies(t, filepath.Join(dir, "a.json"), `{"ExtensionSettings": {}, "DnsOverHttpsMode": "secure", "HomepageLocation": "x"}`)
	writePolicies(t, filepath.Join(dir, "b.json"), `{"HomepageLocation": "x"}`)
	writePolicies(t, filepath.Join(dir, "broken.json"), `{`)
	writePolicies(t, filepath.Join(dir, chromePolicyFile), `{"ExtensionSettings": {}}`)

	got := chromePolicyConflicts(dir)
	want := []chromePolicyConflict{{file: filepath.Join(dir, "a.json"), keys: []string{"DnsOverHttpsMode", "ExtensionSettings"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conflicts = %+v, want %+v", got, want)
	}
}

func TestCheckChromeUpdateURL(t *testing.T) {
	body := "<app appid='" + chromeExtensionID + "'>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.xml":
			w.Write([]byte(body))
		case "/other.xml":
			w.Write([]byte("<app appid='someoneelse'>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := chromeUpdateURL
	defer func() { chromeUpdateURL = old }()
	for path, wantErr := range map[string]bool{"/ok.xml": false, "/other.xml": true, "/missing.xml": true} {
		chromeUpdateURL = srv.URL + path
		if err := CheckChromeUpdateURL(); (err != nil) != wantErr {
			t.Errorf("%s: err = %v, want error %v", path, err, wantErr)
		}
	}
}
