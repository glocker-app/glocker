# Known bypasses (no root required)

Every mechanism in glocker is defeatable by someone with root, and that's by
design — the escape hatches (`-uninstall`, `-unblock`, `-extend`) exist so the
system stays a friction device rather than a prison. This document tracks the
narrower and more damaging class: **bypasses that need no root at all**, are
near-zero effort, and leave nothing in the accountability log.

Anything here that turns into actual work belongs in `ROADMAP.md`; this file is
the reference list, not a status tracker.

## 1. DNS-over-HTTPS

`/etc/hosts` is consulted by glibc's resolver. A browser doing its own DoH never
touches it. Firefox `network.trr.mode=3` and Chrome's "Use secure DNS" are both
a checkbox in Settings.

`internal/install/firefox.go` writes `/etc/firefox/policies/policies.json` to
install the extension, but sets **no `DNSOverHTTPS` policy and no locked
`Preferences`**. For Chrome-family browsers, `internal/install/chrome.go` writes a
managed policy with `DnsOverHttpsMode: "off"` (Chrome, Chromium, Brave, Edge).

**Status:** open for Firefox, closed for Chrome-family browsers. The largest hole
— it voids the entire hosts layer in seconds.
**Close it with:** `"DNSOverHTTPS": {"Enabled": false, "Locked": true}` in the
Firefox policy.

## 2. Subdomains

`internal/enforcement/hosts.go` emits only `domain` and `www.domain` (four lines:
v4/v6 × bare/www). `old.reddit.com`, `m.youtube.com`, `i.redd.it` all resolve.

**Status:** open. Structural to hosts-file blocking — a hosts file cannot
wildcard.
**Close it with:** either an explicit subdomain list per domain in the config, or
the local-resolver approach (see "The structural fix" below).

## 3. SOCKS proxy with remote DNS

Tor Browser, or `ssh -D 8080` plus a browser proxy setting. Name resolution
happens at the far end, so the hosts file is never consulted.

**Status:** mitigatable today via `programs.list` — add `tor-browser`,
`mullvad-browser` etc. so they're killed on sight. See caveat in item 5: the
match is on process name, so this holds only against the honest-mistake case,
not against someone who renames the binary.
**Residual:** `ssh -D` can't be blocked this way without blocking ssh itself.

## 4. Mirrors, frontends, readers

archive.today, nitter-style frontends, RSS, text-mode proxies, translate
gateways. No amount of DNS blocking enumerates these.

**Status:** partially covered by the extension's content/URL keyword monitoring,
which is the right layer for it. Only works in browsers running the extension.

## 5. Renaming a forbidden binary

`internal/monitoring/forbidden.go` substring-matches on `comm` from `ps`.
`cp /usr/bin/steam ~/.cache/x && ~/.cache/x` is invisible to it.

**Status:** open, and it undercuts item 3 — every program restriction inherits
this weakness.
**Close it with:** match on the resolved `/proc/<pid>/exe` inode or path in
addition to `comm`, so a copy elsewhere still resolves to a known binary. Won't
stop a rebuild-from-source, but raises the cost from `cp` to something
deliberate.

## 6. `LD_PRELOAD` shim, or a sandboxed `/etc`

A preloaded `getaddrinfo` override, or a Flatpak/Snap browser that sees its own
`/etc`.

**Status:** open. Low prevalence, high effort to close, and anyone doing this has
clearly decided — deliberately out of scope for now.

## 7. Unprivileged mount namespace

`unshare -Urm` followed by a bind-mount of a clean hosts file. The daemon's own
view of `/etc/hosts` stays pristine, so nothing trips, and no checksum changes.

**Status:** open, and **unverified** — needs testing on Debian 13 to confirm
whether unprivileged userns is enabled (it usually is). If it is, this is a
complete, silent, no-root bypass of the hosts layer.
**Close it with:** `kernel.unprivileged_userns_clone=0` / the equivalent
`user.max_user_namespaces` sysctl, as a precheck finding rather than something
glocker enforces.

## 8. A second device

Your phone. No software on this laptop addresses this.

**Status:** out of scope for the agent; the accountability/reporting side
(glockpeek) is the only lever, and only if the phone reports in too.

## Fixes, by what they cost

The same eight items cut by what it takes to close them. Nothing here is
scheduled — it's so the cheap wins aren't lost among the structural ones.

### Config only — works today

| Item | Fix |
|---|---|
| 1 (partial) | Add browsers glocker writes no policy for (Vivaldi, Opera, …) to `programs.list`. Doesn't touch Firefox DoH, but closes "use the other browser with secure DNS". |
| 2 | List `old.reddit.com`, `m.youtube.com` etc. as their own `domains.list` entries. Tedious and unbounded, but available now. |
| 3 | `programs.list` with `tor-browser`, `mullvad-browser`. Subject to the item-5 caveat. |
| 4 | `content.keywords.url_keywords` — `nitter`, `archive.`, `redd.it`, `piped`. Extension browsers only, but the right layer for a set nobody can enumerate. |

### Needs implementing

| Item | Change | Size |
|---|---|---|
| 1 | `"DNSOverHTTPS": {"Enabled": false, "Locked": true}` in the policy JSON `internal/install/firefox.go` already writes (Chrome-family is done). | Small — best value on this page |
| 5 | Match `/proc/<pid>/exe` (resolved path or inode) alongside `comm` in `internal/monitoring/forbidden.go`. Makes every config-only program restriction above actually stick. | Small–medium |
| 2 | A `subdomains:` field on `Domain`, or prefix expansion from a common list. Caps the problem rather than solving it — hosts files can't wildcard. | Medium |
| 1, 3, 7 | The structural fix below. | v2 |

### Detectable by `precheck`

All read-only and file-based, which is what the package already does:

- **`doh-firefox`** — parse `/etc/firefox/policies/policies.json` plus each
  profile's `prefs.js`/`user.js` for `network.trr.mode`. OPEN if DoH is on, or
  merely unlocked.
- **`doh-chrome`** — check `/etc/opt/chrome/policies/managed/` for
  `DnsOverHttpsMode`.
- **`alt-browsers`** — scan `PATH`, `/var/lib/flatpak`, `~/.local/share` for
  browsers absent from `programs.list`. Tells you what to add, which beats
  guessing.
- **`userns`** — read `/proc/sys/kernel/unprivileged_userns_clone` and
  `/proc/sys/user/max_user_namespaces`. Settles whether item 7 is real here.
- **`nsswitch`** — check the `hosts:` line in `/etc/nsswitch.conf` still contains
  `files`. Cheap, and catches the root-level variant too.
- **`container-groups`** — `docker`/`podman`/`lxd` membership. Root-equivalent,
  and fits precheck's existing escalation framing directly.

Scoping caveat: precheck's package doc frames it as routes to *become root or
recover*. These are resolver-layer evasion — a different axis. They belong under
a second heading in the report, not mixed into the existing findings, or the
break-glass narrative gets muddied. Either way precheck stays advisory: it
reports, it doesn't enforce.

## The structural fix

Items 1, 3, and most of 7 share one root cause: blocking at the hosts-file layer
only works for clients that use the system resolver. A local DNS resolver plus an
nftables rule redirecting all egress on 53/853 and blocking known DoH endpoints
closes them together. That's a v2-scale change, noted here so the incremental
fixes above aren't mistaken for a complete answer.
