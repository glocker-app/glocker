# glockpeek end-to-end encryption — design

**Status:** built (crypto core, agent encryption, vault endpoints, browser
setup/unlock/decrypt, `glocker -connect`, RecordID dedup). Recovery-code unlock has
no shipped path yet (§5). Needs a real cryptographer to review key handling
before any zero-knowledge claim is made or shipped.

> **Author TODO (Noufal):** understand these primitives properly before shipping —
> **PBKDF2** (and why iteration count matters / what "memory-hard" means and why
> Argon2 has it), **AES-256-GCM** (nonce, the auth tag, AAD/context-binding),
> **HMAC-SHA256** (why a *keyed* hash defeats the dictionary attack on the record
> id), **HKDF** (why the full-entropy recovery code needs no slow KDF). This design
> is AI-drafted; don't stake a zero-knowledge claim on trust in it.

**Goal:** for the **hosted** glockpeek tier, the operator (us) cannot read a user's
content. Zero-knowledge is a *feature we sell*, not just hygiene.

---

## 1. Deployment paths — E2E is Path-3 only

| Path | Setup | glockpeek | Sync | Encryption |
|---|---|---|---|---|
| **1. Client-only (free)** | `make full-install` | none | none | **none** — `/var` logs, plaintext |
| **2. Client + local glockpeek (free)** | `make full-install` | local, loopback | agent → `localhost:4317` | **none** — no untrusted operator |
| **3. Client + hosted (paid)** | install, then **sign up** | hosted | agent → hosted + token | **E2E on** |

The entire E2E feature is **additive and Path-3-gated**. Paths 1 & 2 never generate
a key, never make a vault, never sign up: `sync.encrypt: false` (it must be set explicitly whenever sync is on), there's no
`/etc/glocker/e2e.key`, nothing changes from today. The free experience is untouched.

**Hosted = E2E before the dashboard opens.** Registration does not create the vault.
On a hosted account with no vault (`!me.e2e`) the dashboard redirects to
`/settings/encryption`, where the browser creates it. The browser unlocks and
decrypts when `me.encrypted` (the latest ingest was marked encrypted). Cleartext
already on the server is handled by migration, not avoided: the browser seals any
cleartext user tags/rules on the next unlocked load, and `glocker -resync` re-pushes
records so the encrypted copies overwrite the stored ones. Operators can set
`require_encryption` to reject unencrypted batches outright (§7).

## 2. Threat model

**Protected:** the *content* of a user's activity — URLs, window titles, keyword
hits, unblock reasons, domain/program names — is unreadable to the server operator,
to a DB dump, and to server-at-rest compromise.

**NOT protected (must be disclosed in the privacy policy):**
- **Metadata:** record timestamps, per-type counts, record *structure* (which fields
  exist, roughly how long), sync times, upload volume, client IP. E2E hides *what*,
  not *that you're active and how much*.
- **The web-crypto trust hole:** because we serve the dashboard JS, a compromised
  server could serve JS that steals the key at decrypt time. Every browser-E2E
  product lives with this. Mitigate: OSS client, reproducible builds, Subresource
  Integrity, an optional pinned native client. We **cannot** claim "we can never see
  it" without this asterisk.

## 3. What's encrypted — per-field, in the SAME record shape

**Decision: encrypt each sensitive field in place; do NOT bundle a record into one
blob.** This keeps the wire format, the ingest API, the storage schema, and
`/api/data` **identical** between local and hosted — the server treats those fields
as opaque strings either way (the "content-blind" property Phase 1 established). A
field like `url` just holds `"https://reddit.com"` locally and `"<base64 nonce‖ct>"`
hosted. Local mode's payload is literally unchanged.

Trade-off accepted: the server sees each record's *structure* (that a `url`/`keyword`
field exists, roughly its length) — a minor metadata leak, and it already knows the
record's `kind`. The blob alternative hides structure but would change the record
shape and force local onto blobs too; not worth it.

| Record | Plaintext (metadata) | Encrypted (content) |
|---|---|---|
| violation | `ts`, `type` | `keyword`, `url`, `domain` |
| unblock | `ts`, `restoreTs` | `domain`, `reason` |
| lifecycle | `ts`, `type`, `reason` (gated enum) | `note` |
| usage | `ts`, `idleMs`, `windowCount` | `activeTitle`, `activeClass`, `activeInstance` |
| heartbeat | `ts`, `alive` | — |

Metadata stays clear because the server needs it: `ts` (ordering/range), `type`/`alive`
(the UNMANAGED + liveness computations that legitimately stay server-side).

## 4. Dedup — a KEYED record id (this is a crypto trap)

