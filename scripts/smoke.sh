#!/usr/bin/env bash
# End-to-end smoke test against a running BurnRate.
#   scripts/smoke.sh [BASE_URL]   default http://localhost:8080
set -euo pipefail
BASE="${1:-${BASE_URL:-http://localhost:8080}}"
TENANT="smoke-$(date +%s)-$RANDOM"
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok   $*"; }

curl -fsS "$BASE/healthz" | grep -q '"ok"' || fail "healthz"
pass "healthz"
curl -fsS "$BASE/readyz" | grep -q '"ready"' || fail "readyz"
pass "readyz"

curl -fsS -X PUT "$BASE/v1/policies/$TENANT" -d '{"capacity":2,"refill_per_second":0.01}' \
  | grep -q '"source":"override"' || fail "put policy"
pass "policy override for $TENANT"

for i in 1 2; do
  curl -fsS -X POST "$BASE/v1/check" -d "{\"tenant\":\"$TENANT\",\"key\":\"card-auth\"}" \
    | grep -q '"allowed":true' || fail "check $i should be allowed"
done
pass "first two checks allowed"

headers="$(mktemp)"
trap 'rm -f "$headers"' EXIT
body="$(curl -fsS -D "$headers" -X POST "$BASE/v1/check" -d "{\"tenant\":\"$TENANT\",\"key\":\"card-auth\"}")"
echo "$body" | grep -q '"allowed":false' || fail "third check should be denied: $body"
grep -qi '^retry-after:' "$headers" || fail "denied response has no Retry-After header"
pass "third check denied with Retry-After"

code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/v1/check" -d '{"tenant":""}')"
[[ "$code" == 400 ]] || fail "bad request returned $code, want 400"
pass "bad request rejected with 400"

metrics="$(curl -fsS "$BASE/metrics")"
for series in 'http_requests_total{code="200",method="POST",route="/v1/check"}' 'burnrate_decisions_total{decision="denied"}' 'burnrate_build_info'; do
  grep -qF "$series" <<<"$metrics" || fail "metrics missing $series"
done
pass "metrics exported"
echo "smoke test passed against $BASE"
