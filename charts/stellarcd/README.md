# stellarcd

Helm chart for **stellarCD**, a GitOps continuous-delivery operator for Terraform and Terragrunt.

The chart installs two workloads:

| Component | What it is |
| --- | --- |
| `controller-manager` | The operator. Watches the `core.stellarcd.io` CRDs, clones Git repositories and drives `terraform` / `terragrunt`. |
| `frontend` | An nginx-served React dashboard. Holds no cluster credentials: it proxies `/api` to the operator's read-only admin API. |

## Prerequisites

- Kubernetes >= 1.25
- Helm >= 3.8
- The controller and UI images available to the cluster (see [Local images](#local-images-minikube))

## Install

```bash
helm upgrade --install stellarcd charts/stellarcd \
  --namespace stellarcd-system --create-namespace
```

### Local images (minikube)

The default image references (`controller:latest`, `ui:latest`) are unqualified local tags, so they must already exist in the node's image store.

```bash
make minikube-build                  # docker build both images
make minikube-load                   # minikube image load both
make helm-install                    # helm upgrade --install with values-minikube.yaml
```

`values-minikube.yaml` turns off leader election, serves metrics over plain HTTP and exposes the dashboard on NodePort 30300.

## CRDs

The five CRDs (`Universe`, `Galaxy`, `Astral`, `Flare`, `StellarApp`) live in `crds/`. Helm installs that directory on `helm install` and then never touches it again — which is what keeps `helm uninstall` from garbage-collecting your custom resources along with the release.

The trade-off is that **`helm upgrade` does not roll out CRD schema changes**. After editing the API types:

```bash
make manifests      # regenerate config/crd/bases from the Go markers
make helm-crds      # sync into charts/stellarcd/crds/ and kubectl apply
```

## Uninstall

```bash
helm uninstall stellarcd -n stellarcd-system
```

CRDs, any existing custom resources, and a workspace PVC created by the chart all survive this on purpose. Remove them explicitly:

```bash
kubectl delete -f charts/stellarcd/crds/
kubectl delete pvc -n stellarcd-system -l app.kubernetes.io/instance=stellarcd
```

## Values

### Global

| Key | Default | Description |
| --- | --- | --- |
| `nameOverride` | `""` | Override the chart name in resource names. |
| `fullnameOverride` | `""` | Override the whole generated name prefix. |
| `imagePullSecrets` | `[]` | Pull secrets for every pod. |
| `commonLabels` | `{}` | Labels added to every object. |
| `commonAnnotations` | `{}` | Annotations added to every object. |

### Controller

| Key | Default | Description |
| --- | --- | --- |
| `controller.replicaCount` | `1` | Manager replicas. Only the leader reconciles. |
| `controller.image.repository` | `controller` | Image repository. |
| `controller.image.tag` | `""` | Image tag; falls back to `.Chart.AppVersion`. |
| `controller.image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `controller.leaderElection.enabled` | `true` | Guards against two managers reconciling one Terraform state. Required when `replicaCount > 1`. |
| `controller.workspace.mountPath` | `/var/lib/stellarcd/workspaces` | Where Git checkouts and Terraform working dirs land. Must be writable — the root filesystem is not. |
| `controller.workspace.type` | `emptyDir` | `emptyDir` or `persistentVolumeClaim`. |
| `controller.workspace.emptyDir.sizeLimit` | `2Gi` | Cap on the scratch volume. |
| `controller.workspace.persistentVolumeClaim.existingClaim` | `""` | Reuse a claim instead of creating one. |
| `controller.workspace.persistentVolumeClaim.size` | `10Gi` | Size of the chart-created claim. |
| `controller.adminApi.enabled` | `true` | Serve the read-only API the dashboard reads through. |
| `controller.adminApi.port` | `8080` | Admin API container port. |
| `controller.adminApi.service.type` | `ClusterIP` | Admin API Service type. Keep internal: it is unauthenticated. |
| `controller.health.port` | `8081` | Port serving `/healthz` and `/readyz`. |
| `controller.metrics.enabled` | `true` | Serve Prometheus metrics. |
| `controller.metrics.secure` | `true` | Gate `/metrics` behind TokenReview + SubjectAccessReview. |
| `controller.metrics.port` | `8443` | Metrics container port. |
| `controller.metrics.serviceMonitor.enabled` | `false` | Create a `ServiceMonitor`. Requires the Prometheus Operator CRDs. |
| `controller.metrics.serviceMonitor.interval` | `30s` | Scrape interval. |
| `controller.metrics.serviceMonitor.labels` | `{}` | Extra labels, typically your Prometheus `release:` selector. |
| `controller.metrics.serviceMonitor.insecureSkipVerify` | `true` | Skip verification of the manager's self-signed metrics cert. |
| `controller.extraArgs` | `[]` | Extra manager flags, e.g. `--zap-log-level=debug`. |
| `controller.env` / `controller.envFrom` | `[]` | Extra environment for the manager. |
| `controller.extraVolumes` / `controller.extraVolumeMounts` | `[]` | Extra volumes, e.g. a cloud credentials file. |
| `controller.resources` | 500m/128Mi limits | Resource requests and limits. |
| `controller.terminationGracePeriodSeconds` | `10` | Time for in-flight Terraform runs to abort on SIGTERM. |
| `controller.podDisruptionBudget.enabled` | `false` | Create a `PodDisruptionBudget`. |
| `controller.nodeSelector` / `tolerations` / `affinity` / `topologySpreadConstraints` | `{}` / `[]` | Standard scheduling controls. |

### Dashboard

| Key | Default | Description |
| --- | --- | --- |
| `ui.enabled` | `true` | Deploy the dashboard. The operator runs fine without it. |
| `ui.image.repository` | `ui` | Image repository. |
| `ui.image.tag` | `""` | Image tag; falls back to `.Chart.AppVersion`. |
| `ui.adminApiUrl` | `""` | URL nginx proxies `/api` to. Defaults to the in-cluster admin API Service. |
| `ui.service.type` | `ClusterIP` | Service type. |
| `ui.service.port` | `3000` | Service port. |
| `ui.service.nodePort` | `""` | Fixed NodePort; only used when `service.type` is `NodePort`. |
| `ui.ingress.enabled` | `false` | Create an Ingress. |
| `ui.ingress.className` | `nginx` | Ingress class. |
| `ui.ingress.hosts` | see `values.yaml` | Host and path rules. |
| `ui.ingress.tls` | `[]` | TLS blocks. |
| `ui.httpRoute.enabled` | `false` | Create a Gateway API `HTTPRoute`. Requires the Gateway API CRDs and a Gateway controller. |
| `ui.httpRoute.apiVersion` | `""` | Pin the Gateway API version. Empty auto-detects `v1`, then `v1beta1`, falling back to `v1`. |
| `ui.httpRoute.parentRefs` | one `stellarcd-gateway` ref | Gateways the route attaches to. At least one is required. |
| `ui.httpRoute.hostnames` | `[stellarcd.example.com]` | Hostnames the route matches. |
| `ui.httpRoute.rules` | `[]` | Routing rules. Empty renders a single `PathPrefix: /` rule to the dashboard; a rule without `backendRefs` gets that same default. |
| `ui.httpRoute.annotations` / `ui.httpRoute.labels` | `{}` | Extra metadata on the route. |
| `ui.virtualService.enabled` | `false` | Create an Istio `VirtualService`. Requires the Istio CRDs and a control plane. |
| `ui.virtualService.apiVersion` | `""` | Pin the Istio API version. Empty auto-detects `v1`, then `v1beta1`, then `v1alpha3`, falling back to `v1beta1`. |
| `ui.virtualService.hosts` | `[stellarcd.example.com]` | Hosts the route matches. Required and must not be empty. |
| `ui.virtualService.gateways` | one `istio-system/stellarcd-gateway` ref | Gateways to bind to. The reserved name `mesh` also applies the route to sidecar-to-sidecar traffic. Empty means mesh-internal only. |
| `ui.virtualService.exportTo` | `[]` | Namespaces the config is visible to. Empty uses the mesh default. |
| `ui.virtualService.http` | `[]` | HTTP routes. Empty renders a single `prefix: /` route to the dashboard; a route without a `route` block gets that same destination. |
| `ui.virtualService.annotations` / `ui.virtualService.labels` | `{}` | Extra metadata on the VirtualService. |

### RBAC and service account

| Key | Default | Description |
| --- | --- | --- |
| `serviceAccount.create` | `true` | Create the controller ServiceAccount. |
| `serviceAccount.name` | `""` | Name; generated from the release when empty. |
| `serviceAccount.annotations` | `{}` | Annotations, e.g. IRSA or Workload Identity bindings. |
| `rbac.create` | `true` | Create the roles and bindings the controller needs. |
| `rbac.tenantRoles.create` | `true` | Create `stellarcd-tenant-admin` / `stellarcd-tenant-viewer`, which the Universe controller binds into tenant namespaces. |
| `rbac.crdHelperRoles.create` | `false` | Create per-kind admin/editor/viewer ClusterRoles for delegating human access. |
| `rbac.metricsReader.create` | `true` | Create a ClusterRole granting GET on `/metrics`. |

### Network policy

| Key | Default | Description |
| --- | --- | --- |
| `networkPolicy.enabled` | `false` | Restrict ingress to the metrics and admin API ports. Needs a CNI that enforces NetworkPolicy — minikube's default bridge CNI does not. |
| `networkPolicy.metricsNamespaceSelector` | `matchLabels: {metrics: enabled}` | Namespaces allowed to scrape metrics. |

### Exposing the dashboard

Four mutually independent options, all off by default:

| | Use when |
| --- | --- |
| `ui.service.type: NodePort` / `LoadBalancer` | Local clusters. `values-minikube.yaml` uses NodePort 30300. |
| `ui.ingress.enabled` | You run an Ingress controller. |
| `ui.httpRoute.enabled` | You run a Gateway API controller. |
| `ui.virtualService.enabled` | You run Istio. |

Any combination can be on at once while migrating, but leaving several enabled permanently means several independent paths to the same Service.

Both the `HTTPRoute` and the `VirtualService` resolve their API version against the cluster rather than hardcoding it, since clusters still on the older CRDs are common. `helm template` reports no cluster capabilities, so offline it renders `v1` for the `HTTPRoute` and `v1beta1` for the `VirtualService` — the version each API has served longest. Pin `ui.httpRoute.apiVersion` / `ui.virtualService.apiVersion` to override.

```bash
helm upgrade --install stellarcd charts/stellarcd -n stellarcd-system \
  --set ui.httpRoute.enabled=true \
  --set ui.httpRoute.parentRefs[0].name=my-gateway \
  --set ui.httpRoute.parentRefs[0].namespace=gateway-system \
  --set ui.httpRoute.hostnames[0]=stellarcd.example.com
```

```bash
helm upgrade --install stellarcd charts/stellarcd -n stellarcd-system \
  --set ui.virtualService.enabled=true \
  --set ui.virtualService.gateways[0]=istio-system/my-gateway \
  --set ui.virtualService.hosts[0]=stellarcd.example.com
```

The dashboard namespace also needs sidecar injection or an ambient waypoint for the mesh to apply the route — a `VirtualService` alone does not put the workload in the mesh.

## Security notes

- **The admin API and dashboard are unauthenticated.** Both stay `ClusterIP` by default. Put an authenticating proxy in front before exposing either one — via the Ingress (`auth-url` annotations), an ExtAuth-capable Gateway, an `HTTPRoute` filter, or an Istio `RequestAuthentication` plus `AuthorizationPolicy`. Routing resources move traffic; none of them authenticate it.
- **Metrics are protected by default** (`controller.metrics.secure=true`). `values-minikube.yaml` turns this off for local convenience; do not carry that into a shared cluster.
- **`stellarcd-tenant-admin` deliberately excludes Secrets**, which hold the cloud credentials and Git tokens injected into Terraform runs. Grant Secret access per namespace where a tenant genuinely needs it.
- **The tenant ClusterRole names are cluster-global constants**, not release-prefixed — they are pinned by `resourceNames` on the manager's `bind` permission. Two releases of this chart in one cluster will contend over them.
