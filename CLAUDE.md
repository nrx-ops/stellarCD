# Project Context: stellarCD

## Description
**stellarCD** is a GitOps Continuous Delivery tool inspired by ArgoCD, built for Infrastructure as Code.
It watches Git repositories (desired state), detects drift against real infrastructure, and reconciles
it by running Terraform or OpenTofu, optionally driven by Terragrunt.

It is a Kubernetes Operator built with Kubebuilder (layout v4). For Kubebuilder mechanics (generated
files, CLI commands, testing workflow), follow `AGENTS.md`. This file covers the product: domain model,
architecture decisions and business rules.

## Source of Truth for Specifications
Functional specifications live in Confluence, personal space `~nicolas.roux`, page **StellarCD > DESIGN**
(4SH DESIGN method: ENT- entities, UC- use cases, SCR- screens, RG- business rules, PRC- processes,
INT- integrations). The page **"Écarts code / specs - stellarCD"** lists known gaps between this code
and the specs. Specs are written in French. Code stays in English (see Language Rule).

When the specs and the code disagree, the specs describe the target. Do not silently "fix" the spec
by coding something else: flag the gap.

## Domain Model (target)
Four nested resources, cosmic naming. API group and version: `core.stellarcd.io/v1alpha1`
(group still under discussion, see Open Questions).

| Kind | Role | Scope |
|---|---|---|
| `Universe` | Tenant. Guardrails: allowed engines, Terragrunt allowed, access groups | Cluster (to confirm) |
| `Galaxy` | Group of Astrals (team or environment). Holds `defaults` inherited by its Astrals | Namespaced (to confirm) |
| `Astral` | One IaC project: a root module, a Terragrunt unit, or a Terragrunt directory run with run-all | Namespaced |
| `Flare` | One execution (Plan or Apply) of an Astral, run in a dedicated Kubernetes Job | Namespaced, owned by its Astral |

### Engine vs Terragrunt (important)
Terragrunt is a **wrapper**, not an alternative to Terraform/OpenTofu. Model them on two axes:

```yaml
spec:
  engine: Terraform            # Terraform | OpenTofu  (does init/plan/apply, owns the state)
  engineVersion: "1.16.6"
  terragrunt:                  # optional
    enabled: true
    version: "1.1.6"
    mode: RunAll               # Unit | RunAll
    excludedUnits: []          # kept (decision of 17/10/2026)
```

Valid combinations: Terraform alone, OpenTofu alone, Terragrunt + Terraform, Terragrunt + OpenTofu.
Enum values are CamelCase, like every other enum in the API. The engine lives on the Astral; there is
no `Terrarium` type any more: the name collides with an existing open-source project (decision of
17/10/2026). Terragrunt 1.x drives OpenTofu by default, so the runner always passes `--tf-path`.

### Astral spec
Decision of 17/10/2026: the fields already in the code are kept, and the specification is updated to
match them. That means `syncPolicy`, `allowDestroy`, `suspend` and `flareHistoryLimit` are NOT added.

`universeRef`, `galaxyRef`, `displayName`, `description`, `vcsRepository{provider, url, branch, path,
secretRef}`, `engine`, `engineVersion`, `terragrunt{...}`, `variables` (non-sensitive), `secrets[]`
(`{name, key, envPrefix}`; `envPrefix: ""` keeps the Secret's own key names, which cloud credentials
need), `workspaceName`, `enableLocking` (engine `-lock` only), `autoApply`, `planOnPR`, `applyOnMerge`,
`customHooks`, `dependsOn`, `driftDetection{enabled, interval}`.

### Astral status
Decision of 17/10/2026: `syncStatus` replaces `phase`. There is no `phase` on the Astral any more.
- `syncStatus`: `Synced` | `OutOfSync` | `Unknown` (alignment with Git, from the last successful plan
  or apply; a failed run says nothing about alignment and leaves it unchanged)
- `activeFlare`: the Flare whose Job is running, if any (this is how progress is shown)
- `drift`: `detected`, `toCreate`, `toUpdate`, `toDestroy`, `toReplace`, `lastChecked`, `lastDetected`
- `units[]`: RunAll only. Per unit: path, syncStatus, result, drift, lastFlare, message
- `lastSyncedRevision`, `lastSyncTime`, `lastFlareRef`, `lastFlareStatus`, `flareHistory`,
  `observedGeneration`, `conditions` (failures are reported on the `Reconciled` condition)

In RunAll, the Astral takes the worst status of its units. Each unit keeps the newest word any Flare
said about it, so a unit-scoped plan updates only its unit, and an excluded unit keeps its last result.

### Flare
Decision of 17/10/2026: the code contract and the specification contract are merged. Where both had a
field for the same thing, the code name is kept: `action` (spec `type`, with `Plan`, `Apply`, `Destroy`,
`Refresh`), `requestedBy` (spec `initiatedBy`), `startTime` / `completionTime` (spec `startedAt` /
`finishedAt`). The spec's per-resource list is `resourceChanges`, because `resources` already holds the
applied counts.

