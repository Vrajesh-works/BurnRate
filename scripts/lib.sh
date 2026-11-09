#!/usr/bin/env bash
# Shared helpers for the kind scripts. Source it; do not run it.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${CLUSTER:-burnrate}"
NS="${NS:-burnrate}"
RELEASE="${RELEASE:-burnrate}"
SELECTOR="app.kubernetes.io/name=burnrate,app.kubernetes.io/instance=${RELEASE}"
IMAGE_TAG="${IMAGE_TAG:-$(awk '/^appVersion:/ {gsub(/"/, "", $2); print $2}' "$ROOT/deploy/helm/burnrate/Chart.yaml")}"
# shellcheck source=../deploy/kind/versions.env
source "$ROOT/deploy/kind/versions.env"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

need() {
  for bin in "$@"; do
    command -v "$bin" >/dev/null 2>&1 || die "missing required tool: $bin"
  done
}

# ready_pods prints how many burnrate pods are Ready.
ready_pods() {
  kubectl -n "$NS" get pods -l "$SELECTOR" \
    -o jsonpath='{range .items[*]}{.metadata.deletionTimestamp}{"|"}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' \
    | grep -c '^|True$' || true
}

# now_ms prints wall-clock milliseconds.
now_ms() { date +%s%3N; }

# preload_images pulls each image on the host (where Docker retries and caches)
# and loads it into the kind nodes, so slow registry pulls inside kind do not
# blow the helm --wait timeout. Set PRELOAD_IMAGES=0 to skip.
preload_images() {
  [[ "${PRELOAD_IMAGES:-1}" == "1" ]] || return 0
  local img
  for img in "$@"; do
    [[ -n "$img" ]] || continue
    if ! docker image inspect "$img" >/dev/null 2>&1; then
      log "pulling $img"
      docker pull -q "$img" >/dev/null || { log "pull failed for $img, kind will try itself"; continue; }
    fi
    load_image "$img" || log "kind load failed for $img"
  done
}

# load_image copies one host image into every kind node. `kind load
# docker-image` fails for multi-arch images when Docker uses the containerd
# image store (the saved index points at platforms that were never pulled),
# so save only the host platform and load that archive instead.
load_image() {
  local tar arch rc=1
  tar="$(mktemp --suffix=.tar)"
  arch="linux/$(docker version -f '{{.Server.Arch}}')"
  # A pull interrupted half-way leaves a tag without the host platform's
  # layers; pull again once if the save fails.
  if docker save --platform "$arch" "$1" -o "$tar" 2>/dev/null \
      || { docker pull -q --platform "$arch" "$1" >/dev/null 2>&1 && docker save --platform "$arch" "$1" -o "$tar" 2>/dev/null; }; then
    kind load image-archive "$tar" --name "$CLUSTER" >/dev/null 2>&1
    rc=$?
  fi
  rm -f "$tar"
  # Some images (grafana distroless) have no os/arch in their config, so
  # `docker save --platform` refuses them. Pull inside each node instead,
  # with retries, before the chart install starts its timeout.
  if (( rc != 0 )); then
    rc=0
    local node try
    for node in $(kind get nodes --name "$CLUSTER"); do
      for try in 1 2 3; do
        docker exec "$node" crictl pull "$1" >/dev/null 2>&1 && break
        (( try == 3 )) && rc=1
        sleep 2
      done
    done
  fi
  return $rc
}

# chart_images prints every container image a chart renders with the given
# helm template arguments, one per line, so they can be preloaded.
chart_images() {
  helm template preload "$@" 2>/dev/null \
    | awk '($1 == "image:" || ($1 == "-" && $2 == "image:")) && $NF != "image:" { print $NF }' \
    | tr -d "\"'" | sort -u
}
