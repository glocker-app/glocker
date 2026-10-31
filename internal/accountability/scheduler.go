// Package scheduler is glockpeek's accountability-email sender. It runs server-side
// (glockpeek is the only party that can send when the agent is dead) and reads the
// per-account AccountabilityConfig synced from the agent's conf.yaml. Emails are
// content-free by necessity — under E2E glockpeek holds only ciphertext, so it can
// report counts and liveness ("14 violations, went dark 2h"), never *what* was
// accessed. Buddy-readable content is a separate multi-recipient-encryption
// feature.
package accountability

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"time"

	"glocker/internal/config"
	"glocker/internal/report"
	"glocker/internal/store"
)

// Mailer is the slice of *mailer.Mailer the scheduler needs (so tests can fake it).
type Mailer interface {
	Send(ctx context.Context, to, subject, textBody, htmlBody string) error
}

const (
	// tickEvery: how often the scheduler wakes to check due digests + alerts.
	tickEvery = 5 * time.Minute
	// schedulerLease: how long a node holds fleet leadership. Longer than tickEvery
	// so the leader renews each tick and keeps it; a dead leader's lease expires
	// within this and another node takes over.
	schedulerLease = 3 * tickEvery
)

// darkThreshold is how long since the last heartbeat before an account counts as
// dark. glockdoc emits a heartbeat every HeartbeatMinutes (reported by the agent;
// default when unreported), so we allow two missed beats plus one scheduler tick
// of slack. A fixed threshold shorter than the interval would mark a healthy agent
// dark for most of every interval and re-alert on each beat.
func darkThreshold(c store.AccountabilityConfig) time.Duration {
	m := c.HeartbeatMinutes
	if m <= 0 {
		m = config.DefaultHeartbeatIntervalMinutes
	}
	return 2*time.Duration(m)*time.Minute + tickEvery
}

// Scheduler sends the digests and alerts.
type Scheduler struct {
	db     *store.DB
	mail   Mailer
	appURL string
	nodeID string           // this process's fleet identity (for the leader lease)
	now    func() time.Time // injectable for tests
	// requireAccess gates sending on the account's subscription (hosted/billing).
	// Off for self-hosted, where there's no subscription to check.
	requireAccess bool
}

// New builds a scheduler. mail may be a disabled mailer (Send is then a no-op),
// in which case the scheduler runs but sends nothing.
func New(db *store.DB, mail Mailer, appURL string, requireAccess bool) *Scheduler {
	host, _ := os.Hostname()
	return &Scheduler{db: db, mail: mail, appURL: appURL,
		nodeID: fmt.Sprintf("%s-%d", host, os.Getpid()), now: time.Now, requireAccess: requireAccess}
}

// accountEmail returns the account's login email to label the digest/alert, or ""
// if it can't be resolved (never blocks a send).
func (n *Scheduler) accountEmail(uid uint) string {
	if u, err := n.db.UserByID(uid); err == nil {
		return u.Email
	}
	return ""
}

// Run loops forever, checking due work every tickEvery. Cheap — it only touches
// accounts that opted in. Start it in a goroutine.
func (n *Scheduler) Run() {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	n.maybeTick()
	for range t.C {
		n.maybeTick()
	}
}

// maybeTick runs a pass only if this node currently holds the fleet-wide
// scheduler lease, so N nodes don't each fire the same digests/alerts. On sqlite
// (single-process local mode) the lease is always granted.
func (n *Scheduler) maybeTick() {
	leader, err := n.db.TryAcquireSchedulerLease(n.nodeID, schedulerLease)
	if err != nil {
		slog.Warn("scheduler: lease acquire failed; skipping this round", "err", err)
		return
	}
	if !leader {
		return // another node is the leader this round
	}
	n.tick(n.now())
}

// trialRemindBefore is how long before a no-card trial ends the "ending soon"
// reminder goes out.
const trialRemindBefore = 3 * 24 * time.Hour

