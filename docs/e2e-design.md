# glockpeek end-to-end encryption — design

**Status:** design, not built. Needs a real cryptographer to review key handling
before any zero-knowledge claim is made or shipped. This document is the starting
point for that review and for implementation.

**Goal:** for the **hosted** glockpeek tier, the operator (us) cannot read a user's
content. Zero-knowledge is a *feature we sell*, not just hygiene.

---

## 1. Scope — what changes and what doesn't

- **Local mode is unaffected.** A desktop glockpeek binds loopback, has one
  implicit account, and reads a normal plaintext DB (or the `/var` logs). No
  encryption there — there's no untrusted operator. E2E is a **hosted-mode only**
  concern.
- **The agent keeps recording plaintext locally.** The `/var` logs stay the source
  of truth; enforcement never depends on the network or on crypto. Encryption
  happens only in the **syncer**, at the moment data leaves the machine.
- **The dashboard keeps showing the same views** — it just decrypts and aggregates
  in the browser instead of receiving server-computed stats.

## 2. Threat model

**Protected:** the *content* of a user's activity — URLs, window titles, keyword
hits, unblock reasons, domain/program names — is unreadable to the server operator,
to anyone who dumps the database, and to anyone who compromises the server at rest.

**NOT protected (honest limitations — must be disclosed in the privacy policy):**
- **Metadata:** record timestamps, per-type counts, sync times, upload volume, and
  client IP. E2E hides *what*, not *that you're active and how much*.
- **The web-crypto trust hole:** because we serve the dashboard JavaScript, a
  compromised/malicious server could serve JS that exfiltrates the key at decrypt
  time. Every browser-based E2E product (Proton, Bitwarden, Standard Notes) lives
  with this. Mitigations: OSS web client, reproducible builds, Subresource
  Integrity, and an optional pinned native/extension client for the paranoid. We
  **cannot** claim "we can never see it" without this asterisk.

## 3. Data classification

| Field | Disposition | Why |
|---|---|---|
| Account id | plaintext | scoping; it's identity, not content |
| Record timestamp (`TS`) | plaintext | ordering, retention, range queries, dedup |
| Record kind (violation/unblock/usage/lifecycle) | plaintext (coarse) | lets the dashboard fetch a type + range without pulling everything; encrypt too if we want zero type-leak |
| Liveness / heartbeat (`alive`) | **plaintext** | the watchdog's "went dark during committed hours" alert must work with no decryption |
| Lifecycle *type* (install/uninstall) | plaintext | drives the UNMANAGED/downtime overlay; keep computable server-side |
| URLs, window titles, keyword hits, unblock reasons, domain/program names, lifecycle *note* | **encrypted** | the sensitive content |

**Consequence:** the server can no longer aggregate content. All content-based
aggregation moves into the browser. (Liveness and unmanaged/downtime, being
plaintext, may still be computed server-side.)

## 4. Key management

Do **not** hand-roll. Do **not** put the key in a cookie (cookies are sent to the
server on every request — that would hand the server the key and defeat E2E).

- **Master key (MK):** a random 256-bit key generated once at registration. All
  content is encrypted under MK (or per-purpose subkeys via HKDF).
- **Passphrase-wrapped MK:** derive a key-encryption key `KEK = Argon2id(passphrase,
  salt)`; store `WrappedMK = AEAD(KEK, MK)` on the server. The server holds only the
  wrapped blob + salt — useless without the passphrase. It never sees MK or the
  passphrase.
- **Any device unlocks:** enter passphrase → derive KEK → fetch WrappedMK → unwrap →
  MK in memory. Portable across the agent (laptop) and the browser (any device).
- **Agent storage:** the agent stores MK in a root-only local file so it can encrypt
  each sync without prompting.
- **Browser storage:** MK lives in memory (session) or guarded IndexedDB — **never a
  cookie**, never sent to the server. Re-derived from the passphrase per session.
