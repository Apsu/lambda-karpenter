# lambda-karpenter

Karpenter cloud provider for Lambda Cloud. Provisions and deprovisions Lambda Cloud
GPU instances as Kubernetes nodes. Targets Karpenter v1.9.0.

## Overview

Two binaries:

- **`cmd/manager`** — Karpenter controller that runs in-cluster.
- **`cmd/lambdactl`** — CLI for the full cluster lifecycle: launch a controller node,
  extract kubeconfig, deploy the stack, manage users, and interact with the Lambda API.

## Configuration

All configuration is driven by environment variables. The project uses a two-file
dotenv convention:

| File | Purpose | Committed |
|---|---|---|
| `.env` | Project defaults (`VERSION`, `IMAGE_REPO`) | Yes |
| `.env.local` | Personal overrides and secrets | No (gitignored) |

Both files are loaded automatically by `lambdactl` (via godotenv) and by the
`Taskfile.yml` (via task dotenv). Explicit environment variables always take
precedence.

Create your `.env.local`:

```bash
CLUSTER_NAME=my-cluster
LAMBDA_API_TOKEN=your-token-here
KUBECONFIG=./my-cluster.kubeconfig
```

## Getting started

### 1. Bootstrap a controller node

Copy the example bootstrap config and customize it:

```bash
mkdir -p configs
cp examples/bootstrap.yaml configs/bootstrap.yaml
cp examples/bootstrap-controller-cloud-init.yaml configs/
# Edit configs/bootstrap.yaml — set region, instance type, SSH key, etc.
```

Then launch the controller:

```bash
lambdactl k8s bootstrap \
  --config configs/bootstrap.yaml \
  --rke2-token <token>
```

The config file specifies region, instance type, SSH key, cloud-init template, and
other settings. All config fields can be overridden with CLI flags. See
`examples/bootstrap.yaml` for the full schema and `lambdactl k8s bootstrap -h` for
all options.

This will:
1. Launch the instance and wait for it to become active
2. SSH in and wait for RKE2 to generate `/etc/rancher/rke2/rke2.yaml`
3. Download and rewrite the kubeconfig (server address, cluster name)
4. Optionally render a LambdaNodeClass from a template

### 2. Deploy the stack

Install the GPU operator, lambda-karpenter Helm chart, and apply NodeClass + NodePool:

```bash
lambdactl k8s deploy \
  --cluster-name my-cluster \
  --nodeclass-file configs/lambdanodeclass.yaml \
  --nodepool-file configs/nodepool.yaml
```

`--image-tag` defaults to `$VERSION` from `.env`. `--cluster-name` defaults to
`$CLUSTER_NAME`. See `lambdactl k8s deploy -h` for all options.

### 3. Verify

```bash
lambdactl k8s status
lambdactl k8s nodeclaims
```

## lambdactl

### Lambda API commands

```
list-instances [--limit N]
get-instance --id <instance-id>
list-instance-types
list-images
get-image --id <id> [--family <f>] [--region <r>] [--arch <a>] [--latest]
launch [--config file.yaml] [--confirm] [flags]
terminate --id <instance-id> [--confirm]
```

### Cluster lifecycle (`k8s`)

```
k8s bootstrap      Launch controller, install RKE2, extract kubeconfig [--config]
k8s kubeconfig     Extract kubeconfig from existing remote RKE2 node
k8s deploy         Install GPU operator + lambda-karpenter + apply resources
```

### User management (`k8s user`)

```
k8s user create    Create per-user ServiceAccount + token kubeconfig
k8s user rotate    Rotate token in existing user kubeconfig
k8s user cleanup   Delete user ServiceAccount + ClusterRoleBinding
```

### Resource management (`k8s`)

```
k8s apply          Server-side apply resources (--nodeclass, --nodepool, --pod)
k8s delete         Delete resources (--nodeclass, --nodepool, --nodeclaim)
k8s status         Show LambdaNodeClass, NodePool, NodeClaim status
k8s nodeclaims     List NodeClaim details
k8s wait           Wait for NodeClaim to be ready (--nodeclaim, --timeout)
```

### Common flags

