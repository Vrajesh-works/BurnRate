#!/usr/bin/env bash
# Bring up BurnRate on a local kind cluster.
#
#   scripts/kind-up.sh            full stack: metrics-server, kube-prometheus-stack,
#                                 Argo Rollouts, OTel collector, BurnRate as a Rollout
#   scripts/kind-up.sh --minimal  metrics-server and BurnRate as a plain Deployment
#                                 (what CI runs)
source "$(dirname "$0")/lib.sh"

MODE=full
[[ "${1:-}" == "--minimal" ]] && MODE=minimal
need kind kubectl helm docker

if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  log "creating kind cluster $CLUSTER"
  kind create cluster --config "$ROOT/deploy/kind/cluster.yaml" --name "$CLUSTER" --wait 120s
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null

if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  log "building burnrate:$IMAGE_TAG"
  docker build -q -t "burnrate:$IMAGE_TAG" --build-arg VERSION="$IMAGE_TAG" "$ROOT" >/dev/null
fi
log "loading image into kind"
load_image "burnrate:$IMAGE_TAG" || die "could not load burnrate:$IMAGE_TAG into kind"

helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ >/dev/null 2>&1 || true
if [[ "$MODE" == full ]]; then
  helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
  helm repo add argo https://argoproj.github.io/argo-helm >/dev/null 2>&1 || true
  helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts >/dev/null 2>&1 || true
fi
helm repo update >/dev/null

log "preloading platform images into kind"
mapfile -t images < <(
  chart_images metrics-server/metrics-server --version "$METRICS_SERVER_VERSION"
  if [[ "$MODE" == full ]]; then
    chart_images prometheus-community/kube-prometheus-stack --version "$KUBE_PROMETHEUS_STACK_VERSION" \
      -f "$ROOT/deploy/kind/kube-prometheus-stack-values.yaml"
    chart_images open-telemetry/opentelemetry-collector --version "$OTEL_COLLECTOR_VERSION" \
      -f "$ROOT/deploy/kind/otel-collector-values.yaml"
    chart_images argo/argo-rollouts --version "$ARGO_ROLLOUTS_VERSION"
  fi
  echo curlimages/curl:8.22.0
)
preload_images "${images[@]}"

log "installing metrics-server (HPA needs it)"
helm upgrade --install metrics-server metrics-server/metrics-server \
  --version "$METRICS_SERVER_VERSION" -n kube-system \
  --set 'args={--kubelet-insecure-tls}' --wait --timeout 5m >/dev/null

if [[ "$MODE" == full ]]; then
  log "installing kube-prometheus-stack (Prometheus, Grafana, Alertmanager)"
  helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
    --version "$KUBE_PROMETHEUS_STACK_VERSION" -n monitoring --create-namespace \
    -f "$ROOT/deploy/kind/kube-prometheus-stack-values.yaml" --wait --timeout 10m >/dev/null

  log "installing OpenTelemetry collector"
  helm upgrade --install otel-collector open-telemetry/opentelemetry-collector \
    --version "$OTEL_COLLECTOR_VERSION" -n monitoring \
    -f "$ROOT/deploy/kind/otel-collector-values.yaml" --wait --timeout 5m >/dev/null

  log "installing Argo Rollouts"
  helm upgrade --install argo-rollouts argo/argo-rollouts \
    --version "$ARGO_ROLLOUTS_VERSION" -n argo-rollouts --create-namespace \
    --wait --timeout 5m >/dev/null

  VALUES=(-f "$ROOT/deploy/helm/burnrate/values-kind.yaml")
else
  VALUES=(--set image.pullPolicy=Never)
fi

log "installing burnrate ($MODE)"
helm upgrade --install "$RELEASE" "$ROOT/deploy/helm/burnrate" -n "$NS" --create-namespace \
  "${VALUES[@]}" --set image.tag="$IMAGE_TAG" --wait --timeout 5m >/dev/null
kubectl -n "$NS" wait --for=condition=Ready pod -l "$SELECTOR" --timeout=180s >/dev/null

# With the HPA on, the chart leaves spec.replicas unset so upgrades never fight
# the autoscaler; the workload starts at 1 and the HPA lifts it to minReplicas.
want="$(kubectl -n "$NS" get hpa "$RELEASE" -o jsonpath='{.spec.minReplicas}' 2>/dev/null || echo 1)"
log "waiting for $want ready pods"
for _ in $(seq 1 90); do
  (( $(ready_pods) >= want )) && break
  sleep 2
done
(( $(ready_pods) >= want )) || die "only $(ready_pods) of $want pods became ready"

log "running helm test"
helm test "$RELEASE" -n "$NS" --timeout 2m >/dev/null

log "burnrate is up: $(ready_pods) ready pods"
cat >&2 <<MSG

  API:      kubectl -n $NS port-forward svc/$RELEASE 8080:80
MSG
if [[ "$MODE" == full ]]; then
  cat >&2 <<MSG
  Grafana:  kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80   (admin / burnrate)
  Prometheus: kubectl -n monitoring port-forward svc/kube-prometheus-stack-prometheus 9090:9090
MSG
fi
