#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

export CONTROLLER_IMG="${CONTROLLER_IMG:-controller:latest}"
export UI_IMG="${UI_IMG:-ui:latest}"
export MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-minikube}"

echo "═══════════════════════════════════════════════════════════════"
echo "stellarCD Minikube Workflow"
echo "═══════════════════════════════════════════════════════════════"
echo "Profile: $MINIKUBE_PROFILE"
echo "Controller: $CONTROLLER_IMG"
echo "UI: $UI_IMG"
echo ""

# Step 1: Build
echo "Step 1/3: Building images..."
"$SCRIPT_DIR/minikube-build.sh"
echo ""

# Step 2: Load
echo "Step 2/3: Loading images into minikube..."
"$SCRIPT_DIR/minikube-load.sh"
echo ""

# Step 3: Deploy
echo "Step 3/3: Deploying to minikube..."
"$SCRIPT_DIR/minikube-deploy.sh"
echo ""

echo "═══════════════════════════════════════════════════════════════"
echo "✓ Workflow complete"
echo "═══════════════════════════════════════════════════════════════"
