package connect

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glocker/internal/config"
	"glocker/internal/e2e"
)

// vaultServer stands in for glockpeek's GET /api/vault: it returns the vault only
// to a caller presenting the right bearer token, 404 when there's no vault.
func vaultServer(t *testing.T, token, vaultJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/vault" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if vaultJSON == "" {
			http.Error(w, "no vault", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(vaultJSON))
	}))
}

func cfgFor(url, token, keyPath string) *config.Config {
	return &config.Config{Sync: config.SyncConfig{GlockpeekURL: url, Token: token, EncryptKeyFile: keyPath}}
}

func TestConnectFetchesUnwrapsAndWritesKey(t *testing.T) {
	pass := []byte("correct horse battery staple")
	mk, _ := e2e.GenerateMasterKey()
	v, _, err := e2e.WrapMasterKey(mk, pass)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	vj, _ := json.Marshal(v)
	srv := vaultServer(t, "tok-123", string(vj))
	defer srv.Close()

	keyPath := filepath.Join(t.TempDir(), "e2e.key")
	got, err := Run(cfgFor(srv.URL, "tok-123", keyPath), pass)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != keyPath {
		t.Errorf("returned path %q, want %q", got, keyPath)
	}

	// The file holds base64(masterKey), 0600, and round-trips to the same key.
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("key not base64: %v", err)
	}
	if string(dec) != string(mk) {
		t.Error("installed key != original master key")
	}
	if fi, _ := os.Stat(keyPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("key perms = %v, want 0600", fi.Mode().Perm())
	}
}

func TestConnectWrongPassphraseFails(t *testing.T) {
	mk, _ := e2e.GenerateMasterKey()
	v, _, _ := e2e.WrapMasterKey(mk, []byte("right passphrase"))
	vj, _ := json.Marshal(v)
	srv := vaultServer(t, "tok", string(vj))
	defer srv.Close()

	keyPath := filepath.Join(t.TempDir(), "e2e.key")
	if _, err := Run(cfgFor(srv.URL, "tok", keyPath), []byte("wrong passphrase")); err == nil {
		t.Fatal("expected failure with wrong passphrase")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Error("no key file should be written on failure")
	}
}

func TestConnectNoVaultIsActionable(t *testing.T) {
	srv := vaultServer(t, "tok", "") // 404: account has no vault yet
	defer srv.Close()
	_, err := Run(cfgFor(srv.URL, "tok", filepath.Join(t.TempDir(), "k")), []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "set one up in the dashboard") {
		t.Fatalf("want a 'set up a vault first' hint, got %v", err)
	}
}

func TestConnectRequiresSyncConfig(t *testing.T) {
	// No URL -> refuses before touching the network.
	if _, err := Run(cfgFor("", "tok", "/tmp/k"), []byte("x")); err == nil {
		t.Error("expected error when glockpeek_url is unset")
	}
	if _, err := Run(cfgFor("http://x", "", "/tmp/k"), []byte("x")); err == nil {
		t.Error("expected error when token is unset")
	}
}