The idempotent re-send relies on a unique index that currently keys on content
(`keyword`, `url_hash`, `domain`). Under per-field encryption those are ciphertext
with a **fresh nonce each sync**, so every re-send looks new → **duplicates**. Fix:

- The agent computes **`RecordID = HMAC-SHA256(masterKey, canonical-plaintext-fields)`**
  from the plaintext *before* encrypting, and sends it in the clear. The unique index
  for **violation** and **unblock** is `(user, device, RecordID)`; `keyword`/`url`/`domain`
  become pure ciphertext payload. Lifecycle, usage and heartbeat keep their
  cleartext keys (`(user, device, ts, type)` / `(user, device, ts)`), which hold no
  sealed content. Without E2E (local mode) RecordID is a plain SHA-256 of the same
  parts.
- It's **deterministic** (same record → same id → dedup works across re-sends) and
  **keyed** (the server can't reverse it).

**Do NOT use a plain hash** (`sha256(url)` / `sha256(keyword)`): the content is
low-entropy and guessable, so `sha256("porn")` / `sha256("reddit.com")` is a two-line
dictionary attack — the operator would recover exactly what we encrypted. The HMAC
key (the master key, which the server never has) is what makes the id safe.

## 5. Key management

- **Master key (MK):** random 32 bytes (256 bits). Minted **in the browser** on
  `/settings/encryption` after signup (the dashboard redirects there until a vault exists).
- **Vault = passphrase-wrapped MK:** `KEK = PBKDF2-HMAC-SHA256(passphrase, salt, iters)`;
  `WrappedMK = AES-GCM(KEK, MK)`. Plus `WrappedMK_Recovery = AES-GCM(HKDF(recoveryCode), MK)`
  under a printed one-time code. The server stores only the vault (wrapped blobs +
  salt + params) — never MK or the passphrase.
- **Recovery code:** generated and shown once at setup, but **nothing shipped accepts it
  yet** — no browser or `-connect` recovery flow. Only the dev tool `cmd/e2ekit recover`
  (`Vault.OpenWithRecovery`) uses it.
- **Agent gets MK** via a one-time `sudo glocker -connect`: prompts the passphrase,
  fetches the vault (authed by the ingest token), unwraps MK and writes it to
  **`/etc/glocker/e2e.key`** (root, 0600). The `sync` stanza (`glockpeek_url`,
  `token`, `encrypt: true`) is set in the config beforehand; `-connect` doesn't edit it.
- **Browser gets MK** by re-deriving from the passphrase per session; held in memory
  (never a cookie, never localStorage of the raw key).
- **Fail closed:** if `sync.encrypt` is on but the key can't load, the syncer refuses to
  run rather than upload plaintext. *(Built.)*

## 6. Crypto primitives (settled)

- **Field encryption:** AES-256-GCM, 12-byte random nonce, `aad = ts|kind|field`
  (context-binding). Go `crypto/cipher`; browser `WebCrypto`. Wire form per field:
  **`base64(nonce ‖ ciphertext-with-tag)`**.
- **Other AADs:**
  - config snapshot (one sealed JSON blob): `<ts>|deviceconfig|config`
  - domain blocklist (gzipped, one blob): `deviceblocklist|<hash>`
  - registry fields sealed in the browser, static labels: `tag.namespace`,
    `tag.name`, `rule.program`, `rule.title`; wire form `enc:v1:` + base64(nonce ‖ ct)
  - vault wraps: `glocker/e2e:mkwrap:v1` (passphrase), `glocker/e2e:mkwrap-recovery:v1`
    (recovery code); recovery HKDF info `glocker/e2e:recovery-kek:v1`
- **KDF:** PBKDF2-HMAC-SHA256, **600,000 iterations**, 32-byte key, 16-byte salt
  (iterations stored in the vault, upgradable). **Native in both WebCrypto and the Go
  stdlib (`crypto/pbkdf2`) — no wasm, no vendoring.** Chosen over Argon2id to avoid a
  wasm dependency; the trade-off is that PBKDF2 isn't memory-hard (more
  GPU-parallelizable), which is a defensible "serious, not missile-codes" choice —
  it's Bitwarden's long-time default (600k) and used by 1Password/LastPass. The
  iteration count, not the algorithm, is what matters; keep it ≥ 600k. Argon2id
  (via a ~7 KB `@openpgpjs/argon2id` wasm) stays an option if we ever want
  memory-hardness — the vault stores its KDF name + params so a future switch is a
  per-vault upgrade.
- **Record id:** HMAC-SHA256(MK, canonical plaintext).
- **Recovery-code key:** HKDF-SHA256 (the code is full-entropy, so no slow KDF).
- libsodium and `age` were evaluated and rejected (too heavy / wrong shape).

