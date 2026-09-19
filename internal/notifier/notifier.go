// Package notifier is glockpeek's accountability-email sender. It runs server-side
// (glockpeek is the only party that can send when the agent is dead) and reads the
// per-account NotifyConfig synced from the agent's conf.yaml. Emails are
// content-free by necessity — under E2E glockpeek holds only ciphertext, so it can
// report counts and liveness ("14 violations, went dark 2h"), never *what* was
// accessed. Buddy-readable content is a separate multi-recipient-encryption
// feature.
package notifier

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"glocker/internal/report"
	"glocker/internal/store"
)

// Mailer is the slice of *mailer.Mailer the notifier needs (so tests can fake it).
type Mailer interface {
	Send(ctx context.Context, to, subject, textBody, htmlBody string) error
}

const (
	// darkAfter: an account counts as "dark" once its newest heartbeat is older
	// than this (a few missed heartbeats, not a single blip).
	darkAfter = 15 * time.Minute
	// tickEvery: how often the scheduler wakes to check due digests + alerts.
	tickEvery = 5 * time.Minute
)

// Notifier sends the digests and alerts.
type Notifier struct {
	db     *store.DB
	mail   Mailer
	appURL string
	now    func() time.Time // injectable for tests
}

// New builds a notifier. mail may be a disabled mailer (Send is then a no-op),
// in which case the scheduler runs but sends nothing.
func New(db *store.DB, mail Mailer, appURL string) *Notifier {
	return &Notifier{db: db, mail: mail, appURL: appURL, now: time.Now}
}

// Run loops forever, checking due work every tickEvery. Cheap — it only touches
// accounts that opted in. Start it in a goroutine.
func (n *Notifier) Run() {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	n.tick(n.now())
	for range t.C {
		n.tick(n.now())
	}
}

// tick does one pass over the opted-in accounts.
func (n *Notifier) tick(now time.Time) {
	cfgs, err := n.db.EnabledNotifyConfigs()
	if err != nil {
		slog.Warn("notifier: listing configs failed", "err", err)
		return
	}
	for _, c := range cfgs {
		n.process(c, now)
	}
}

// process handles one account's due alerts + digest.
func (n *Notifier) process(c store.NotifyConfig, now time.Time) {
	to := c.Buddies()
	if len(to) == 0 {
		return
	}
	nowMS := now.UnixMilli()

	// Went-dark alert — the dead-man's switch. Fire once per dark episode: we've
	// already alerted for this episode if LastWentDarkAt is newer than the most
	// recent heartbeat; a fresh heartbeat re-arms it.
	if c.WentDark {
		if last, ok := n.db.LatestHeartbeatTS(c.UserID); ok {
			dark := now.Sub(time.UnixMilli(last)) > darkAfter
			if dark && c.LastWentDarkAt <= last {
				subj, text, html := wentDarkEmail(time.UnixMilli(last), now)
				if n.send(to, subj, text, html) {
					_ = n.db.MarkWentDarkSent(c.UserID, nowMS)
				}
			}
		}
	}

	// Periodic digest over the cadence window (high violations flag the subject).
	if c.DigestEveryHours > 0 {
		window := time.Duration(c.DigestEveryHours) * time.Hour
		due := c.LastDigestAt == 0 || now.Sub(time.UnixMilli(c.LastDigestAt)) >= window
		if due {
			rep, err := report.BuildWindow(n.db, c.UserID, now, window)
			if err != nil {
				slog.Warn("notifier: building report failed", "user", c.UserID, "err", err)
				return
			}
			quiet := rep.Violations == 0 && rep.Unblocks == 0 && rep.UnmanagedMinutes == 0
			if !quiet || c.DigestWhenQuiet {
				subj, text, html := digestEmail(rep, c.HighViolations)
				n.send(to, subj, text, html)
			}
			// Stamp even when we skipped a quiet window, so the cadence clock
			// advances rather than firing every tick.
			_ = n.db.MarkDigestSent(c.UserID, nowMS)
		}
	}
}

// send emails every recipient; returns true if at least one send didn't error.
func (n *Notifier) send(to []string, subject, text, html string) bool {
	ok := false
	for _, addr := range to {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if err := n.mail.Send(context.Background(), addr, subject, text, html); err != nil {
			slog.Warn("notifier: send failed", "to", addr, "err", err)
			continue
		}
		ok = true
	}
	return ok
}
