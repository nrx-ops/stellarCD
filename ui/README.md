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
docker build -t ui:latest .
```

Run container:
```bash
docker run -p 3000:3000 \
  -e ADMIN_API_URL=http://stellarcd-admin-api:8080 \
  ui:latest
```

The image serves the static build with nginx and proxies `/api` to
`$ADMIN_API_URL`. The vite dev server is never used at runtime: its proxy
configuration only applies to `npm run dev`.

## Architecture

- **Frontend:** React 18, TypeScript, Tailwind CSS
- **State:** React Query for API data fetching
- **Build:** Vite (fast HMR, optimized build)
- **API:** the stellarCD operator's admin API (axios + reverse proxy)

## Kubernetes Integration

The dashboard never calls the Kubernetes API server directly and holds no
cluster credentials. All reads go through the operator's admin API
(`internal/adminapi`), which runs inside the manager and therefore reuses the
manager's ServiceAccount, RBAC and automatically renewed token.

```
browser -> nginx (/api) -> stellarcd-admin-api:8080 -> Kubernetes API
```

- Development: `vite.config.ts` proxies `/api` to `$ADMIN_API_URL`
  (default `http://localhost:8080`, i.e. a locally running `make run`).
- Production: `nginx.conf.template` proxies `/api` to `$ADMIN_API_URL`,
  set by `config/ui/deployment.yaml`.

The admin API is unauthenticated and read-only. Put the dashboard behind
`oauth2-proxy` + `ingress-nginx` (or an equivalent gateway) before exposing it
outside the cluster.

## Endpoints consumed

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/stellarapps?namespace=` | List StellarApps |
| GET | `/api/v1/stellarapps/{ns}/{name}` | Get one StellarApp |
| GET | `/api/v1/stellarapps/{ns}/{name}/events` | Events recorded for an app |
| GET | `/api/v1/namespaces` | Namespace names |
| GET | `/api/v1/crds` | Installed stellarCD CRDs |