Spec: `universeRef`, `galaxyRef`, `astralRef`, `action`, `scope` (`All` | `Unit`), `unitPath` (required
if scope=Unit), `trigger` (`Manual` | `Schedule` | `GitChange` | `Drift`), `revision`, `planRef`,
`confirmDestroy`, `requestedBy`, `variables`, `autoApprove`, `approved`, `cancel`, `parallelism`,
`refreshBeforePlan`, `tags`, `timeoutSeconds`. CEL rules: unitPath required for scope Unit; only a
Manual Flare may use scope Unit.

Status: `phase` (`Pending` | `AwaitingApproval` | `AwaitingConfirmation` | `Running` | `Succeeded` |
`Failed` | `Cancelled`), `step` (`GitFetch` | `Init` | `Plan` | `Apply` | `Report`), `jobName`,
`attempt`, `lockAcquiredAt`, `revision`, `planSummary`, `resourceChanges[]` (address, action, unit; capped
at 200), `pendingDestroy[]`, `unitResults[]`, `message`, `startTime`, `completionTime`, `duration`,
`resources` (applied counts), `planOutput`, `applyOutput`, `errors`, `warnings`, `conditions`.

## Architecture Decisions
1. **CRD-driven.** All configuration lives in Custom Resources. No local config file.
2. **One Kubernetes Job per Flare.** The controller NEVER runs terraform/tofu/terragrunt itself
   (no `os/exec` of IaC binaries in the manager). It creates a Flare, creates a Job owned by the Flare,
   watches it, and reports results. This keeps the controller light and restartable: running Jobs
   survive a controller restart and are picked up again through their Flares.
   Implemented: `cmd/runner` + `internal/runner` run inside the Job (the only code that execs git and
   the engines); `internal/runspec` is the contract between the two sides and contains no exec code.
   The controller must never import `internal/runner`.
3. **Job content.** Runner image `Dockerfile.runner` (git, Terraform, OpenTofu, Terragrunt, each checked
   against its published SHA256SUMS). Git secret mounted as a file; a GitHub App is exchanged by the
   controller for a one-hour installation token first, so the App private key never reaches the pod.
   Cloud secrets injected as env vars from their Secret (never through the run request). No
   ServiceAccount token in the pod. `backoffLimit: 0`, `activeDeadlineSeconds` = `timeoutSeconds`
   (default 3600), non-root, read-only root filesystem.
4. **Command selection inside the Job:**
    - no Terragrunt: engine commands in `path`
    - Terragrunt, mode `unit`: `terragrunt plan/apply` in `path`
    - Terragrunt, mode `run-all`, scope `all`: `terragrunt run-all plan/apply` in `path`, excluded units skipped
    - Terragrunt, scope `unit`: plain `terragrunt plan/apply` in `path/unitPath`
5. **Plans are saved** and converted to JSON (`resource_changes`) to compute drift, one per unit in run-all
   (`terragrunt run --all --out-dir --json-out-dir --report-file`). Apply always applies the saved plan
   checked by the destroy protection, never a fresh one.
6. **RBAC.** The controller needs create/get/list/watch/delete on `batch/jobs` and get on `pods/log`,
   on top of its own CRDs and read access to Secrets.

## Business Rules (do not break)
- **One active Flare per Astral**, whatever its scope. A unit-scoped run blocks run-all and vice versa.
  Scheduled Flares are skipped while one is active. The lock is `Flare.status.lockAcquiredAt`, held
  while the Flare's Job runs (decision of 17/10/2026: never in controller memory). It is taken with an
  uncached read under a single Flare worker. A Flare that finds the lock taken currently WAITS (Pending,
  retried): the specification says manual requests are rejected, queueing is still to confirm.
  This lock adds to the backend state lock, it does not replace it.
- **Automatic Flares** (interval, new commit, Astral creation) always use scope `all`.
  Only manual Flares may target a single unit.
- **Inheritance:** effective value = Astral value, else Galaxy `defaults`, else code default.
  `allowDestroy` and `terragrunt.excludedUnits` are never inherited.
