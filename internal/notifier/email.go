package notifier

import (
	"fmt"
	htmlpkg "html" // aliased: the funcs below use a named result called `html`
	"strings"
	"time"

	"glocker/internal/report"
)

// digestEmail renders the periodic accountability digest — counts + liveness only
// (no keywords/domains: the server can't read them under E2E). highViol is the
// account's high-violation threshold; >0 and crossed flags the subject.
func digestEmail(rep report.Report, highViol int) (subject, text, html string) {
	hours := int(rep.End.Sub(rep.Start).Round(time.Hour).Hours())
	heavy := highViol > 0 && rep.Violations >= highViol

	subject = fmt.Sprintf("glockpeek check-in — last %dh", hours)
	if heavy {
		subject = fmt.Sprintf("⚠ glockpeek — high activity (%d violations, last %dh)", rep.Violations, hours)
	}

	lines := []string{
		fmt.Sprintf("Accountability summary for the last %d hours:", hours),
		"",
		fmt.Sprintf("  • Blocked-keyword hits: %d", rep.Violations),
		fmt.Sprintf("  • Temporary unblocks:   %d", rep.Unblocks),
		fmt.Sprintf("  • Protection off for:   %d min", rep.UnmanagedMinutes),
	}
	if heavy {
		lines = append(lines, "", fmt.Sprintf("This crosses the alert threshold of %d.", highViol))
	}
	lines = append(lines,
		"",
		"These are counts only — glockpeek stores your records encrypted and",
		"cannot see which sites or apps were involved.",
	)
	text = strings.Join(lines, "\n") + "\n"
	html = digestHTML(rep, hours, heavy, highViol)
	return
}

// wentDarkEmail is the dead-man's-switch alert: the agent stopped reporting.
func wentDarkEmail(lastSeen, now time.Time) (subject, text, html string) {
	ago := durationWords(now.Sub(lastSeen))
	subject = "⚠ glockpeek — the agent went dark"
	text = fmt.Sprintf(
		"glocker stopped checking in.\n\n"+
			"Last heartbeat: %s (%s ago).\n\n"+
			"The agent may have been stopped, crashed, or uninstalled — protection "+
			"may be off. Worth checking in.\n",
		lastSeen.Format("2006-01-02 15:04 MST"), ago)
	html = fmt.Sprintf(
		"<p><strong>glocker stopped checking in.</strong></p>"+
			"<p>Last heartbeat: %s (%s ago).</p>"+
			"<p>The agent may have been stopped, crashed, or uninstalled — protection "+
			"may be off. Worth checking in.</p>",
		htmlpkg.EscapeString(lastSeen.Format("2006-01-02 15:04 MST")), htmlpkg.EscapeString(ago))
	return
}

func digestHTML(rep report.Report, hours int, heavy bool, highViol int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<p>Accountability summary for the last %d hours:</p><ul>", hours)
	fmt.Fprintf(&b, "<li>Blocked-keyword hits: <strong>%d</strong></li>", rep.Violations)
	fmt.Fprintf(&b, "<li>Temporary unblocks: <strong>%d</strong></li>", rep.Unblocks)
	fmt.Fprintf(&b, "<li>Protection off for: <strong>%d min</strong></li>", rep.UnmanagedMinutes)
	b.WriteString("</ul>")
	if heavy {
		fmt.Fprintf(&b, "<p><strong>This crosses the alert threshold of %d.</strong></p>", highViol)
	}
	b.WriteString("<p style=\"color:#666;font-size:.9em\">These are counts only — glockpeek stores your records encrypted and cannot see which sites or apps were involved.</p>")
	return b.String()
}

// (digest content is server-generated counts; no user input is interpolated, but
// the went-dark timestamps are escaped above out of habit.)

// durationWords renders a coarse "2h 5m"-style span.
func durationWords(d time.Duration) string {
	if d < time.Minute {
		return "under a minute"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}
