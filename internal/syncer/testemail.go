package syncer

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"glocker/internal/config"
)

// SendTestEmail asks glockpeek to send an accountability test email for this
// account (to the synced buddy_emails), verifying the whole mail path end to end.
// glockpeek is the sender — the agent holds no mail key — so `glocker -test-email`
// is really this request. It's a one-off POST, independent of the running sync
// loop, and returns glockpeek's human-readable result (or its error body).
func SendTestEmail(cfg *config.Config) (string, error) {
	if !cfg.Sync.Enabled {
		return "", fmt.Errorf("sync is disabled — accountability email is sent by glockpeek, so enable sync (and reinstall) first")
	}
	url := strings.TrimRight(orDefault(cfg.Sync.GlockpeekURL, config.DefaultGlockpeekURL), "/") + "/api/test-email"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	if cfg.Sync.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Sync.Token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("%s", msg)
	}
	return msg, nil
}
