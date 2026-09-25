# Testing multi-device locally

Two ways to make an account look like it has several machines, so you can exercise
the per-device dashboard (the Combined/device switcher, per-device filtering)
without owning multiple computers.

Both need a **hosted** glockpeek so tokens map to devices (local mode is always a
single implicit device). Run a throwaway one with `-mode hosted` + a sqlite file —
no config, no touching your real instance:

```bash
go build -o /tmp/gp ./cmd/glockpeek
/tmp/gp -mode hosted -db /tmp/test.db -driver sqlite -listen 127.0.0.1:4334
```

## A. Simulate devices via the ingest API (simplest — no agent)

`device_id` is stamped from the ingest token, so one token = one device. Create an
account, raise its device cap, mint a token per "machine", and POST a batch with
each:

```bash
DB=/tmp/test.db; GP="/tmp/gp -mode hosted -db $DB -driver sqlite"
echo "testpass123" | $GP -adduser test@x.com
$GP -setlimit test@x.com -devices 5
$GP -addtoken test@x.com        # -> token A
$GP -addtoken test@x.com        # -> token B
# (optional) rename them so the switcher reads nicely:
sqlite3 "$DB" "UPDATE api_tokens SET name='work laptop' WHERE id=1; UPDATE api_tokens SET name='home desktop' WHERE id=2;"

./deploy/testing/simulate-device.sh http://127.0.0.1:4334 <token-A> 14
./deploy/testing/simulate-device.sh http://127.0.0.1:4334 <token-B> 6
```

Then log in as `test@x.com`, set up encryption once (the account needs a vault),
and the sidebar shows Combined + both devices with real rows.

Note: `simulate-device.sh` sends plaintext (`encrypted:false`) — only for a
throwaway instance without `require_encryption`. Never point it at a real account.

## B. A real agent in Docker (fuller, but fiddly)

Runs the actual syncer as a "second machine". Caveats: glocker wants root/setuid,
makes `/etc/hosts` immutable (`chattr`), edits sudoers, and its usage monitor needs
an X display — none of which a container has cleanly. So run it `--privileged`,
with the enforcement mechanisms disabled in its `conf.yaml` (`domains.enabled:
false`, `programs.enabled: false`, `sudoers.enabled: false`) and only
`sync.enabled: true` pointing at your test glockpeek with that device's token. You
mostly get heartbeats (no desktop = no usage data), which is why Approach A is
usually enough.
