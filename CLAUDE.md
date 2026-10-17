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
  engine: terraform            # terraform | opentofu  (does init/plan/apply, owns the state)
  engineVersion: "1.9.5"
  terragrunt:                  # optional
    enabled: true
    version: "0.67.x"
    mode: run-all              # unit | run-all
    excludedUnits: []          # proposal, to confirm
```

Valid combinations: Terraform alone, OpenTofu alone, Terragrunt + Terraform, Terragrunt + OpenTofu.
The current `executor` enum (`terraform;terragrunt`) is wrong and must be replaced.

### Astral spec (target)
`galaxyRef`, `gitRepository{url, revision, credentialsSecretRef}`, `path`, `engine`, `engineVersion`,
`terragrunt{...}`, `interval` (default 5m), `cloudCredentialsSecretRef`, `syncPolicy` (`Manual` | `Auto`),
`allowDestroy` (default false), `suspend`, `flareHistoryLimit` (default 20, to confirm).

### Astral status (target)
Split progress from result. Do NOT keep a single `phase` mixing both.
- `syncStatus`: `Synced` | `OutOfSync` | `Unknown` (alignment with Git)
- `phase`: `Idle` | `Planning` | `Applying` | `Failed` (what is running)
- `drift`: counts to create / update / destroy + detection time
- `units[]`: run-all only. Per unit: relative path, syncStatus, drift, last Flare
- `lastSyncedRevision`, `lastSyncTime`, `lastFlareRef`, `observedGeneration`, `conditions`

In run-all, the Astral takes the worst status of its units.

### Flare (target)
Spec: `astralRef`, `type` (`Plan` | `Apply`), `scope` (`all` | `unit`), `unitPath` (required if scope=unit),
`trigger` (`Schedule` | `GitChange` | `Manual` | `Drift`), `revision`, `initiatedBy`, `planRef` (Apply only).
Status: `phase` (`Pending` | `Running` | `Succeeded` | `Failed` | `Cancelled`), `step`, `jobName`,
`startedAt`, `finishedAt`, `planSummary`, `resources[]` (with their unit), `unitResults[]`, `message`.

## Architecture Decisions
1. **CRD-driven.** All configuration lives in Custom Resources. No local config file.
2. **One Kubernetes Job per Flare.** The controller NEVER runs terraform/tofu/terragrunt itself
   (no `os/exec` of IaC binaries in the manager). It creates a Flare, creates a Job owned by the Flare,
   watches it, and reports results. This keeps the controller light and restartable: running Jobs
   survive a controller restart and are picked up again through their Flares.
3. **Job content.** Image with the engine at `engineVersion`, plus Terragrunt if enabled. Git secret
   mounted as a file, cloud secret injected as environment variables. `backoffLimit: 0` (never retry an
   Apply automatically). `activeDeadlineSeconds` set (default to confirm).
4. **Command selection inside the Job:**
    - no Terragrunt: engine commands in `path`
    - Terragrunt, mode `unit`: `terragrunt plan/apply` in `path`
    - Terragrunt, mode `run-all`, scope `all`: `terragrunt run-all plan/apply` in `path`, excluded units skipped
    - Terragrunt, scope `unit`: plain `terragrunt plan/apply` in `path/unitPath`
5. **Plans are saved** and converted to JSON (`resource_changes`) to compute drift, one per unit in run-all.
6. **RBAC.** The controller needs create/get/list/watch/delete on `batch/jobs` and get on `pods/log`,
   on top of its own CRDs and read access to Secrets.

## Business Rules (do not break)
- **One active Flare per Astral**, whatever its scope. A unit-scoped run blocks run-all and vice versa.
  Scheduled Flares are skipped while one is active. Manual requests are rejected (queueing: to confirm).
  This lock adds to the backend state lock, it does not replace it.
- **Automatic Flares** (interval, new commit, Astral creation) always use scope `all`.
  Only manual Flares may target a single unit.
- **Inheritance:** effective value = Astral value, else Galaxy `defaults`, else code default.
  `allowDestroy` and `terragrunt.excludedUnits` are never inherited.
- **Destroy protection:** if a plan destroys anything (replacements included, to confirm):
  `Manual` requires explicit user confirmation, `Auto` without `allowDestroy` does NOT apply and leaves
  the Astral OutOfSync with a clear message, `Auto` with `allowDestroy` applies.
- **Sync policy:** `Manual` only plans, apply waits for a user. `Auto` chains plan then apply on drift.
- A Universe rejects Astrals using a non-allowed engine, or Terragrunt when it is not allowed.

## Migration From the Current Code
The repo currently has a single kind `StellarApp` with an `executor` enum and a mixed `phase`.
Decided changes, in this order:
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
- How the Job returns its result: pod writes the Flare status, result ConfigMap, or controller parses logs.
- Log retention after the Job pod is gone.
- New commit detection: lightweight polling (`ls-remote`) vs incoming webhook.
- Apply after a newer commit landed: apply the saved plan or re-plan first.
- Run-all failure semantics when only some units fail.
- `excludedUnits` option: keep or drop.
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