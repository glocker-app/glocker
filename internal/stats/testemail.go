package stats

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"glocker/internal/accountability"
)

// handleTestEmail lets an agent (via `glocker -test-email`) ask glockpeek to send
// a clearly-marked test to the account's synced buddy addresses, so an operator
// can confirm accountability email is actually deliverable before relying on it.
//
// Token-gated (an agent for this account), and it mails ONLY that account's own
// configured buddies — never an arbitrary address in the request — so it can't be
// turned into an open relay.
func handleTestEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// mail is a no-op when unconfigured (Send returns nil), so a test would
	// silently "succeed" — detect that here and say so instead.
	em, ok := mail.(interface{ Enabled() bool })
	if mail == nil || (ok && !em.Enabled()) {
		http.Error(w, "glockpeek mail is not configured — set the mail stanza in glockpeek.yaml", http.StatusServiceUnavailable)
		return
	}

	uid := userFrom(r).ID
	// Route through the REAL digest pathway (report → template → send the scheduler
	// uses), so the test exercises the live path rather than a bespoke message.
	to, err := accountability.New(db, mail, "").SendDigestNow(uid)
	switch {
	case errors.Is(err, accountability.ErrNoConfig):
		http.Error(w, "no accountability config has synced yet for this account — reinstall the agent with the accountability stanza set", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, accountability.ErrNoBuddies):
		http.Error(w, "no buddy_emails in the synced accountability config — set them in conf.yaml and reinstall", http.StatusUnprocessableEntity)
		return
	case err != nil:
		http.Error(w, "send failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	fmt.Fprintf(w, "sent the digest to %d buddy address(es): %s\n", len(to), strings.Join(to, ", "))
}
