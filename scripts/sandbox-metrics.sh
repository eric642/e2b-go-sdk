#!/usr/bin/env bash
# Create an E2B sandbox via the REST API, poll its metrics, and kill it.
#
# Required env:
#   E2B_API_KEY   team API key (X-API-Key header)
# Optional env:
#   E2B_DOMAIN    e2b domain; default "e2b.app"
#   E2B_API_URL   full control-plane URL override; default https://api.$E2B_DOMAIN
#   E2B_TEMPLATE  template id/alias; default "base"
#   SANDBOX_TTL   sandbox lifetime in seconds; default 300
#   POLL_COUNT    metric poll iterations; default 3
#   POLL_SLEEP    seconds between polls; default 10
#   KEEP_SANDBOX  set to 1 to skip kill on exit
#
# Deps: curl, jq.

set -euo pipefail

for bin in curl jq; do
  command -v "$bin" >/dev/null || { echo "missing dependency: $bin" >&2; exit 1; }
done

: "${E2B_API_KEY:?E2B_API_KEY is required}"
E2B_DOMAIN="${E2B_DOMAIN:-e2b.app}"
E2B_API_URL="${E2B_API_URL:-https://api.${E2B_DOMAIN}}"
E2B_TEMPLATE="${E2B_TEMPLATE:-base}"
SANDBOX_TTL="${SANDBOX_TTL:-300}"
POLL_COUNT="${POLL_COUNT:-3}"
POLL_SLEEP="${POLL_SLEEP:-10}"
KEEP_SANDBOX="${KEEP_SANDBOX:-0}"

api() {
  local method=$1 path=$2
  shift 2
  curl -sS -X "$method" \
    -H "X-API-Key: ${E2B_API_KEY}" \
    -H "Content-Type: application/json" \
    "${E2B_API_URL}${path}" \
    "$@"
}

echo ">> creating sandbox (template=${E2B_TEMPLATE}, ttl=${SANDBOX_TTL}s) at ${E2B_API_URL}"
create_body=$(jq -nc \
  --arg t "$E2B_TEMPLATE" \
  --argjson ttl "$SANDBOX_TTL" \
  '{templateID: $t, timeout: $ttl}')

create_resp=$(api POST /sandboxes --data-raw "$create_body")
sandbox_id=$(echo "$create_resp" | jq -r '.sandboxID // empty')

if [[ -z "$sandbox_id" ]]; then
  echo "create failed: $create_resp" >&2
  exit 1
fi
echo ">> sandbox created: $sandbox_id"

cleanup() {
  if [[ "$KEEP_SANDBOX" == "1" ]]; then
    echo ">> KEEP_SANDBOX=1, leaving $sandbox_id running"
    return
  fi
  echo ">> killing sandbox $sandbox_id"
  api DELETE "/sandboxes/${sandbox_id}" -o /dev/null -w "http %{http_code}\n" || true
}
trap cleanup EXIT

for i in $(seq 1 "$POLL_COUNT"); do
  echo ">> [$i/$POLL_COUNT] GET /sandboxes/${sandbox_id}/metrics"
  api GET "/sandboxes/${sandbox_id}/metrics"
  echo
  [[ "$i" -lt "$POLL_COUNT" ]] && sleep "$POLL_SLEEP"
done
