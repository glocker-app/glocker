# E2E — notes for the README / user docs

Scratch material to fold into the README (and maybe a docs page) when we document
end-to-end encryption. **Not final copy** — notes to draw from. Keep adding as we build.

## Lead with the three paths

- **Free, client-only** — `make full-install`. Blocks + records locally. No account,
  no cloud, no encryption. Nothing leaves your machine.
- **Free, client + local glockpeek** — same install; a local dashboard reads a local
  DB over loopback. Still no encryption — there's no untrusted operator to hide from.
- **Paid, hosted** — install, then sign up. Your data syncs to the hosted service and
  is **end-to-end encrypted**: we (the operator) can't read it.

E2E is a **hosted-only** thing. The free paths never make a key, never sign up.

## What E2E protects — and doesn't (be honest)

- **Protected:** the *content* — the URLs you hit, window titles, keyword hits,
  domains, unblock reasons. Unreadable to us, to a database dump, or to anyone who
  breaks into the server.
- **Not hidden (metadata):** timestamps, how many events, when you synced, roughly
  how much. E2E hides *what*, not *that you were active and how much*.
- **The honest asterisk:** because we serve the dashboard's code, a malicious/compromised
  server could in theory serve code that grabs your key when you type your passphrase.
  Every browser-based E2E app lives with this (Proton, Bitwarden, …). Mitigations:
  open-source client, reproducible builds, SRI, optional native client. **Don't claim
  "we can never see it"** without that caveat — and a real crypto review.

## Passphrase + recovery code

- You pick a **passphrase**. It never leaves your device; it derives the key that
  unlocks your data.
- A one-time **recovery code** is shown at setup — write it down. It's a second way in
  if you forget the passphrase.
- **Lose both → your hosted data is unrecoverable, by design** — that's the flip side
  of "we can't read it." (Your local `/var` logs are still there, though.)

## Export / opt-out

- `glocker -export` dumps your data **decrypted** — the agent has both the key and the
  local plaintext logs. Take your data and leave anytime; no lock-in.

## Plain-language concept explanations (these landed well — reuse)

- **Salt** — a random per-vault tag mixed into your passphrase *before* it becomes a
  key, so two people with the same passphrase get different keys and precomputed
  guess-tables don't work. It's public (stored in the vault); the *passphrase* is the
  secret.
- **Nonce** ("number used once") — a fresh random value each time something is
  encrypted, so encrypting the same thing twice looks completely different and no
  patterns leak. Public, stored next to the ciphertext; must never repeat under one key.
- **Context-binding (`aad`)** — a tamper-proof label glued to a record ("user1 · web
  visit · #42") so a record can only open *as itself*; the server can't quietly
  re-file your history. **Not something a user sets** — the agent does it automatically.

## Dev-only tooling (not user-facing)

- `cmd/e2ekit` — a prototype CLI that exercises the crypto by hand (`create-key`,
  `wrap-key`, `unlock`, `recover`, `encrypt`, `decrypt`). **Not shipped to users**;
  it's the reference/validation harness for the real integration.

## Crypto choices (dev note)

- KDF **PBKDF2-HMAC-SHA256 @ 600k** (native, no wasm — Bitwarden's long-time default).
- Records + key-wrap **AES-256-GCM**; dedup **HMAC-SHA256**; recovery-code key **HKDF**.
- Full design + rationale: `docs/e2e-design.md`.