```
--token <token>             Lambda API token (or LAMBDA_API_TOKEN env var)
--token-file <path>         Read token from file (checks ./lambda-api.key as fallback)
--base-url <url>            Lambda API base URL (or LAMBDA_API_BASE_URL)
--kubeconfig <path>         Path to kubeconfig (or KUBECONFIG)
```

### Config files

Both `launch` and `k8s bootstrap` support `--config` for a YAML config file.
Config values are loaded first, then CLI flags override. See `examples/` for
templates:

- `examples/bootstrap.yaml` — bootstrap controller config
- `examples/launch.yaml` — standalone instance launch config
- `examples/bootstrap-controller-cloud-init.yaml` — cloud-init template for RKE2
- `examples/lambdanodeclass.yaml` — LambdaNodeClass template (Go `text/template`)

Copy examples to `configs/` (gitignored) and customize for your cluster.

## Development

### Taskfile

[Task](https://taskfile.dev) is used for build automation. It reads `.env` and
`.env.local` automatically.

```bash
task                    # build + test + vet
task build-go           # compile binaries to ./bin/
task build              # multi-arch Docker image (push)
task build-local        # local Docker image (no push)
task test               # go test ./...
task test-race          # go test -race ./...
task vet                # go vet ./...
task helm-template      # render Helm chart
task run                # run controller locally (needs LAMBDA_API_TOKEN, CLUSTER_NAME)
task set-version -- 0.4.0   # bump version everywhere
```

### Versioning

The version is defined in `.env` (`VERSION=0.3.0`) and synced to the Helm chart
by `task set-version`. This updates:

- `.env` — `VERSION=`
- `charts/lambda-karpenter/Chart.yaml` — `version` and `appVersion`
- `charts/lambda-karpenter/values.yaml` — `image.tag`

### Helm chart

The chart is at `charts/lambda-karpenter/`. It ships the LambdaNodeClass CRD and
upstream Karpenter CRDs in the Helm `crds/` directory (install-only, never deleted
on uninstall). Do not install the AWS Karpenter controller chart.

```bash
helm upgrade --install lambda-karpenter ./charts/lambda-karpenter \
  --namespace karpenter --create-namespace \
  --set config.clusterName=<cluster-name> \
  --set config.apiTokenSecret.name=lambda-api \
  --set config.apiTokenSecret.key=token
```

## Controller environment variables

These are set in the Helm chart values and consumed by `cmd/manager`:

| Variable | Default | Description |
|---|---|---|
| `LAMBDA_API_TOKEN` | (required) | Lambda Cloud API token |
| `PROVIDER_CLUSTER_NAME` | (required) | Cluster identifier used in instance tags |
| `LAMBDA_API_BASE_URL` | `https://cloud.lambda.ai` | Lambda API base URL |
| `LAMBDA_API_RPS` | `1` | Global API rate limit (requests/sec) |
| `LAMBDA_LAUNCH_MIN_INTERVAL_SECONDS` | `5` | Minimum seconds between launches |
| `INSTANCE_TYPE_CACHE_TTL` | `10m` | Instance type cache TTL |
| `LOG_DEV_MODE` | `false` | Set `true` for human-readable logs |

## GPU workloads

Install the NVIDIA GPU Operator with tolerations for Karpenter's unregistered taint
so device plugins start before the taint is removed:

```bash
helm upgrade --install gpu-operator nvidia/gpu-operator \
  --namespace gpu-operator --create-namespace \
  --version v25.10.1 \
  -f examples/gpu-operator-values.yaml
```

Since all Lambda Cloud instances are GPUs, the example NodePools do not apply a
GPU taint — the `nvidia.com/gpu` resource request is sufficient to gate GPU
workload scheduling. This avoids threading tolerations through every system
component (coredns, ingress, metrics-server, etc.).

## Notes

- Worker nodes join the cluster with `provider-id=lambda://<instance-id>`. The
  example cloud-init reads the instance ID from cloud-init metadata and strips
  dashes to match the API format.
- Lambda tag keys are normalized: `karpenter.sh/nodeclaim` becomes
  `karpenter-sh-nodeclaim` (lowercase, max 55 chars, must start with a letter).
- Lambda Cloud has no availability zones. The provider synthesizes a single zone
  per region (`us-east-3` becomes `us-east-3a`).
- Architecture is inferred from instance type name: `gh200` maps to `arm64`,
  everything else to `amd64`.
