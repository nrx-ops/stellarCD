#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$SCRIPT_DIR"

CONTROLLER_IMG="${CONTROLLER_IMG:-controller:latest}"
UI_IMG="${UI_IMG:-ui:latest}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
BUILD_DATE="${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

echo "Building controller image: $CONTROLLER_IMG"
docker build \
  --build-arg VERSION="$VERSION" \
  --build-arg GIT_COMMIT="$GIT_COMMIT" \
  --build-arg BUILD_DATE="$BUILD_DATE" \
  -t "$CONTROLLER_IMG" \
  .

echo "Building UI image: $UI_IMG"
docker build \
  -t "$UI_IMG" \
  ./ui

echo "✓ Images built successfully"
echo "  - $CONTROLLER_IMG"
echo "  - $UI_IMG"
