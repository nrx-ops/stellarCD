#!/usr/bin/env bash
set -euo pipefail

CONTROLLER_IMG="${CONTROLLER_IMG:-controller:latest}"
UI_IMG="${UI_IMG:-ui:latest}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-minikube}"

# Check minikube is running
if ! minikube -p "$MINIKUBE_PROFILE" status > /dev/null 2>&1; then
  echo "Error: minikube profile '$MINIKUBE_PROFILE' is not running"
  exit 1
fi

echo "Loading images into minikube ($MINIKUBE_PROFILE)..."

# Drop the tag inside the node before loading it again.
#
# `minikube image load` keeps whatever image already carries the tag, and
# --overwrite alone does not help while a stopped container still references it.
# A rebuilt ":latest" then silently never reaches the node and the cluster keeps
# running the previous binary. Untagging first makes every load authoritative.
load_image() {
  local img="$1"

  echo "Loading $img"
  # `docker rmi -f` on a tag whose image a container still uses only removes the
  # tag; the layers stay behind for that container. Running pods therefore keep
  # working on the old image ID until they are recreated.
  minikube -p "$MINIKUBE_PROFILE" ssh -- "docker rmi -f $img >/dev/null 2>&1; true"
  minikube -p "$MINIKUBE_PROFILE" image load --overwrite=true "$img"
}

load_image "$CONTROLLER_IMG"
load_image "$UI_IMG"

echo "✓ Images loaded successfully into minikube"
