package accountability

import "context"

// SendTestEmail sends one clearly-marked test message through the mailer, so an
// operator can confirm accountability email is actually deliverable (Mailgun
// config + DNS + recipient) before relying on it. It carries no account data —
// it only proves the send path works. Used by `glockpeek -test-email <addr>`.
func SendTestEmail(mail Mailer, to string) error {
	const subject = "glockpeek test email"
	const text = "This is a test from glockpeek.\n\n" +
		"If you received it, accountability email delivery is working. Real " +
		"messages carry violation counts and liveness alerts for the account " +
		"you're supporting — never what was accessed.\n"
	const html = `<p>This is a <strong>test</strong> from glockpeek.</p>` +
		`<p>If you received it, accountability email delivery is working. Real ` +
		`messages carry violation counts and liveness alerts for the account ` +
		`you're supporting — never what was accessed.</p>`
	return mail.Send(context.Background(), to, subject, text, html)
}
