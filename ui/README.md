# stellarCD UI

React + TypeScript dashboard for stellarCD Kubernetes Operator.

## Development

```bash
npm install
npm run dev
```

UI runs on `http://localhost:3000` with hot reload.

## Build

```bash
npm run build
```

Outputs to `dist/`.

## Docker

Build image:
```bash
docker build -t stellarcd-ui:latest .
```

Run container:
```bash
docker run -p 3000:3000 \
  -e KUBE_API_URL=https://kubernetes.default.svc.cluster.local \
  stellarcd-ui:latest
```

## Architecture

- **Frontend:** React 18, TypeScript, Tailwind CSS
- **State:** React Query for API data fetching
- **Build:** Vite (fast HMR, optimized build)
- **API:** Kubernetes API client (axios + proxy)

## Kubernetes Integration

UI communicates with Kubernetes API via a proxy (configured in `vite.config.ts`). In production, deploy behind an API gateway or reverse proxy that:
1. Handles OIDC/RBAC authentication
2. Proxies requests to Kubernetes API server
3. Enforces CORS headers

Example: Use `oauth2-proxy` + `ingress-nginx` to secure the dashboard.
