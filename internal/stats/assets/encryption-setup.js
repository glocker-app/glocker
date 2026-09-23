// Client-side crypto for the /settings/encryption page. The master key and
// passphrase never leave the browser — the server only ever receives the wrapped
// vault. Extracted from the old standalone setup-encryption.html so the page can
// be served through the SSR template at a clean URL.
import { generateMasterKey, createVault } from "./e2e-crypto.js";

const $ = (id) => document.getElementById(id);
const show = (id) => $(id).classList.remove("hidden");
const hide = (id) => $(id).classList.add("hidden");

// The vault's KDF is PBKDF2 (not memory-hard), so its strength rests on the
// passphrase. Gate NIST 800-63B style — length + a common-password blocklist, no
// composition rules — and steer toward a multi-word passphrase. Returns an error
// string, or null when acceptable.
const WEAK_PASSPHRASES = new Set([
  "password", "passw0rd", "passphrase", "letmein", "welcome", "qwerty", "dragon",
  "monkey", "iloveyou", "admin", "abc123", "changeme", "glocker", "secret",
  "111111", "000000", "123456", "1234567", "12345678", "123456789", "1234567890",
  "qwertyuiop", "correcthorsebatterystaple",
]);
function passphraseProblem(p) {
  if (p.length < 12) {
    return "Use at least 12 characters — four random words make a strong, memorable passphrase.";
  }
  if (WEAK_PASSPHRASES.has(p.toLowerCase().replace(/\s+/g, ""))) {
    return "That's a commonly used password — pick something unique.";
  }
  if (new Set(p).size < 5) {
    return "Too repetitive — add more variety, or use a few random words.";
  }
  return null;
}

async function init() {
  let me;
  try {
    const r = await fetch("/api/me", { credentials: "same-origin" });
    if (r.status === 401) { hide("loading"); show("need-login"); return; }
    me = await r.json();
  } catch {
    hide("loading"); show("need-login"); return;
  }
  hide("loading");
  if (me.e2e) { show("already"); } else { show("form-card"); }
}

$("reset-btn")?.addEventListener("click", () => { hide("already"); show("form-card"); });

$("form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = $("form-err");
  err.textContent = "";
  const p1 = $("pass").value, p2 = $("pass2").value;
  const weak = passphraseProblem(p1);
  if (weak) { err.textContent = weak; return; }
  if (p1 !== p2) { err.textContent = "The two passphrases don't match."; return; }

  const submit = $("submit");
  submit.disabled = true;
  submit.textContent = "Deriving key… (this takes a moment)";
  try {
    const mk = await generateMasterKey();
    const { vault, recoveryCode } = await createVault(mk, p1);
    const put = await fetch("/api/vault", {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(vault),
    });
    if (!put.ok) throw new Error("server rejected the vault (" + put.status + ")");
    $("recovery-code").textContent = recoveryCode;
    hide("form-card");
    show("done-card");
  } catch (ex) {
    err.textContent = "Setup failed: " + ex.message;
    submit.disabled = false;
    submit.textContent = "Create encryption key";
  }
});

$("copy-btn").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText($("recovery-code").textContent);
    $("copy-btn").textContent = "Copied ✓";
  } catch { /* clipboard may be blocked; the code is on screen anyway */ }
});

$("saved-check").addEventListener("change", (e) => { $("finish-btn").disabled = !e.target.checked; });
$("finish-btn").addEventListener("click", () => { window.location.href = "/"; });

init();
