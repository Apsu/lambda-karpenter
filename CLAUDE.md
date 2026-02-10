# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Karpenter cloud provider for Lambda Cloud. Provisions and deprovisions Lambda Cloud GPU instances as Kubernetes nodes. Targets **Karpenter v1.9.0**, written in **Go 1.25**.

## Build & Run

```bash
# Build the controller manager
go build -o bin/manager ./cmd/manager

# Run the controller manager locally (requires env vars)
LAMBDA_API_TOKEN=... PROVIDER_CLUSTER_NAME=... go run ./cmd/manager

# Run the CLI tool
go run ./cmd/lambdactl list-instance-types

# Run all tests
go test ./...

# Run tests for a specific package
go test ./internal/provider/...
go test ./internal/lambdaclient/...

# Build Docker image
docker build -t lambda-karpenter .
```

## Architecture

Two binaries:
- **`cmd/manager`** — Karpenter controller-manager (production). Creates the Lambda API client, rate limiter, instance type cache, instance list cache, and CloudProvider, then starts the Karpenter operator with all standard controllers.
- **`cmd/lambdactl`** — Read-only CLI for API validation and K8s resource management. Uses server-side apply for `k8s apply`.

### Core packages (`internal/`)

- **`provider/`** — Implements `cloudprovider.CloudProvider` interface (Create, Delete, Get, List, GetInstanceTypes). Delegates to Karpenter core for scheduling limits. Uses `InstanceLister` interface for cached instance listing.
- **`provider/metrics.go`** — Prometheus counters for instance create/delete.
- **`lambdaclient/`** — HTTP client for Lambda Cloud API v1. Includes retry logic (exponential backoff + jitter for 429/5xx), rate limiting, and context-aware retry loops. Also contains the instance type cache (singleflight + stale-while-revalidate) and instance list cache (singleflight + TTL).
- **`lambdaclient/metrics.go`** — Prometheus counters and histograms for API request count/latency.
- **`ratelimit/`** — Two-tier rate limiter: global token bucket (default 1 rps) + launch-specific minimum spacing (default 5s) via channel-based semaphore.
- **`config/`** — Loads all settings from environment variables (no config files).
- **`controller/`** — LambdaNodeClass reconciler that validates resources, sets Ready condition, populates image resolution status, and sets LastValidatedAt.

### CRD

- **API group**: `karpenter.lambda.cloud`, version `v1alpha1`
- **Kind**: `LambdaNodeClass` (cluster-scoped, shortname `lnc`)
- Types defined in `api/v1alpha1/`. DeepCopy methods are hand-written (no code generation).
- CRD YAML is hand-authored and bundled in the Helm chart under `charts/lambda-karpenter/crds/` (Helm special directory — install-only, never deleted on uninstall).

### Key patterns

- **Provider ID format**: `lambda://<instance-id>` with fallback resolution by hostname/name.
- **Tag-based idempotency**: Instances tagged with `karpenter-sh-nodeclaim`, `karpenter-sh-nodepool`, `karpenter-sh-cluster`. On Create, existing instances are checked by tag before launching new ones.
- **Tag key sanitization**: Lambda tag keys are normalized (lowercase, `/._` replaced with `-`, max 55 chars, must start with letter). E.g., `karpenter.sh/nodeclaim` → `karpenter-sh-nodeclaim`.
- **Instance status mapping**: `terminated`/`preempted`/`unhealthy`/`terminating` are terminal states.
- **Instance list caching**: All `ListInstances` calls go through `InstanceListCache` (5s TTL + singleflight) to avoid O(n) list calls per reconciliation.

### Helm chart

Located at `charts/lambda-karpenter/`. Bundles both the LambdaNodeClass CRD and upstream Karpenter NodeClaim/NodePool CRDs in `crds/`. No separate CRD install path.

### Required environment variables

- `LAMBDA_API_TOKEN` — Lambda Cloud API token
- `PROVIDER_CLUSTER_NAME` — Cluster identifier used in instance tags

### Optional environment variables

- `LAMBDA_API_BASE_URL` (default `https://cloud.lambda.ai`)
- `LAMBDA_API_RPS` (default 1)
- `LAMBDA_LAUNCH_MIN_INTERVAL_SECONDS` (default 5)
- `INSTANCE_TYPE_CACHE_TTL` (default 10m)
- `LOG_DEV_MODE` (default `false`) — set `true` for human-readable dev logs
