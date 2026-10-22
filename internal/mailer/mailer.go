// Package mailer sends transactional email via Mailgun, configured entirely from
// config.MailConfig (no hardcoded domain). glockpeek uses it for account
// verification. A disabled or unconfigured mailer is a safe no-op, so callers
// don't need to special-case it.
package mailer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mailgun/mailgun-go/v4"

	"glocker/internal/config"
)

// Mailer sends mail from a fixed From on a configured Mailgun domain.
type Mailer struct {
	mg         mailgun.Mailgun
	from       string // bare sender address (what From() reports)
	fromHeader string // From header used when sending, e.g. "glockpeek <noreply@…>"
	enabled    bool
}

// New builds a Mailer from config. It is disabled (Send is a no-op) unless
// Enabled with both a domain and an API key. From defaults to noreply@<domain>;
// region "eu" switches to Mailgun's EU API base.
func New(cfg config.MailConfig) *Mailer {
	if !cfg.Enabled || cfg.Domain == "" || cfg.APIKey == "" {
		return &Mailer{enabled: false}
	}
	mg := mailgun.NewMailgun(cfg.Domain, cfg.APIKey)
	if strings.EqualFold(cfg.Region, "eu") {
		mg.SetAPIBase(mailgun.APIBaseEU)
	}
	from := cfg.From
	if from == "" {
		from = "noreply@" + cfg.Domain
	}
	// Give the From header a display name so recipients see a name, not "noreply".
	// Honor a From already in "Name <addr>" form; otherwise wrap the bare address
	// with from_name (default "glockpeek"). From() still reports the bare address.
	fromHeader := from
	if !strings.Contains(from, "<") {
		name := cfg.FromName
		if name == "" {
			name = "glockpeek"
		}
		fromHeader = fmt.Sprintf("%s <%s>", name, from)
	}
	return &Mailer{mg: mg, from: from, fromHeader: fromHeader, enabled: true}
}

// Enabled reports whether mail will actually be sent.
func (m *Mailer) Enabled() bool { return m != nil && m.enabled }

// From returns the configured sender address.
func (m *Mailer) From() string { return m.from }

// Send delivers one email. htmlBody is optional (empty = plain text only).
// Returns nil immediately when the mailer is disabled.
func (m *Mailer) Send(ctx context.Context, to, subject, textBody, htmlBody string) error {
	if !m.Enabled() {
		return nil
	}
	msg := mailgun.NewMessage(m.fromHeader, subject, textBody, to)
	if htmlBody != "" {
		msg.SetHTML(htmlBody)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, _, err := m.mg.Send(ctx, msg); err != nil {
		return fmt.Errorf("mailer: send to %s: %w", to, err)
	}
	return nil
}
