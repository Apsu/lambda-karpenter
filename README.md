# lambda-karpenter

Karpenter cloud provider for Lambda Cloud. Provisions and deprovisions Lambda Cloud
GPU instances as Kubernetes nodes. Targets Karpenter v1.9.0.

## Quick start

1. Set env vars:

```bash
export LAMBDA_API_TOKEN=...
export PROVIDER_CLUSTER_NAME=...
```

2. Run manager:

```bash
go run ./cmd/manager
```

## Helm chart

Install the provider controller using the Helm chart. This chart also installs the
Karpenter `NodeClaim` and `NodePool` CRDs. Do not install the AWS Karpenter controller
chart. CRDs are shipped in the Helm `crds/` directory (install-only, never deleted on
uninstall).

```bash
helm upgrade --install lambda-karpenter ./charts/lambda-karpenter \
  --namespace karpenter --create-namespace \
  --set config.clusterName=<cluster-name> \
  --set config.apiTokenSecret.name=lambda-api \
  --set config.apiTokenSecret.key=token
```

Create the token secret:

```bash
kubectl -n karpenter create secret generic lambda-api \
  --from-literal=token=<your-token>
```

## GPU operator

If you're running GPU workloads, install the NVIDIA GPU Operator with tolerations for
Karpenter's unregistered taint so device plugins can start before the taint is removed.

```bash
GPU_OPERATOR_VERSION="${GPU_OPERATOR_VERSION:-v25.10.1}"

helm upgrade --install gpu-operator nvidia/gpu-operator \
  --namespace gpu-operator --create-namespace \
  --version "$GPU_OPERATOR_VERSION" \
  -f examples/gpu-operator-values.yaml
```

The example NodePool YAMLs apply an `nvidia.com/gpu:NoSchedule` taint. The GPU Operator
tolerates this by default. GPU workload pods must include a matching toleration (see
`examples/gpu-test-pod.yaml`).

## Bootstrap controller node

To launch a fresh controller node and bootstrap RKE2, use
`examples/bootstrap-controller-cloud-init.yaml`. This config:

- Installs and starts `rke2-server`
- Sets `cni: flannel`

Replace these placeholders before launch:

- `REPLACE_WITH_RKE2_TOKEN`

Helper script (launch + render cloud-init):

```bash
export RKE2_TOKEN=...
export PUBLIC_ENDPOINT=...       # optional; auto-resolved if unset
export REGION=us-east-3
export INSTANCE_TYPE=gpu_1x_gh200
export IMAGE_FAMILY=lambda-stack-24-04
export SSH_KEY_NAME=my-key
export CLUSTER_NAME=my-cluster
export SSH_USER=ubuntu           # optional, default ubuntu
export SSH_KEY_PATH=~/.ssh/id_rsa # optional, if not using ssh-agent
export RKE2_SERVER_ADDR=...       # optional; private IP for agents (auto-detected if unset)
export NODECLASS_OUT=./lambdanodeclass.generated.yaml

./examples/bootstrap-controller.sh
```

The script waits for SSH, downloads `/etc/rancher/rke2/rke2.yaml`, and rewrites the
API server address to use the public IP.

It also generates a worker NodeClass with the correct `server` and `token` values at
`$NODECLASS_OUT` (default `./lambdanodeclass.generated.yaml`).

## Deploy to cluster

Once you have a working kubeconfig:

```bash
export KUBECONFIG=./rke2.yaml
export LAMBDA_API_TOKEN=...
export CLUSTER_NAME=my-cluster

./examples/deploy.sh
```

## CLI (Lambda API validation)

The `lambdactl` CLI is read-only by default and helps validate API connectivity before
running Karpenter.

```bash
go run ./cmd/lambdactl list-instance-types
```

Optional flags:

```bash
--token <token>
--token-file <path>        # also checks ./lambda-api.key as fallback
--base-url https://cloud.lambda.ai
```

Commands:

```bash
list-instances [--limit N]
get-instance --id <instance-id>
list-instance-types
list-images
get-image --id <image-id> [--region us-east-3] [--arch arm64] [--latest]
launch --confirm [--config file.yaml] [flags]
terminate --id <instance-id> --confirm
k8s <command> [flags]
```

K8s subcommands (uses server-side apply):

```bash
k8s apply --nodeclass examples/lambdanodeclass.yaml --nodepool examples/nodepool.yaml
k8s delete --nodeclass lambda-gh200 --nodepool gh200-pool
k8s status
k8s nodeclaims
k8s wait --nodeclaim <name> --timeout 10m
```

## Environment variables

### Required

| Variable | Description |
|---|---|
| `LAMBDA_API_TOKEN` | Lambda Cloud API token |
| `PROVIDER_CLUSTER_NAME` | Cluster identifier used in instance tags |

### Optional

| Variable | Default | Description |
|---|---|---|
| `LAMBDA_API_BASE_URL` | `https://cloud.lambda.ai` | Lambda API base URL |
| `LAMBDA_API_RPS` | `1` | Global API rate limit (requests/sec) |
| `LAMBDA_LAUNCH_MIN_INTERVAL_SECONDS` | `5` | Minimum seconds between launches |
| `INSTANCE_TYPE_CACHE_TTL` | `10m` | Instance type cache TTL |
| `LOG_DEV_MODE` | `false` | Set `true` for human-readable (non-JSON) logs |

## Notes

- Nodes must join the cluster with a `provider-id` matching the Lambda instance ID
  (e.g., `lambda://<id>`). The provided cloud-init config reads the instance ID from
  cloud-init metadata and strips dashes to match the API format.
- Lambda tag keys are normalized to match Lambda's tag constraints. Keys like
  `karpenter.sh/nodeclaim` are converted to `karpenter-sh-nodeclaim`.
- `lambdactl terminate` is intentionally disabled with an unconditional exit for safety.
- This repository targets Karpenter v1.9.0.
