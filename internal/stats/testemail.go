package stats

import (
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
	cfg, err := db.GetAccountabilityConfig(uid)
	if err != nil {
		http.Error(w, "no accountability config has synced yet for this account — reinstall the agent with the accountability stanza set", http.StatusUnprocessableEntity)
		return
	}
	buddies := cfg.Buddies()
	if len(buddies) == 0 {
		http.Error(w, "no buddy_emails in the synced accountability config — set them in conf.yaml and reinstall", http.StatusUnprocessableEntity)
		return
	}

	var sent int
	var firstErr error
	for _, to := range buddies {
		if err := accountability.SendTestEmail(mail, to); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sent++
	}
	if sent == 0 {
		http.Error(w, "send failed: "+firstErr.Error(), http.StatusBadGateway)
		return
	}
	fmt.Fprintf(w, "sent a test email to %d buddy address(es): %s\n", sent, strings.Join(buddies, ", "))
}
