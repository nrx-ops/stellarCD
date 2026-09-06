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

echo "Loading controller image: $CONTROLLER_IMG"
minikube -p "$MINIKUBE_PROFILE" image load "$CONTROLLER_IMG"

echo "Loading UI image: $UI_IMG"
minikube -p "$MINIKUBE_PROFILE" image load "$UI_IMG"

echo "✓ Images loaded successfully into minikube"
