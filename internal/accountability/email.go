package accountability

import (
	"fmt"
	htmlpkg "html" // aliased: the funcs below use a named result called `html`
	"strconv"
	"strings"
	"time"

	"glocker/internal/report"
)

// emailShell wraps body HTML in a simple, inline-styled card (email clients don't
// do <style>/external CSS reliably). `who` is the account's login email, shown in
// the header so a buddy watching several people knows whose machine this is about.
func emailShell(who, innerHTML string) string {
	sub := ""
	if who != "" {
		sub = fmt.Sprintf(`<div style="color:#6b7280;font-size:13px;margin-top:3px">for %s</div>`, htmlpkg.EscapeString(who))
	}
	return `<div style="background:#f4f5f7;padding:24px 12px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif">` +
		`<div style="max-width:480px;margin:0 auto;background:#ffffff;border:1px solid #e5e7eb;border-radius:10px;overflow:hidden">` +
		`<div style="padding:16px 22px;border-bottom:1px solid #eef0f2">` +
		`<div style="font-weight:800;font-size:16px;color:#111;letter-spacing:-.2px">glock<span style="color:#ff4d4d">peek</span></div>` +
		sub +
		`</div>` +
		`<div style="padding:20px 22px;color:#222;font-size:14px;line-height:1.55">` + innerHTML + `</div>` +
		`</div></div>`
}

// withWho appends the account email to a subject when known.
func withWho(s, who string) string {
	if who == "" {
		return s
	}
	return s + " — " + who
}

// digestEmail renders the periodic accountability digest — counts + liveness only
// (no keywords/domains: the server can't read them under E2E). highViol is the
// account's high-violation threshold; >0 and crossed flags the subject. who is the
// account's login email, shown in the subject + header.
func digestEmail(rep report.Report, highViol int, who string) (subject, text, html string) {
	hours := int(rep.End.Sub(rep.Start).Round(time.Hour).Hours())
	heavy := highViol > 0 && rep.Violations >= highViol

	if heavy {
		subject = withWho(fmt.Sprintf("⚠ glockpeek — high activity (%d violations, last %dh)", rep.Violations, hours), who)
	} else {
		subject = withWho(fmt.Sprintf("glockpeek check-in — last %dh", hours), who)
	}

	lines := []string{
		fmt.Sprintf("Accountability summary for the last %d hours:", hours),
		"",
		fmt.Sprintf("  • Blocked-keyword hits: %d", rep.Violations),
		fmt.Sprintf("  • Temporary unblocks:   %d", rep.Unblocks),
		fmt.Sprintf("  • Protection off for:   %d min", rep.UnmanagedMinutes),
	}
	if heavy {
		lines = append(lines, "", fmt.Sprintf("Crosses the alert threshold of %d.", highViol))
	}
	text = strings.Join(lines, "\n") + "\n"

	stat := func(label, val string) string {
		return `<tr>` +
			`<td style="padding:7px 0;border-bottom:1px solid #f1f2f4;color:#555">` + label + `</td>` +
			`<td style="padding:7px 0;border-bottom:1px solid #f1f2f4;text-align:right;font-weight:700;font-size:17px;color:#111">` + val + `</td>` +
			`</tr>`
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div style="font-weight:600;margin:0 0 10px">Last %d hours</div>`, hours)
	b.WriteString(`<table style="width:100%;border-collapse:collapse">`)
	b.WriteString(stat("Blocked-keyword hits", strconv.Itoa(rep.Violations)))
	b.WriteString(stat("Temporary unblocks", strconv.Itoa(rep.Unblocks)))
	b.WriteString(stat("Protection off", fmt.Sprintf("%d min", rep.UnmanagedMinutes)))
	b.WriteString(`</table>`)
	if heavy {
		fmt.Fprintf(&b, `<div style="margin-top:14px;padding:9px 12px;background:#fdecea;border-radius:6px;color:#b3261e;font-weight:600">Crosses the alert threshold of %d.</div>`, highViol)
	}
	html = emailShell(who, b.String())
	return
}

// wentDarkEmail is the dead-man's-switch alert: the agent stopped reporting.
func wentDarkEmail(lastSeen, now time.Time, who string) (subject, text, html string) {
	ago := durationWords(now.Sub(lastSeen))
	stamp := lastSeen.Format("2006-01-02 15:04 MST")
	subject = withWho("⚠ glockpeek — the agent went dark", who)

	text = fmt.Sprintf(
		"glocker stopped checking in.\n\nLast heartbeat: %s (%s ago).\n",
		stamp, ago)

	inner := `<div style="display:inline-block;padding:4px 11px;background:#fef3c7;color:#92400e;border-radius:6px;font-weight:600;font-size:13px">⚠ Agent went dark</div>` +
		`<p style="margin:15px 0 6px;font-weight:600;font-size:15px">glocker stopped checking in.</p>` +
		fmt.Sprintf(`<p style="margin:0;color:#555">Last heartbeat: <strong>%s</strong> (%s ago).</p>`,
			htmlpkg.EscapeString(stamp), htmlpkg.EscapeString(ago))
	html = emailShell(who, inner)
	return
}

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
