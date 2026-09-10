<!--
DRAFT — for your review, NOT legal advice. Have a lawyer review before publishing,
especially the cross-border (GST/VAT/GDPR) and payment sections. Fill every
[BRACKETED] placeholder. Written to be accurate to how glockpeek actually works as
of this draft; if the architecture changes (e.g. encrypting app class/instance),
update the "What we can and cannot see" section.
-->

# Privacy Policy

**Effective date:** [DATE]
**Who we are:** [LEGAL ENTITY / YOUR NAME], [CITY], India ("we", "us"). Contact: [PRIVACY CONTACT EMAIL].

Glocker is a distraction-blocking tool for Linux. It has two parts:

- **The agent** (`glocker`) runs on *your* computer and does the blocking. It's the source of everything; it works fully offline.
- **glockpeek** is an optional hosted dashboard (glockerapp.com) that shows statistics about your own activity. You can also run glockpeek yourself, in which case none of your data reaches us at all.

This policy covers the **hosted glockpeek service**. If you self-host, we receive nothing.

## The short version

- glockpeek is **end-to-end encrypted**. The sensitive content of your activity — the URLs, keywords, window titles, and unblock reasons — is encrypted **on your device** with a key derived from your passphrase, *before* it is ever sent to us. **We store only ciphertext and cannot read it.** Only your browser, after you enter your passphrase, decrypts it.
- We **do not** sell your data, show ads, or run third-party trackers/analytics.
- We keep only what's needed to run the service, and you can export or delete it.

## What we collect

**Account data.** Your email address (your login identity) and a password stored only as a salted **argon2id hash** (never in plaintext). Device/API tokens used by your agent to sync are stored only as hashes. Login sessions are opaque random tokens in an httpOnly cookie.

**Activity records synced from your agent** (only if you enable sync to the hosted service):

- **Encrypted, unreadable by us** — the content fields: visited URLs, matched keywords, blocked domains, unblock reasons, and active-window titles. These are encrypted client-side (see "How the encryption works").
- **Stored unencrypted (metadata)** — timestamps; event types (e.g. "install", "uninstall", "web_access"); counts; idle time; window counts; the **application name/class** of the active window (e.g. `firefox-esr`); liveness heartbeats; and an opaque, keyed per-record identifier we use only for de-duplication (it is derived with your key and cannot be reversed by us to recover content). We are candid that this metadata reveals *patterns* (when you were active, which apps you ran, how often) even though it does not reveal the *content* of what you did.

**Your encryption vault.** A copy of your master key, wrapped ("locked") by your passphrase and by a recovery code. We store this only as opaque wrapped material — we cannot open it.

**Payment data.** Handled by our payment processor ([RAZORPAY / MERCHANT-OF-RECORD]); we do not store your card details. We retain billing records (plan, dates, amounts) as required for accounting/tax.

## How the encryption works (plain language)

When you set up encryption, your browser creates a random master key and locks it with your passphrase (and a one-time recovery code). Your agent uses that master key to encrypt each record's content fields before uploading. We receive and store only the encrypted blobs. To view your dashboard, your browser fetches the blobs and decrypts them locally after you enter your passphrase.

**This means: if you lose *both* your passphrase and your recovery code, your encrypted data is permanently unrecoverable — by you and by us.** We have no way to reset it. Please keep your recovery code somewhere safe.

## How we use data

- To operate the dashboard and sync for **your** account only. All data is scoped per-account.
- To send **transactional email** (account verification, and account/security notices) via [MAILGUN]. We do not send marketing email without your consent.
- To process payments and meet legal/accounting obligations (paid tier).

We do **not** use your data to build profiles, train models, target ads, or share with data brokers.

## Who we share with (sub-processors)

- **DigitalOcean** — hosting/infrastructure (servers, backups) in [REGION].
- **Mailgun** — transactional email delivery.
- **[RAZORPAY / MERCHANT-OF-RECORD]** — payment processing (paid tier only).

Each processes data only to provide its service to us. We may disclose data if required by law; because content is end-to-end encrypted, a lawful demand to us yields only ciphertext and metadata, not your content.

## Cookies

One httpOnly **session cookie** to keep you logged in. A small local setting for your chosen dashboard time-window. No advertising or cross-site tracking cookies.

## Data retention & deletion

- Activity records are retained until you delete them or close your account.
- On **account deletion**, we remove your account, tokens, vault, and synced records from the live database within [30] days; backups age out on their normal cycle (up to [30] days).
- You can **export** your data from the dashboard at any time.

## Your rights

You can access, export, correct, or delete your data. If you're in the EU/UK, you have GDPR/UK-GDPR rights (access, erasure, portability, objection); [LEGAL ENTITY] is the controller. To exercise any right, email [PRIVACY CONTACT EMAIL].

## Security

End-to-end encryption of content (AES-256-GCM under a PBKDF2-derived key); argon2id password hashing; hashed API tokens; TLS in transit; per-account data isolation; the option to require encrypted uploads. No system is perfectly secure, but the design ensures that even a full compromise of our servers exposes ciphertext, not your content.

## Children

Not directed to anyone under [16]. We don't knowingly collect their data.

## Changes

We'll post changes here and update the effective date; material changes will be notified by email.

## Contact

[LEGAL ENTITY], [ADDRESS], [CITY], India — [PRIVACY CONTACT EMAIL].
