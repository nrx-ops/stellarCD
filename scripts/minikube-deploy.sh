#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$SCRIPT_DIR"

CONTROLLER_IMG="${CONTROLLER_IMG:-controller:latest}"
UI_IMG="${UI_IMG:-ui:latest}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-minikube}"
NAMESPACE="${NAMESPACE:-stellarcd-system}"
KUSTOMIZE="${KUSTOMIZE:-./bin/kustomize}"

# Check minikube is running
if ! minikube -p "$MINIKUBE_PROFILE" status > /dev/null 2>&1; then
  echo "Error: minikube profile '$MINIKUBE_PROFILE' is not running"
  exit 1
fi

# Set kubectl context to minikube
kubectl config use-context "$MINIKUBE_PROFILE" || true

# Ensure kustomize exists
if [ ! -f "$KUSTOMIZE" ]; then
  echo "Error: kustomize not found at $KUSTOMIZE"
  echo "Run 'make kustomize' first"
  exit 1
fi

# Create namespace if it doesn't exist
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

echo "Deploying to minikube ($MINIKUBE_PROFILE) in namespace $NAMESPACE..."

# Update image refs in kustomization and deploy
cd config/manager
"$KUSTOMIZE" edit set image "controller=$CONTROLLER_IMG" || true
cd - > /dev/null

"$KUSTOMIZE" build config/default | kubectl apply -f -

echo "✓ Deployment complete"
echo ""
echo "Check deployment status:"
echo "  kubectl -n $NAMESPACE get deployments"
echo "  kubectl -n $NAMESPACE logs -f deployment/manager"
