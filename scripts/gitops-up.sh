#!/usr/bin/env bash
# Install Argo CD on the kind cluster and hand the platform to GitOps.
#
#   scripts/gitops-up.sh [REVISION]   default: main
#
# Argo CD then pulls deploy/argocd/apps from GitHub and syncs metrics-server,
# kube-prometheus-stack, the OTel collector, Argo Rollouts and BurnRate.
# The burnrate image is not in a registry yet (see Future work), so this
# script still builds it and loads it into kind.
source "$(dirname "$0")/lib.sh"
need kind kubectl helm docker

REVISION="${1:-${GITOPS_REVISION:-main}}"

if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  log "creating kind cluster $CLUSTER"
  kind create cluster --config "$ROOT/deploy/kind/cluster.yaml" --name "$CLUSTER" --wait 120s
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null

if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  log "building burnrate:$IMAGE_TAG"
  docker build -q -t "burnrate:$IMAGE_TAG" --build-arg VERSION="$IMAGE_TAG" "$ROOT" >/dev/null
fi
load_image "burnrate:$IMAGE_TAG" || die "could not load burnrate:$IMAGE_TAG into kind"

helm repo add argo https://argoproj.github.io/argo-helm >/dev/null 2>&1 || true
helm repo update argo >/dev/null
log "installing Argo CD $ARGO_CD_VERSION"
helm upgrade --install argocd argo/argo-cd --version "$ARGO_CD_VERSION" \
  -n argocd --create-namespace \
  --set dex.enabled=false --set notifications.enabled=false \
  --wait --timeout 10m >/dev/null

log "applying root app at revision $REVISION"
sed -e "s#targetRevision: main#targetRevision: $REVISION#" \
    -e "s#revision: main#revision: $REVISION#" \
    "$ROOT/deploy/argocd/root-app.yaml" | kubectl apply -f - >/dev/null

log "waiting for Argo CD to sync every app (up to 15 minutes)"
for _ in $(seq 1 180); do
  status="$(kubectl -n argocd get applications -o jsonpath='{range .items[*]}{.metadata.name}={.status.sync.status}/{.status.health.status}{" "}{end}' 2>/dev/null || true)"
  if [[ -n "$status" ]] && grep -q 'burnrate=' <<<"$status" && ! grep -qvE '=Synced/Healthy$' < <(tr ' ' '\n' <<<"$status" | grep .); then
    log "all apps Synced/Healthy: $status"
    break
  fi
  sleep 5
done
kubectl -n argocd get applications
cat >&2 <<MSG

  Argo CD UI: kubectl -n argocd port-forward svc/argocd-server 8443:443
  Password:   kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d
MSG