- **Destroy protection** (decision of 17/10/2026): an apply whose plan deletes or replaces anything
  ALWAYS waits for a user, there is no `allowDestroy`. The Flare stops in `AwaitingConfirmation` with
  `status.pendingDestroy`; setting `spec.confirmDestroy` to the Astral's name launches a second Job
  whose allowlist is exactly those resources: it re-plans and applies only if nothing else is destroyed.
  `autoApply` never chains an apply onto a destroying plan ("Auto-apply blocked: the plan destroys
  resources."). Replacements count as destructions.
- **Auto-apply:** `autoApply` (kept from the code instead of `syncPolicy`) chains an Apply onto a
  successful whole-Astral Plan that found changes, pinned to that plan's revision.
- **RunAll blocked by a failed unit:** a whole-Astral apply is refused while any unit is failed;
  plans and single-unit runs stay allowed. `autoApply` reports "Auto-apply blocked: a unit failed."
- A Universe rejects Astrals using a non-allowed engine, or Terragrunt when it is not allowed.

## Migration From the Current Code
Done: Universe, Galaxy, Astral and Flare exist and are reconciled; Flares run in Jobs; the engine model
is engine + Terragrunt. Still open: the API group, and removing `StellarApp` (and its dashboard views).
Original plan, kept for reference:
1. Settle the API group (see Open Questions) before touching anything.
2. Replace `StellarApp` with `Astral` using the target model above. No installation exists yet, so no
   conversion webhook is needed. Prefer the Kubebuilder CLI to scaffold the new kind
   (`kubebuilder create api --group core --version v1alpha1 --kind Astral`), then remove the
   `StellarApp` files and its `PROJECT` entry (the only acceptable manual edit of `PROJECT`).
3. Scaffold `Flare`, then implement the Flare Job execution, then the Astral reconcile loop.
4. Add `Galaxy` and `Universe` once the loop is stable on a single Astral.

## Operational Requirements
- **Secrets:** referenced via Kubernetes `Secret` objects only. Never log them, never put them in status,
  metrics labels, events or Flare results. Terraform `sensitive` values stay masked.
- **Observability:** `/metrics` (already served, secure by default) and `/version` (already served on the
  same address). Target business metrics: `stellarcd_astral_sync_status`, `stellarcd_astral_drift_resources`,
  `stellarcd_flare_total`, `stellarcd_flare_duration_seconds`, `stellarcd_git_fetch_errors_total`
  (names to confirm). No secrets or plan content in labels.
- **Structured logging** with controller-runtime `logr`.
- **Graceful shutdown:** on SIGTERM the manager stops cleanly. Running Jobs are not killed.
- **Finalizers:** deleting an Astral deletes its Flares and their Jobs. It never destroys infrastructure.

## Open Questions (ask, do not guess)
Do not invent behaviour for these. Leave a `TODO(spec):` comment and ask:
- API group: `core.stellarcd.io` (current) vs earlier `gitops` / `nrx-ops.io`.
- Universe cluster-scoped? Galaxy mapped to a namespace?
- How the Job returns its result. Implemented as: the controller parses the runner's log (step markers
  and a result block tagged with a per-Job nonce), so the pod needs no cluster credentials. To confirm.
- Log retention after the Job pod is gone. Implemented as: finished Jobs kept 24 h
  (`ttlSecondsAfterFinished`); the result itself is copied into the Flare status at once. To confirm.
- Runner image strategy. Implemented as: one image with pinned engine versions; a Flare asking for
  another version fails before init. To confirm (vs. downloading versions at run time).
- New commit detection: lightweight polling (`ls-remote`) vs incoming webhook.
- Apply after a newer commit landed. Implemented as: every Apply Job plans then applies its own saved
  plan; a chained auto-apply is pinned to the revision its plan ran. To confirm.
- Run-all failure semantics when only some units fail. Implemented as the ENT-Flare proposal: the Flare
  is Failed as soon as one unit fails, the other units' results are still reported. To confirm.
- Creating Astrals from the UI bypasses Git: allow, restrict, or generate a pull request instead.

## Go Development Guidelines
1. **Language Rule:** ALL source code, identifiers, comments, commit messages and repo documentation are
   in English, regardless of the language of the prompt or of the specs.
2. **Simplicity first:** simple, flat, idiomatic Go. Composition over inheritance. Interfaces only where
   needed (for example to fake the Kubernetes client or Git access in tests).
3. **Errors:** always handle explicitly, wrap with context (`fmt.Errorf("failed to create flare job: %w", err)`),
   no `panic` in business logic.
4. **Context:** every function doing I/O or calling the API server takes `context.Context` first.
5. **Think schema first:** design the CRD spec and status before writing reconcile logic.
6. Prefer the standard library.

## Testing Strategy
- **Controllers:** `envtest` integration tests. Assert on created Jobs and Flare/Astral status,
  not on IaC output (Jobs do not run in envtest).
- **Business logic** (command selection, inheritance, destroy protection, drift parsing from plan JSON):
  table-driven unit tests.
- **E2E:** isolated Kind cluster, as described in `AGENTS.md`.

## Instructions for the AI Assistant
- Read this file, then `AGENTS.md`, before changing code.
- Follow the target model and business rules above. If a request contradicts them, say so before coding.
- Never reintroduce the `executor` enum or `os/exec` of IaC binaries in the controller.
- Keep explanations of Go architectural choices short and clear.