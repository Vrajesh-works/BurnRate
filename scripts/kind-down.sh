#!/usr/bin/env bash
# Delete the local kind cluster.
source "$(dirname "$0")/lib.sh"
need kind
kind delete cluster --name "$CLUSTER"
