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

The admin API is unauthenticated. Its write routes (Flares, Astrals) answer 403
unless the operator runs with `--admin-api-writes`, which the chart only turns on
in `values-minikube.yaml`. Put the dashboard behind `oauth2-proxy` +
`ingress-nginx` (or an equivalent gateway) before exposing it outside the
cluster, and keep writes off until then.

## Screens

The screens follow the DESIGN specification in Confluence (SCR- pages, the
"Navigation" page and the "UX - Statuts et couleurs" page). The interface is in
English.

| Route | Screen |
| --- | --- |
| `/u/<universe>[/g/<galaxy>]` | Astral list, filtered by Galaxy |
| `/u/<universe>/new` | New Astral |
| `/u/<universe>/g/<galaxy>/a/<astral>/<tab>` | Astral detail: `overview`, `drift`, `history`, `configuration` |
| `/u/<universe>/g/<galaxy>/a/<astral>/flares/<flare>` | Flare detail and pod logs |
| `/u/<universe>/admin/galaxies[/new\|/<galaxy>]` | Admin: Galaxies, their defaults, backend and discovery |
| `/u/<universe>/admin/credentials` | Admin: Secrets of the Universe namespace (values never shown) |

Statuses refresh every 3 seconds by polling the admin API.

## Endpoints consumed

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/config` | Whether writes are enabled |
| GET | `/api/v1/universes` | Universe selector |
| GET | `/api/v1/galaxies?universe=` | Galaxy column |
| GET | `/api/v1/astrals?universe=` | Astral list |
| GET | `/api/v1/astrals/{ns}/{name}` | Astral detail |
| GET | `/api/v1/astrals/{ns}/{name}/flares` | Flares of an Astral, newest first |
| GET | `/api/v1/flares/{ns}/{name}` | Flare detail |
| GET | `/api/v1/flares/{ns}/{name}/logs` | Runner log of the Flare's Job |
| GET | `/api/v1/namespaces/{ns}/secrets` | Secret names for the credential pickers |
| POST | `/api/v1/astrals` | Create an Astral |
| PUT | `/api/v1/astrals/{ns}/{name}` | Update an Astral, then start a plan |
| DELETE | `/api/v1/astrals/{ns}/{name}` | Delete an Astral (never destroys infrastructure) |
| POST | `/api/v1/astrals/{ns}/{name}/flares` | Run plan / reconcile, whole Astral or one unit |
| POST | `/api/v1/flares/{ns}/{name}/cancel` | Cancel a Flare |
| POST | `/api/v1/flares/{ns}/{name}/approve` | Approve or reject a Flare |
| POST | `/api/v1/flares/{ns}/{name}/confirm-destroy` | Confirm the destructions of an apply |
| GET, PUT, DELETE | `/api/v1/galaxies/{ns}/{name}` | Read, update or delete a Galaxy (delete refused while it has Astrals) |
| POST | `/api/v1/galaxies` | Create a Galaxy in its Universe's namespace |
| GET, POST | `/api/v1/namespaces/{ns}/credentials` | List Secrets with their usage (no values), create one |
| PUT, DELETE | `/api/v1/namespaces/{ns}/credentials/{name}` | Replace every value of a Secret, delete an unused one |