- **Recovery code:** a second wrapping `WrappedMK_Recovery = AEAD(recoveryKey, MK)`
  under a random, one-time, printable code — so "forgot passphrase" is not instant
  data loss. (The #1 support issue of every E2E product.)

**Key loss is softened by local-first:** losing the key loses only *server* data
that can't be regenerated. Because the agent keeps the `/var` logs, the user can
re-key and re-encrypt-and-upload from local logs.

## 5. Crypto primitives (candidates — cryptographer to confirm)

- **Record encryption:** AEAD with a per-record nonce — **AES-256-GCM** (Go
  `crypto/cipher`, browser **WebCrypto** — both stdlib-grade) or libsodium
  `secretbox` (XSalsa20-Poly1305).
- **KDF:** **Argon2id** (Go `golang.org/x/crypto/argon2`; browser needs a wasm
  argon2 — WebCrypto has no Argon2).
- **Single-library option:** **libsodium** (Go binding + libsodium.js) gives
  Argon2id + secretbox consistently on both sides; cost is a sizeable wasm blob.
- **Key-wrap shortcut:** **`age`** (Go + JS impls) has passphrase-based wrapping
  (scrypt) built in — use it for the MK wrap and AES-GCM for bulk records, so we
  don't hand-manage the KDF.

## 6. Server schema (hosted)

```
EncRecord {
  UserID   uint     // plaintext — scope
  TS       int64    // plaintext — ordering / retention / range
  Kind     string   // plaintext coarse type (or encrypted for zero type-leak)
  RecordID string   // client-assigned unique id → idempotent upsert without the
                     //   server reading content (replaces the url_hash unique key)
  Nonce    []byte
  Cipher   []byte   // AEAD(MK, canonical-plaintext-record)
}

KeyVault {           // one per account
  UserID            uint
  Salt              []byte
  WrappedMK         []byte   // AEAD(KEK from passphrase, MK)
  WrappedMK_Recovery []byte  // AEAD(recovery code, MK)
}
```
Heartbeat and lifecycle-type stay their own **plaintext** tables so liveness and
the UNMANAGED overlay still compute server-side.

## 7. Data flow

1. **Agent → server:** syncer reads `/var` logs, builds records, encrypts the
   content fields under MK, POSTs `{UserID, TS, Kind, RecordID, Nonce, Cipher}` to
   `/api/ingest`. Idempotent on `RecordID`.
2. **Browser → server:** dashboard fetches ciphertext blobs (by kind + range),
   plus WrappedMK + salt; user types passphrase → derive KEK → unwrap MK → decrypt
   blobs → **aggregate in JS** → render.

## 8. Feature impacts

- **Server-side aggregation → browser.** `internal/stats` stops computing content
  stats; it serves blobs. All the current `buildData` logic is reimplemented in JS.
  *(This is the bulk of the work and is done first, plaintext, as its own phase —
  see below.)*
- **Notification layer** can only alert on **metadata** (counts, downtime), never
  "you visited X" — consistent with "accountability stays content-free."
- **Server-side search is lost** (server can't read content). Search becomes a
  client-side filter over decrypted data.

## 9. Phased plan

- **Phase 1 — client-side aggregation (plaintext, no crypto).** Move all dashboard
  aggregation from the server (`internal/stats/buildData`) into the browser: the
  server returns raw records; the JS computes everything it currently receives
  pre-computed. Ships as a normal, non-crypto refactor and is independently
  useful/testable. **← doing this first.**
- **Phase 2 — encryption at rest in transit.** Encrypt content fields in the syncer;
  blob schema + ingest changes; browser decrypts blobs before the Phase-1
  aggregation runs. Key management minimal (single passphrase-derived key).
- **Phase 3 — key lifecycle + hardening.** Master-key-wrapped-under-passphrase,
  recovery code, key rotation, SRI/reproducible-build hardening, and the
  cryptographer review before any public zero-knowledge claim.

## 10. Open questions (for review)

- Encrypt `Kind` too, or accept the per-type count leak for query efficiency?
- Per-record blobs (full detail, more storage) vs agent-side encrypted rollups
  (compact, less detail)? Start per-record; revisit.
- HKDF subkeys per purpose vs MK directly?
- Nonce strategy (random 96-bit for GCM vs counter) and re-key cadence.
- AEAD associated data: bind `UserID|TS|Kind|RecordID` as AAD so the server can't
  splice a record into another account/time.
