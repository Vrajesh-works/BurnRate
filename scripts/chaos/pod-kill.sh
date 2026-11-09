#!/usr/bin/env bash
# Pod-kill chaos test with measured recovery.
#
# Starts an in-cluster probe that calls POST /v1/check through the Service
# every 50 ms, deletes one BurnRate pod, and records:
#   - recovery_seconds: time from the delete until the Ready pod count is back
#     to where it was before the kill
#   - probe requests and failures seen by the client during the experiment
#
# Usage: scripts/chaos/pod-kill.sh [--force]
#   --force   delete with --grace-period=0 (SIGKILL, no graceful drain)
# Env: RESULTS_DIR (default ./chaos-results), MAX_RECOVERY_SECONDS (default 60)
source "$(dirname "$0")/../lib.sh"
need kubectl

FORCE=0
[[ "${1:-}" == "--force" ]] && FORCE=1
RESULTS_DIR="${RESULTS_DIR:-$ROOT/chaos-results}"
MAX_RECOVERY_SECONDS="${MAX_RECOVERY_SECONDS:-60}"
PROBE="chaos-probe-$(date +%s)"
mkdir -p "$RESULTS_DIR"

before="$(ready_pods)"
(( before >= 2 )) || die "need at least 2 ready pods before the experiment, have $before"
victim="$(kubectl -n "$NS" get pods -l "$SELECTOR" --field-selector=status.phase=Running \
  -o jsonpath='{.items[0].metadata.name}')"
log "ready pods before: $before, victim: $victim, force: $FORCE"

cleanup() { kubectl -n "$NS" delete pod "$PROBE" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT

# The probe prints one status code per request; "000" means connection error.
kubectl -n "$NS" run "$PROBE" --image=curlimages/curl:8.22.0 --restart=Never \
  --labels=app.kubernetes.io/part-of=chaos \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":100,"seccompProfile":{"type":"RuntimeDefault"}}}}' \
  --command -- sh -c "end=\$((\$(date +%s)+${MAX_RECOVERY_SECONDS}+20)); while [ \$(date +%s) -lt \$end ]; do \
    curl -s -o /dev/null -m 2 -w '%{http_code}\n' -X POST http://${RELEASE}.${NS}/v1/check \
      -d '{\"tenant\":\"chaos\",\"key\":\"probe\"}'; sleep 0.05; done" >/dev/null
kubectl -n "$NS" wait --for=condition=Ready "pod/$PROBE" --timeout=60s >/dev/null
sleep 3

delete_args=(--wait=false)
(( FORCE )) && delete_args+=(--grace-period=0 --force)
start="$(now_ms)"
kubectl -n "$NS" delete pod "$victim" "${delete_args[@]}" >/dev/null 2>&1
log "deleted $victim at $start ms"

# ready_pods ignores pods with a deletionTimestamp, so the count dips as soon
# as the delete is accepted. Recovery is when it is back to the starting value.
dipped=0
recovered=""
deadline=$(( start + MAX_RECOVERY_SECONDS * 1000 ))
while (( $(now_ms) < deadline )); do
  n="$(ready_pods)"
  (( n < before )) && dipped=1
  if (( dipped )) && (( n >= before )); then
    recovered="$(now_ms)"
    break
  fi
  sleep 0.25
done

sleep 3  # let the probe observe the steady state again
codes="$(kubectl -n "$NS" logs "$PROBE" 2>/dev/null || true)"
total="$(grep -c . <<<"$codes" || true)"
failed="$(grep -vc '^200$' <<<"$codes" || true)"

if [[ -z "$recovered" ]]; then
  status=failed
  recovery="null"
  log "did not recover within ${MAX_RECOVERY_SECONDS}s"
else
  status=recovered
  recovery="$(awk -v s="$start" -v e="$recovered" 'BEGIN { printf "%.2f", (e - s) / 1000 }')"
  log "recovered in ${recovery}s"
fi

result="$RESULTS_DIR/pod-kill-$(date -u +%Y%m%dT%H%M%SZ).json"
cat >"$result" <<JSON
{
  "experiment": "pod-kill",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "force": $([[ $FORCE == 1 ]] && echo true || echo false),
  "victim": "$victim",
  "ready_pods_before": $before,
  "status": "$status",
  "recovery_seconds": $recovery,
  "max_recovery_seconds": $MAX_RECOVERY_SECONDS,
  "probe_requests": $total,
  "probe_failures": $failed
}
JSON
cat "$result"
log "probe: $failed failed out of $total requests; result written to $result"
[[ "$status" == recovered ]]