// tick does one pass over the opted-in accounts, then the trial-reminder sweep.
func (n *Scheduler) tick(now time.Time) {
	cfgs, err := n.db.EnabledAccountabilityConfigs()
	if err != nil {
		slog.Warn("scheduler: listing configs failed", "err", err)
		return
	}
	for _, c := range cfgs {
		n.process(c, now)
	}
	// Trial reminders are a hosted-billing concern — skip them for self-hosted
	// instances, which have no subscription to nudge toward (requireAccess=false).
	if n.requireAccess {
		n.sweepTrials(now)
	}
}

// sweepTrials sends the two no-card-trial reminders — one as the trial nears its
// end, one once it has ended — each at most once, to the account's own email.
func (n *Scheduler) sweepTrials(now time.Time) {
	users, err := n.db.TrialReminderCandidates()
	if err != nil {
		slog.Warn("scheduler: listing trial candidates failed", "err", err)
		return
	}
	for _, u := range users {
		if u.TrialEndsAt == nil || u.Email == "" {
			continue
		}
		left := u.TrialEndsAt.Sub(now)
		switch {
		case left <= 0 && u.TrialRemindEndAt == nil:
			subj, text, html := trialReminderEmail(0, true, n.appURL)
			if err := n.mail.Send(context.Background(), u.Email, subj, text+textFooter, html); err == nil {
				_ = n.db.MarkTrialRemindEnd(u.ID, now)
			} else {
				slog.Warn("scheduler: trial-ended email failed", "user", u.ID, "err", err)
			}
		case left > 0 && left <= trialRemindBefore && u.TrialRemindSoonAt == nil:
			days := int(math.Ceil(left.Hours() / 24))
			subj, text, html := trialReminderEmail(days, false, n.appURL)
			if err := n.mail.Send(context.Background(), u.Email, subj, text+textFooter, html); err == nil {
				_ = n.db.MarkTrialRemindSoon(u.ID, now)
			} else {
				slog.Warn("scheduler: trial-ending email failed", "user", u.ID, "err", err)
			}
		}
	}
}

// process handles one account's due alerts + digest.
func (n *Scheduler) process(c store.AccountabilityConfig, now time.Time) {
	to := c.Buddies()
	if len(to) == 0 {
		return
	}
	// Don't send for an account without an active subscription (hosted): a
	// lapsed/abusive account must not keep burning the sending domain's reputation
	// and quota. Off for self-hosted (requireAccess=false).
	if n.requireAccess {
		if u, err := n.db.UserByID(c.UserID); err != nil || !u.HasAccess() {
			return
		}
	}
	nowMS := now.UnixMilli()
	who := n.accountEmail(c.UserID) // identify whose machine, for the subject/header

	// Went-dark alert — the dead-man's switch. Fire once per dark episode: we've
	// already alerted for this episode if LastWentDarkAt is newer than the most
	// recent heartbeat; a fresh heartbeat re-arms it.
	if c.WentDark {
		if last, ok := n.db.LatestHeartbeatTS(c.UserID); ok {
			dark := now.Sub(time.UnixMilli(last)) > darkThreshold(c)
			if dark && c.LastWentDarkAt <= last {
				subj, text, html := wentDarkEmail(time.UnixMilli(last), now, who)
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
				slog.Warn("scheduler: building report failed", "user", c.UserID, "err", err)
				return
			}
			quiet := rep.Violations == 0 && rep.Unblocks == 0 && rep.UnmanagedMinutes == 0
			if !quiet || c.DigestWhenQuiet {
				subj, text, html := digestEmail(rep, c.HighViolations, who)
				n.send(to, subj, text, html)
			}
			// Stamp even when we skipped a quiet window, so the cadence clock
			// advances rather than firing every tick.
			_ = n.db.MarkDigestSent(c.UserID, nowMS)
		}
	}
}

// send emails every recipient; returns true if at least one send didn't error.
func (n *Scheduler) send(to []string, subject, text, html string) bool {
	ok := false
	for _, addr := range to {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if err := n.mail.Send(context.Background(), addr, subject, text+textFooter, html); err != nil {
			slog.Warn("scheduler: send failed", "to", addr, "err", err)
			continue
		}
		ok = true
	}
	return ok
}
