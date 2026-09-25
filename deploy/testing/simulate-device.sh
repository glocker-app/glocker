#!/usr/bin/env bash
# Simulate a device syncing to glockpeek — POST a small batch of violations +
# heartbeats to /api/ingest with the given ingest token. glockpeek stamps the
# device_id from the token (one token = one device), so running this with two
# different tokens makes two devices appear on the account, with real rows.
#
# Handy for exercising the multi-device dashboard without a real second machine.
# Sends plaintext (encrypted:false), so the target must NOT have
# require_encryption on (a throwaway `glockpeek -mode hosted` test instance is
# fine; do not point it at a real encrypted account).
#
# Usage: simulate-device.sh <glockpeek-url> <ingest-token> [num-violations]
set -euo pipefail

URL="${1:?usage: simulate-device.sh <glockpeek-url> <ingest-token> [n]}"
TOKEN="${2:?ingest token required}"
N="${3:-8}"

now=$(( $(date +%s) * 1000 ))
day=$(( 86400 * 1000 ))
kws=(gambling casino reddit youtube twitter news shopping)
doms=(bet365.com reddit.com youtube.com twitter.com news.ycombinator.com amazon.com)

viols=""
for i in $(seq 1 "$N"); do
  ts=$(( now - (RANDOM % 12) * day - (RANDOM % 20) * 3600000 ))
  k=${kws[$((RANDOM % ${#kws[@]}))]}
  d=${doms[$((RANDOM % ${#doms[@]}))]}
  [ -n "$viols" ] && viols="$viols,"
  viols="$viols{\"ts\":$ts,\"recordId\":\"sim-$i-$ts\",\"keyword\":\"$k\",\"url\":\"https://$d/x\",\"type\":\"content-keyword\",\"domain\":\"$d\"}"
done
hb="{\"ts\":$now,\"alive\":true},{\"ts\":$((now - 3600000)),\"alive\":true}"

curl -sS -X POST "$URL/api/ingest" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"encrypted\":false,\"violations\":[$viols],\"heartbeat\":[$hb]}" \
  -w '\ningest: %{http_code}\n'
