#!/usr/bin/env bash
# Run the k6 load test inside the kind cluster and record HPA scaling.
# Env: PEAK_RPS (default 1500), HOLD (default 2m), RESULTS_DIR.
source "$(dirname "$0")/lib.sh"
need kubectl

RESULTS_DIR="${RESULTS_DIR:-$ROOT/chaos-results}"
PEAK_RPS="${PEAK_RPS:-1500}"
HOLD="${HOLD:-2m}"
JOB="k6-load-$(date +%s)"
mkdir -p "$RESULTS_DIR"

kubectl -n "$NS" create configmap "$JOB" --from-file=check.js="$ROOT/loadtest/check.js" >/dev/null
cleanup() {
  kubectl -n "$NS" delete job "$JOB" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl -n "$NS" delete configmap "$JOB" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

start_replicas="$(kubectl -n "$NS" get hpa "$RELEASE" -o jsonpath='{.status.currentReplicas}')"
log "starting k6 at $PEAK_RPS rps for $HOLD; HPA replicas now: $start_replicas"

kubectl -n "$NS" apply -f - >/dev/null <<YAML
apiVersion: batch/v1
kind: Job
metadata:
  name: $JOB
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/part-of: loadtest
    spec:
      restartPolicy: Never
      securityContext:
        runAsNonRoot: true
        runAsUser: 12345
        seccompProfile: {type: RuntimeDefault}
      containers:
        - name: k6
          image: grafana/k6:2.3.0
          args: ["run", "--quiet", "-e", "BASE_URL=http://$RELEASE.$NS", "-e", "PEAK_RPS=$PEAK_RPS", "-e", "HOLD=$HOLD", "/scripts/check.js"]
          securityContext:
            allowPrivilegeEscalation: false
            capabilities: {drop: ["ALL"]}
          resources:
            requests: {cpu: 500m, memory: 256Mi}
          volumeMounts:
            - {name: scripts, mountPath: /scripts}
      volumes:
        - name: scripts
          configMap: {name: $JOB}
YAML

max_replicas="$start_replicas"
first_scale=""
t0="$(now_ms)"
while true; do
  succeeded="$(kubectl -n "$NS" get job "$JOB" -o jsonpath='{.status.succeeded}' 2>/dev/null || true)"
  failed="$(kubectl -n "$NS" get job "$JOB" -o jsonpath='{.status.failed}' 2>/dev/null || true)"
  cur="$(kubectl -n "$NS" get hpa "$RELEASE" -o jsonpath='{.status.currentReplicas}' 2>/dev/null || echo 0)"
  if (( cur > max_replicas )); then
    max_replicas="$cur"
    [[ -z "$first_scale" ]] && first_scale="$(awk -v s="$t0" -v e="$(now_ms)" 'BEGIN { printf "%.0f", (e - s) / 1000 }')"
    log "HPA scaled to $cur replicas"
  fi
  [[ -n "$succeeded" || -n "$failed" ]] && break
  sleep 5
done

kubectl -n "$NS" logs "job/$JOB" > "$RESULTS_DIR/$JOB.txt" 2>&1 || true
tail -40 "$RESULTS_DIR/$JOB.txt"
result="$RESULTS_DIR/$JOB.json"
cat > "$result" <<JSON
{
  "experiment": "k6-load",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "peak_rps": $PEAK_RPS,
  "hold": "$HOLD",
  "k6_thresholds_passed": $([[ -n "$succeeded" ]] && echo true || echo false),
  "replicas_before": $start_replicas,
  "replicas_max": $max_replicas,
  "seconds_to_first_scale_up": ${first_scale:-null}
}
JSON
cat "$result"
(( max_replicas > start_replicas )) || die "HPA did not scale out"
