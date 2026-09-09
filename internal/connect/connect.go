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

// Result reports what Run did with the key file.
type Result int

const (
	// Installed means the key was written (freshly, or replacing a different one
	// under force).
	Installed Result = iota
	// AlreadyMatches means a key was already present and equals the vault key, so
	// nothing was written — and, usefully, it confirms the installed key really is
	// this account's vault key (so already-synced ciphertext is decryptable).
	AlreadyMatches
)

// Run fetches the account vault, unwraps it with passphrase, and installs the
// master key at the configured key file.
//
// If a key is already present it is NOT blindly overwritten: an identical key is a
// no-op (AlreadyMatches), and a *different* key is refused unless force is set —
// clobbering it would make any data encrypted under the old key unreadable.
func Run(cfg *config.Config, passphrase []byte, force bool) (string, Result, error) {
	url := strings.TrimRight(strings.TrimSpace(cfg.Sync.GlockpeekURL), "/")
	if url == "" {
		return "", 0, fmt.Errorf("sync.glockpeek_url is not set — connect needs a hosted glockpeek to fetch your vault from")
	}
	if strings.TrimSpace(cfg.Sync.Token) == "" {
		return "", 0, fmt.Errorf("sync.token is not set — connect authenticates to glockpeek with your device token")
	}
	keyPath := cfg.Sync.KeyFile()

	vaultJSON, err := fetchVault(url, cfg.Sync.Token)
	if err != nil {
		return "", 0, err
	}

	var v e2e.Vault
	if err := json.Unmarshal(vaultJSON, &v); err != nil {
		return "", 0, fmt.Errorf("vault from server is not valid JSON: %w", err)
	}
	mk, err := v.Open(passphrase)
	if err != nil {
		return "", 0, fmt.Errorf("could not unlock the vault — wrong passphrase, or the vault is corrupt: %w", err)
	}
	if len(mk) != e2e.MasterKeyLen {
		return "", 0, fmt.Errorf("unwrapped key is %d bytes, want %d", len(mk), e2e.MasterKeyLen)
	}

	// Guard against clobbering an existing key: identical is a no-op; different is
	// refused unless forced (overwriting orphans data encrypted under the old key).
	if existing, rerr := os.ReadFile(keyPath); rerr == nil {
		if strings.TrimSpace(string(existing)) == base64.StdEncoding.EncodeToString(mk) {
			return keyPath, AlreadyMatches, nil
		}
		if !force {
			return keyPath, 0, fmt.Errorf("a different key is already installed at %s — overwriting it makes any data encrypted under the old key unreadable; re-run `glocker -connect -force` to replace it", keyPath)
		}
	}

	if err := writeKey(keyPath, mk); err != nil {
		return "", 0, err
	}
	return keyPath, Installed, nil
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
