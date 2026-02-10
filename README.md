# lambda-karpenter

Karpenter provider for Lambda Cloud (MVP). See `spec.md` for requirements.

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

Install the provider controller using the Helm chart. This chart also installs the Karpenter
`NodeClaim` and `NodePool` CRDs. Do not install the AWS Karpenter controller chart.

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

## CLI (Lambda API validation)

The `lambdactl` CLI is read-only by default and helps validate API connectivity before running Karpenter.

```bash
go run ./cmd/lambdactl list-instance-types
```

Optional flags:

```bash
--token <token>
--token-file <path>
--base-url https://cloud.lambda.ai
```

Commands:

```bash
list-instances
get-instance --id <instance-id>
list-instance-types
list-images
get-image --id <image-id> [--region us-east-3] [--arch arm64] [--latest]
launch --confirm [--config file.yaml] [flags]
terminate --id <instance-id> --confirm
k8s <command> [flags]
```

K8s subcommands:

```bash
k8s apply --nodeclass lambdanodeclass.yaml --nodepool nodepool.yaml
k8s delete --nodeclass lambda-gh200 --nodepool gh200-pool
k8s status
k8s nodeclaims
k8s wait --nodeclaim <name> --timeout 10m
```

Example `--config` file:

```yaml
name: test-node
hostname: test-node
region: us-west-1
instanceType: gpu_1x_a10
imageFamily: lambda-stack-24-04
sshKeyNames:
  - default
tags:
  environment: dev
```

## Notes

- This repository targets Karpenter v1.9.0.
- Lambda tag keys are normalized to match Lambda's tag constraints. Keys like `karpenter.sh/nodeclaim` are converted to `karpenter-sh-nodeclaim`.
- `lambdactl terminate` is intentionally disabled with an unconditional exit for safety.

## Status

Initial Karpenter CloudProvider implementation is included, but image resolution and advanced scheduling features are not yet implemented.
