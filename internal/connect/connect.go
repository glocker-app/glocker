// Package connect implements `glocker -connect`: the one-time step that links an
// installed agent to a hosted glockpeek account's end-to-end encryption. It
// fetches the account's key vault (using the agent's ingest token), unwraps the
// master key with the user's passphrase, and writes the key to disk so the syncer
// can encrypt records before upload. It never sends the passphrase or the key
// anywhere — unwrapping happens locally.
package connect

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/e2e"
)

// Run fetches the account vault, unwraps it with passphrase, and writes the master
// key to the configured key file. Returns the key-file path on success. It is
// idempotent: re-running overwrites the key with the same value.
func Run(cfg *config.Config, passphrase []byte) (string, error) {
	url := strings.TrimRight(strings.TrimSpace(cfg.Sync.GlockpeekURL), "/")
	if url == "" {
		return "", fmt.Errorf("sync.glockpeek_url is not set — connect needs a hosted glockpeek to fetch your vault from")
	}
	if strings.TrimSpace(cfg.Sync.Token) == "" {
		return "", fmt.Errorf("sync.token is not set — connect authenticates to glockpeek with your device token")
	}
	keyPath := cfg.Sync.KeyFile()

	vaultJSON, err := fetchVault(url, cfg.Sync.Token)
	if err != nil {
		return "", err
	}

	var v e2e.Vault
	if err := json.Unmarshal(vaultJSON, &v); err != nil {
		return "", fmt.Errorf("vault from server is not valid JSON: %w", err)
	}
	mk, err := v.Open(passphrase)
	if err != nil {
		return "", fmt.Errorf("could not unlock the vault — wrong passphrase, or the vault is corrupt: %w", err)
	}
	if len(mk) != e2e.MasterKeyLen {
		return "", fmt.Errorf("unwrapped key is %d bytes, want %d", len(mk), e2e.MasterKeyLen)
	}

	if err := writeKey(keyPath, mk); err != nil {
		return "", err
	}
	return keyPath, nil
}

// fetchVault GETs the account's vault, authenticating with the ingest token. It
// maps the common failures to actionable messages.
func fetchVault(baseURL, token string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/vault", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach glockpeek at %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	case http.StatusNotFound:
		return nil, fmt.Errorf("no encryption vault on this account yet — set one up in the dashboard (Set up encryption) first")
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("glockpeek rejected the device token (check sync.token)")
	default:
		return nil, fmt.Errorf("glockpeek returned %s fetching the vault", resp.Status)
	}
}

// writeKey writes base64(mk) to path with 0600, creating the parent dir if needed.
// Written atomically (temp + rename) so a crash can't leave a half-written key.
func writeKey(path string, mk []byte) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create key dir %s: %w", dir, err)
		}
	}
	enc := base64.StdEncoding.EncodeToString(mk) + "\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(enc), 0o600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install key at %s: %w", path, err)
	}
	return nil
}
