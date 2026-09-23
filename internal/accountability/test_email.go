package accountability

import (
	"errors"
	"time"

	"glocker/internal/report"
)

// Sentinel errors so callers (the HTTP handler) can map to statuses without
// string-matching.
var (
	ErrNoConfig  = errors.New("no accountability config synced for this account")
	ErrNoBuddies = errors.New("no buddy_emails configured")
)

// SendDigestNow builds and sends this account's real periodic digest immediately —
// the same report, template (digestEmail), and recipients the scheduler uses on
// its cadence — bypassing only the due/quiet gate, and WITHOUT stamping the
// throttle clock (so it doesn't delay the next real digest). This is what
// `glocker -test-email` triggers, so the test exercises the live pathway instead
// of a bespoke message. Returns the buddy addresses it sent to.
func (n *Scheduler) SendDigestNow(uid uint) ([]string, error) {
	c, err := n.db.GetAccountabilityConfig(uid)
	if err != nil {
		return nil, ErrNoConfig
	}
	to := c.Buddies()
	if len(to) == 0 {
		return nil, ErrNoBuddies
	}
	window := 24 * time.Hour
	if c.DigestEveryHours > 0 {
		window = time.Duration(c.DigestEveryHours) * time.Hour
	}
	rep, err := report.BuildWindow(n.db, uid, n.now(), window)
	if err != nil {
		return nil, err
	}
	subj, text, html := digestEmail(rep, c.HighViolations, n.accountEmail(uid))
	if !n.send(to, "[test] "+subj, text, html) {
		return nil, errors.New("the mailer accepted no recipient")
	}
	return to, nil
}
