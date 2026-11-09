#!/usr/bin/env bash
# Canary auto-rollback demo (needs the full stack from scripts/kind-up.sh).
#
# 1. Starts in-cluster traffic against the Service.
# 2. Upgrades the release with FAULT_ERROR_RATE so the new canary pods fail
#    a share of /v1/check calls.
# 3. Waits for the Prometheus AnalysisTemplate to fail and Argo Rollouts to
#    abort, then records how long that took.
# 4. Restores the healthy config.
#
# Env: FAULT_ERROR_RATE (default 0.3), TIMEOUT_SECONDS (default 300), RESULTS_DIR.
source "$(dirname "$0")/lib.sh"
need kubectl helm

FAULT="${FAULT_ERROR_RATE:-0.3}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-300}"
RESULTS_DIR="${RESULTS_DIR:-$ROOT/chaos-results}"
TRAFFIC="canary-traffic-$(date +%s)"
mkdir -p "$RESULTS_DIR"

kubectl -n "$NS" get rollout "$RELEASE" >/dev/null 2>&1 || die "no Rollout $RELEASE in $NS; run scripts/kind-up.sh (full mode) first"
phase="$(kubectl -n "$NS" get rollout "$RELEASE" -o jsonpath='{.status.phase}')"
[[ "$phase" == Healthy ]] || die "rollout must start Healthy, is $phase"

cleanup() { kubectl -n "$NS" delete pod "$TRAFFIC" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT

log "starting background traffic"
kubectl -n "$NS" run "$TRAFFIC" --image=curlimages/curl:8.22.0 --restart=Never \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":100,"seccompProfile":{"type":"RuntimeDefault"}}}}' \
  --command -- sh -c "while true; do curl -s -o /dev/null -m 2 -X POST http://${RELEASE}.${NS}/v1/check \
    -d '{\"tenant\":\"canary\",\"key\":\"k'\$RANDOM'\"}'; sleep 0.02; done" >/dev/null
kubectl -n "$NS" wait --for=condition=Ready "pod/$TRAFFIC" --timeout=60s >/dev/null

log "deploying a bad canary (FAULT_ERROR_RATE=$FAULT)"
start="$(now_ms)"
helm upgrade "$RELEASE" "$ROOT/deploy/helm/burnrate" -n "$NS" --reuse-values \
  --set-string config.faultErrorRate="$FAULT" >/dev/null

outcome=timeout
deadline=$(( start + TIMEOUT_SECONDS * 1000 ))
while (( $(now_ms) < deadline )); do
  phase="$(kubectl -n "$NS" get rollout "$RELEASE" -o jsonpath='{.status.phase}')"
  aborted="$(kubectl -n "$NS" get rollout "$RELEASE" -o jsonpath='{.status.abort}')"
  if [[ "$aborted" == true || "$phase" == Degraded ]]; then
    outcome=aborted
    break
  fi
  if [[ "$phase" == Healthy ]] && (( $(now_ms) - start > 20000 )); then
    outcome=promoted
    break
  fi
  sleep 2
done
elapsed="$(awk -v s="$start" -v e="$(now_ms)" 'BEGIN { printf "%.1f", (e - s) / 1000 }')"
message="$(kubectl -n "$NS" get rollout "$RELEASE" -o jsonpath='{.status.message}' | tr '"' "'")"
analysis="$(kubectl -n "$NS" get analysisrun -l rollouts-pod-template-hash --sort-by=.metadata.creationTimestamp \
  -o jsonpath='{range .items[-1:]}{.metadata.name}={.status.phase}{end}' 2>/dev/null || true)"
log "outcome: $outcome after ${elapsed}s ($message) analysis: $analysis"

log "restoring healthy config"
helm upgrade "$RELEASE" "$ROOT/deploy/helm/burnrate" -n "$NS" --reuse-values \
  --set-string config.faultErrorRate=0 >/dev/null
for _ in $(seq 1 60); do
  [[ "$(kubectl -n "$NS" get rollout "$RELEASE" -o jsonpath='{.status.phase}')" == Healthy ]] && break
  sleep 2
done

result="$RESULTS_DIR/canary-$(date -u +%Y%m%dT%H%M%SZ).json"
cat > "$result" <<JSON
{
  "experiment": "bad-canary",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "fault_error_rate": $FAULT,
  "outcome": "$outcome",
  "seconds_to_outcome": $elapsed,
  "analysis_run": "$analysis",
  "rollout_message": "$message"
}
JSON
cat "$result"
[[ "$outcome" == aborted ]]
