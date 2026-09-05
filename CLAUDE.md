# Project Context: stellarCD

## Description
**stellarCD** is a Continuous Delivery (GitOps) tool inspired by ArgoCD, but specifically designed for the Infrastructure as Code ecosystem (Terraform and Terragrunt).
It is a Kubernetes Operator. Its role is to monitor Git repositories (Desired State) and reconcile them with the actual state of the infrastructure by executing Terraform/Terragrunt commands via subprocesses.

## Tech Stack & Core Tools
- **Language:** Go (version 1.21+)
- **Paradigm:** Kubernetes Operator / Controller pattern.
- **Key Libraries:**
    - `sigs.k8s.io/controller-runtime` (Kubebuilder) for the reconciliation loop and CRD management.
    - `k8s.io/client-go` for interacting with the Kubernetes API.
    - `go-git` for fetching and polling Git states.
    - Standard `os/exec` for wrapper execution of `terraform` and `terragrunt` binaries.

## Kubernetes Native Architecture
1. **CRD-Driven Configuration:**
    - ALL configuration (target Git repositories, Terraform paths, intervals, credentials references) is stored in Kubernetes Custom Resources (e.g., `StellarApp`).
    - The application does not rely on a local config file. The K8s API is the single source of truth.
2. **The Reconciliation Loop:**
    - The core logic lives inside the K8s Controller `Reconcile()` function.
    - It handles K8s events and triggers the GitOps workflow: Git fetch -> Terraform Plan -> Terraform Apply.
    - CRD status subresources (`.status`) must be updated consistently to reflect the current state (e.g., `Syncing`, `Synced`, `Degraded`, `Applying`).
3. **Secrets Management:**
    - Sensitive data (cloud credentials, Git SSH keys/tokens) must be referenced via standard Kubernetes `Secret` objects. The controller will read these secrets at runtime to securely inject them into the Terraform execution environment as temporary environment variables.

## Operational & GitOps Requirements
1. **Concurrency & Locking:**
    - Terraform runs must be strictly serialized per environment/directory to avoid state corruption. The controller must handle locking mechanisms so we don't trigger concurrent runs on the same Terraform state.
2. **Observability:**
    - The application must expose a `/metrics` endpoint for Prometheus to track reconciliation times, Git sync status, and TF execution outcomes.
    - Use structured logging (`log/slog` or controller-runtime's `logr`). **Never** log sensitive secrets or credentials.
3. **Graceful Shutdown:**
    - Implement graceful shutdown to ensure running Terraform jobs are cleanly aborted or finished when the stellarCD process receives a SIGTERM.

## Go Development Guidelines
1. **Language Rule:**
    - ALL source code, variables, functions, structs, comments, commit messages, and documentation MUST be written in English, regardless of the language used in the prompt.
2. **Simplicity First (Idiomatic Go):**
    - Write simple, flat, and readable code. Favor composition over inheritance.
    - Use interfaces only when necessary (e.g., for mocking Git operations or shell executions in tests).
3. **Error Handling:**
    - Always return and handle errors explicitly (`if err != nil`).
    - No `panic` in business logic.
    - Wrap errors to preserve context: `fmt.Errorf("failed to fetch git repository: %w", err)`.
4. **Context (`context.Context`):**
    - Every function performing I/O, network calls, or subprocess executions must take a `context.Context` as its first parameter. This is critical for timeout management and cancellation of hanging Terraform commands.

## Testing Strategy
- **Controller Logic:** Use `envtest` (provided by controller-runtime) to write integration tests for the reconciliation loop against a local control plane.
- **Business Logic:** Use standard Table-Driven Tests. Isolate shell/git dependencies to keep unit tests fast and reliable.

## Instructions for the AI Assistant
- **Strict English Output:** Generate all code, variable names, and code comments in English.
- Always think about the data structures and K8s API schema (CRD spec) before writing execution logic.
- Stick to the Go standard library (stdlib) whenever possible.
- Provide brief, clear explanations for your Go architectural choices.
- When generating code, ensure it aligns strictly with the Kubebuilder / Operator SDK project layout.