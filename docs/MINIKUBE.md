# Testing stellarCD with Minikube

Guide to build, load, and deploy stellarCD to a local minikube cluster.

## Prerequisites

- Docker installed and running
- Minikube installed (`brew install minikube` or similar)
- `kubectl` configured
- Go 1.21+ (for code generation)

## Quick Start

### 1. Start minikube

```bash
minikube start
```

### 2. Full workflow (build + load + deploy)

```bash
make minikube-full
```

This runs all three steps in sequence.

## Step-by-step Workflow

### Step 1: Build images locally

```bash
make minikube-build
```

Builds:
- Controller binary image (`controller:latest`)
- UI image (`ui:latest`)

Options:
```bash
CONTROLLER_IMG=mycontroller:v1.0 UI_IMG=myui:v1.0 make minikube-build
```

### Step 2: Load images into minikube

```bash
make minikube-load
```

Transfers the built images into minikube's Docker daemon without pushing to a registry.

Options:
```bash
MINIKUBE_PROFILE=custom-profile make minikube-load
```

### Step 3: Deploy to minikube

```bash
make minikube-deploy
```

Uses Kustomize to deploy to the minikube cluster (default namespace: `stellarcd-system`).

## Custom Image Tags

Set image names before running any command:

```bash
export CONTROLLER_IMG=controller:dev
export UI_IMG=ui:dev
export MINIKUBE_PROFILE=stellarcd-dev

make minikube-full
```

Or inline:

```bash
CONTROLLER_IMG=controller:dev make minikube-full
```

## Verify Deployment

```bash
# Check pods
kubectl -n stellarcd-system get pods

# Watch logs
kubectl -n stellarcd-system logs -f deployment/manager

# Access UI (if deployed)
kubectl -n stellarcd-system port-forward svc/ui 3000:3000
# Then open http://localhost:3000
```

## Cleanup

### Undeploy from minikube

```bash
make undeploy
```

### Stop minikube

```bash
minikube stop
```

### Delete minikube cluster

```bash
minikube delete
```

## Troubleshooting

### "minikube profile is not running"

Start minikube first:
```bash
minikube start -p <profile-name>
```

### Image not found in minikube

Ensure image was built and loaded:
```bash
docker images | grep controller
minikube image ls
```

### CRDs not installed

Manually install CRDs:
```bash
make install
```

### Deployment stuck in ImagePullBackOff

Set imagePullPolicy to Never when using local images. Check kustomization patches in `config/default/`.
