#!/usr/bin/env bash
set -euo pipefail

MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-minikube}"

echo "Checking minikube environment..."
echo ""

# Check Docker
if command -v docker &> /dev/null; then
  echo "✓ Docker: $(docker --version)"
else
  echo "✗ Docker not found"
  exit 1
fi

# Check Minikube
if command -v minikube &> /dev/null; then
  echo "✓ Minikube: $(minikube version --short)"
else
  echo "✗ Minikube not found"
  exit 1
fi

# Check kubectl
if command -v kubectl &> /dev/null; then
  echo "✓ kubectl: $(kubectl version --client --short 2>/dev/null || echo 'installed')"
else
  echo "✗ kubectl not found"
  exit 1
fi

# Check minikube status
echo ""
echo "Minikube profile: $MINIKUBE_PROFILE"
if minikube -p "$MINIKUBE_PROFILE" status > /dev/null 2>&1; then
  STATUS=$(minikube -p "$MINIKUBE_PROFILE" status --format {{.Host}})
  echo "✓ Minikube is running"
  echo ""

  # Show image list
  echo "Images in minikube:"
  minikube -p "$MINIKUBE_PROFILE" image ls | head -10
  echo "..."
else
  echo "✗ Minikube profile '$MINIKUBE_PROFILE' is not running"
  echo ""
  echo "To start minikube:"
  echo "  minikube start -p $MINIKUBE_PROFILE"
  exit 1
fi

echo ""
echo "✓ Environment ready for deployment"