## 7. Mode gating & the (near-zero) server change

- **Agent:** `sync.encrypt` bool + `sync.encrypt_key_file`. `enc()` is a pass-through when off
  (local byte-identical), else encrypts. *(Built.)*
- **Server:** stays a **dumb, key-blind store** — `handleIngest`/`handleData` never
  read a record's content. Storage columns already hold strings, so they take
  ciphertext unchanged. Server changes:
  - `/api/me`: `e2e` (a vault exists) and `encrypted` (the latest ingest was encrypted)
  - ingest and config batches carry an `encrypted` flag
  - `require_encryption` (glockpeek config): ingest/config reject unencrypted batches with 422
  - `GET /api/vault` (session or bearer token, so `-connect` can fetch it) and
    `PUT /api/vault` (session)
- **Browser:** `!me.e2e` → redirect to `/settings/encryption`. If `me.encrypted`, unlock
  (passphrase → MK) and decrypt each field before the (already client-side) aggregation.

## 8. Data flow

1. **Agent → server:** `syncer.build()` reads `/var` logs, computes `RecordID`
   (HMAC), encrypts the sensitive fields (`enc`), POSTs the same-shaped batch to
   `/api/ingest`. Idempotent on `(user, device, RecordID)` for violations/unblocks.
2. **Server:** stores/serves opaque records; computes only metadata things
   (unmanaged, liveness) that use plaintext fields.
3. **Browser → server:** fetch the vault + records; passphrase → KEK → unwrap MK →
   decrypt fields → run the Phase-1 aggregation → render.

## 9. Export / opt-out

The **agent** holds the master key *and* the plaintext logs, so decrypted export is
agent-native and needs no browser:
- `glocker -export` (not built yet) — dump the local `/var` logs (plaintext; no crypto), or
  decrypt-from-server for the full account.
- Browser "Export" (decrypt-in-browser → download) is a **secondary** convenience for
  agent-less devices — later.

## 10. Status

- **Built & tested:** `internal/e2e` (master key, vault wrap/unwrap, recovery code,
  `EncryptRecord`/`DecryptRecord`, `RecordID`); `cmd/e2ekit` CLI prototype; **Phase 1**
  (`/api/data` content-blind, aggregation client-side); agent syncer per-field
  encryption (`sync.encrypt`, `enc`, fail-closed) with RecordID dedup; vault endpoints
  (`internal/stats/vault.go`); browser setup (`assets/encryption-setup.js`) and
  unlock/decrypt (`assets/app.js`); `glocker -connect` (`internal/connect`).
- **Not built:** a recovery-code unlock path (§5); `glocker -export` (§9).
- Steps 1–6 below are done; step 7 is the manual end-to-end check.

## 11. Build steps (one at a time)

1. **Browser crypto ↔ Go interop.** JS `deriveKEK`/wrap/unwrap/recovery helpers
   (native WebCrypto — PBKDF2/AES-GCM/HMAC/HKDF, no wasm), validated by a **round-trip with `e2ekit`**: a
   vault made in the browser opens with `e2ekit unlock`/`recover`, and vice-versa.
   Isolates the one risky, review-worthy piece before any UI.
2. **Server vault storage.** `KeyVault` table + `PUT/GET /api/vault`; add the `e2e`
   bool to `/api/me`.
3. **Browser "Set up encryption" UI.** Passphrase → generate MK → wrap → `PUT /api/vault`
   → show recovery code once.
4. **Agent `-connect`.** Prompt passphrase → fetch vault → unwrap → write
   `/etc/glocker/e2e.key` (the `sync` stanza is configured separately).
5. **HMAC `RecordID` dedup (§4).** Agent computes it; store unique index → `(user,
   device, RecordID)` for violation/unblock; ingest/store updated.
6. **Browser unlock + decrypt.** Decrypt fields before the Phase-1 aggregation, gated
   on `me.encrypted`.
7. **End-to-end.** Sign up on `my.glockerapp.com`, set up encryption, connect the
   agent, watch ciphertext decrypt in the dashboard — i.e. be a paying customer on
   your own site.

## 12. Footguns

- **Never reuse a GCM nonce** under a key — fresh random 12 bytes every encryption.
- **Never persist MK/KEK wrong** — in-memory (browser), root-only file (agent); never
  a cookie or localStorage of the raw key.
- **AAD must match byte-for-byte** across Go and JS (`ts|kind|field` and the other
  AADs in §6), and the
  **PBKDF2 iteration count + salt** must match both sides. One mismatch → nothing
  decrypts.
- **Record id must be HMAC, not a plain hash** (§4) — else the dedup id leaks content.
- **Fail closed** if e2e is on but the key is missing — never fall back to plaintext.
