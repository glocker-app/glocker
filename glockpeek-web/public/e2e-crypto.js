// glockpeek end-to-end encryption — browser crypto.
//
// Native WebCrypto only (no wasm, no bundler): PBKDF2 + AES-GCM + HKDF. This MUST
// stay byte-for-byte compatible with the Go reference in internal/e2e — same
// PBKDF2 iterations, same AES-GCM layout, same AAD strings, same base32/base64.
// A vault made here opens with `e2ekit unlock`/`recover`, and vice-versa (that
// round-trip is the acceptance test). See docs/e2e-design.md.
//
// Works in the browser and in Node ≥20 (both expose globalThis.crypto.subtle).

// ── constants (mirror internal/e2e) ──────────────────────────────────────────
export const MASTER_KEY_LEN = 32;
const SALT_LEN = 16;
const NONCE_LEN = 12; // AES-GCM standard nonce
const DEFAULT_ITERATIONS = 600_000; // PBKDF2-HMAC-SHA256
const RECOVERY_CODE_BYTES = 20; // 160 bits
const AAD_MK_WRAP = "glocker/e2e:mkwrap:v1";
const AAD_RECOVERY_WRAP = "glocker/e2e:mkwrap-recovery:v1";
const RECOVERY_HKDF_INFO = "glocker/e2e:recovery-kek:v1";

const te = new TextEncoder();
const rand = (n) => crypto.getRandomValues(new Uint8Array(n));

// ── encodings (match Go: base64.StdEncoding, base32.StdEncoding no-padding) ───
function toB64(u8) {
  let s = "";
  for (let i = 0; i < u8.length; i++) s += String.fromCharCode(u8[i]);
  return btoa(s);
}
function fromB64(str) {
  const s = atob(str);
  const u8 = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) u8[i] = s.charCodeAt(i);
  return u8;
}

const B32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"; // RFC 4648
function toB32(u8) {
  let bits = 0, value = 0, out = "";
  for (let i = 0; i < u8.length; i++) {
    value = (value << 8) | u8[i];
    bits += 8;
    while (bits >= 5) { out += B32[(value >>> (bits - 5)) & 31]; bits -= 5; }
  }
  if (bits > 0) out += B32[(value << (5 - bits)) & 31];
  return out;
}
function fromB32(code) {
  // Forgiving of case, spaces, dashes — mirrors decodeRecoveryCode in Go.
  const clean = code.toUpperCase().replace(/[ -]/g, "");
  let bits = 0, value = 0;
  const out = [];
  for (const c of clean) {
    const idx = B32.indexOf(c);
    if (idx < 0) throw new Error("invalid recovery code");
    value = (value << 5) | idx;
    bits += 5;
    if (bits >= 8) { out.push((value >>> (bits - 8)) & 0xff); bits -= 8; }
  }
  return new Uint8Array(out);
}

// ── primitives ───────────────────────────────────────────────────────────────
export async function generateMasterKey() {
  return rand(MASTER_KEY_LEN);
}

async function deriveKEK(passphrase, salt, iterations) {
  const material = typeof passphrase === "string" ? te.encode(passphrase) : passphrase;
  const km = await crypto.subtle.importKey("raw", material, "PBKDF2", false, ["deriveBits"]);
  const bits = await crypto.subtle.deriveBits(
    { name: "PBKDF2", hash: "SHA-256", salt, iterations }, km, 256);
  return new Uint8Array(bits);
}

async function recoveryKEK(raw) {
  const km = await crypto.subtle.importKey("raw", raw, "HKDF", false, ["deriveBits"]);
  // Empty salt matches Go's nil salt: HMAC zero-pads either to the block size.
  const bits = await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt: new Uint8Array(0), info: te.encode(RECOVERY_HKDF_INFO) },
    km, 256);
  return new Uint8Array(bits);
}

// seal/open produce/consume Go's Box layout: a 12-byte nonce and ciphertext with
// the 16-byte GCM tag appended (WebCrypto and Go GCM agree on that layout).
async function seal(keyBytes, plaintext, aad) {
  const key = await crypto.subtle.importKey("raw", keyBytes, "AES-GCM", false, ["encrypt"]);
  const nonce = rand(NONCE_LEN);
  const ct = new Uint8Array(await crypto.subtle.encrypt(
    { name: "AES-GCM", iv: nonce, additionalData: aad, tagLength: 128 }, key, plaintext));
  return { nonce, ct };
}
async function open(keyBytes, box, aad) {
  const key = await crypto.subtle.importKey("raw", keyBytes, "AES-GCM", false, ["decrypt"]);
  return new Uint8Array(await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: box.nonce, additionalData: aad, tagLength: 128 }, key, box.ct));
}

// ── vault (mirrors WrapMasterKey / Open / OpenWithRecovery) ───────────────────
export async function createVault(mk, passphrase) {
  const salt = rand(SALT_LEN);
  const kek = await deriveKEK(passphrase, salt, DEFAULT_ITERATIONS);
  const wp = await seal(kek, mk, te.encode(AAD_MK_WRAP));

  const raw = rand(RECOVERY_CODE_BYTES);
  const recoveryCode = toB32(raw);
  const rk = await recoveryKEK(raw);
  const wr = await seal(rk, mk, te.encode(AAD_RECOVERY_WRAP));

  const vault = {
    version: 1,
    kdf: "pbkdf2-sha256",
    pbkdf2: { iterations: DEFAULT_ITERATIONS },
    salt: toB64(salt),
    wrappedMK: { nonce: toB64(wp.nonce), ct: toB64(wp.ct) },
    wrappedMKRecovery: { nonce: toB64(wr.nonce), ct: toB64(wr.ct) },
  };
  return { vault, recoveryCode };
}

export async function openVault(vault, passphrase) {
  const salt = fromB64(vault.salt);
  const kek = await deriveKEK(passphrase, salt, vault.pbkdf2.iterations);
  return open(kek, { nonce: fromB64(vault.wrappedMK.nonce), ct: fromB64(vault.wrappedMK.ct) },
    te.encode(AAD_MK_WRAP));
}

export async function openVaultWithRecovery(vault, code) {
  const rk = await recoveryKEK(fromB32(code));
  return open(rk, { nonce: fromB64(vault.wrappedMKRecovery.nonce), ct: fromB64(vault.wrappedMKRecovery.ct) },
    te.encode(AAD_RECOVERY_WRAP));
}

// ── records ──────────────────────────────────────────────────────────────────
// Box form ({nonce, ct} base64) — matches e2ekit's record.json.
export async function encryptRecord(mk, plaintext, aad = new Uint8Array(0)) {
  const box = await seal(mk, typeof plaintext === "string" ? te.encode(plaintext) : plaintext, aad);
  return { nonce: toB64(box.nonce), ct: toB64(box.ct) };
}
export async function decryptRecord(mk, box, aad = new Uint8Array(0)) {
  return open(mk, { nonce: fromB64(box.nonce), ct: fromB64(box.ct) }, aad);
}

// Field form (base64(nonce‖ct) single string) — matches the syncer's enc() wire,
// for decrypting the per-field values in /api/data later.
export async function decryptField(mk, b64NonceCt, aad) {
  const buf = fromB64(b64NonceCt);
  return open(mk, { nonce: buf.slice(0, NONCE_LEN), ct: buf.slice(NONCE_LEN) }, aad);
}

export const _internal = { toB64, fromB64, toB32, fromB32, deriveKEK }; // for tests
