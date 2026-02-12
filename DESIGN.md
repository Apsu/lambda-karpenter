# Design Decisions

Key architectural and design decisions for the Lambda Cloud Karpenter provider.
See CLAUDE.md for build instructions and package layout.

---

## CloudProvider

### No provider-side NodePool limit enforcement

Karpenter core enforces `spec.limits` in the scheduler and provisioner *before*
`CloudProvider.Create()` is called. The AWS provider does not do its own limit
checking either. Provider-side enforcement was removed to match upstream
semantics and avoid double-counting.

### Synthetic availability zones

Lambda Cloud has no availability zones. The provider synthesizes a single zone
per region as `regionName + "a"` (e.g. `us-east-3a`). This satisfies
Karpenter's topology-aware scheduling without misrepresenting the infrastructure.

### Architecture detection

Instance types containing `gh200` in the name are reported as `arm64`; all
others default to `amd64`. This heuristic covers Lambda's current GPU lineup.
The `kubernetes.io/arch` and `kubernetes.io/os` labels are set on every
instance type offering.

### Node addresses not populated

Karpenter v1.9.0's `NodeClaimStatus` does not have an `Addresses` field.
Nodes register with the API server via hostname matching during the kubelet
join process. No provider-side address population is needed or possible.

### Image resolution is pass-through

`spec.image.id` → `status.resolvedImageID` and `spec.image.family` →
`status.resolvedImageFamily` are echoed directly. Full API-based resolution
(family → ID lookup) is deferred until Lambda exposes an Images API.

### Tag-based idempotency

Instances are tagged with `karpenter-sh-nodeclaim`, `karpenter-sh-nodepool`,
and `karpenter-sh-cluster`. On `Create()`, existing instances are checked by
tag before launching new ones, preventing duplicate launches on retry.

Tag keys are sanitized for the Lambda API: lowercased, `/._` replaced with `-`,
max 55 characters, must start with a letter.

### Instance status mapping

`terminated`, `preempted`, `unhealthy`, and `terminating` are treated as
terminal states. Drift detection compares region and instance type against the
LambdaNodeClass spec.

---

## CRDs

### Hand-authored, no code generation

CRD YAML and DeepCopy methods are hand-written. The schema is simple enough
that controller-gen adds complexity without value. CRDs are bundled in the Helm
`crds/` directory (install-only, never deleted on uninstall).

### InstanceTypeSelector declared but rejected

The `instanceTypeSelector` field exists in the LambdaNodeClass type for future
use but validation rejects any non-empty value with a clear error. This avoids
silent misconfiguration while preserving the schema for forward compatibility.

---

## Instance Type Selection

### Type-agnostic NodeClass

`LambdaNodeClass.spec.instanceType` is optional. When omitted, the NodeClass
defines *how* instances join the cluster (region, SSH keys, image, userData)
while the NodePool defines *what* instance types to use via
`node.kubernetes.io/instance-type` requirements. Multiple NodePools can share
a single NodeClass, each selecting different instance types.

When `instanceType` is set, `GetInstanceTypes` filters to only that type and
`buildLaunchRequest` falls back to it — fully backward compatible.

### userData templating

`spec.userData` supports Go `text/template` actions rendered at launch time.
Available variables: `{{.Region}}`, `{{.ClusterName}}`, `{{.NodeClaimName}}`,
`{{.ImageFamily}}`, `{{.ImageID}}`. Strings without `{{` pass through unmodified
(fast path). Node labels (instance-type, region, zone, capacity-type) are set by
the provider on the NodeClaim and propagated to the Node by Karpenter — they do
not need to be set in userData.

## GPU Operator Integration

### No GPU taint

Since all Lambda Cloud instances are GPUs, NodePools do not apply an
`nvidia.com/gpu:NoSchedule` taint. The GPU resource request is sufficient to
gate scheduling. Omitting the taint avoids threading tolerations through every
system component (coredns, ingress-nginx, metrics-server, the GPU Operator
Deployment itself, etc.). In a mixed GPU/CPU fleet the taint would make sense,
but Lambda's public cloud is GPU-only.

### Karpenter startup toleration

The GPU Operator's DaemonSets and NFD worker need a
`karpenter.sh/unregistered:NoSchedule` toleration so they can schedule on nodes
that haven't finished registering yet.

### Consolidation

GPU NodePools use `WhenEmpty` consolidation to avoid disrupting running GPU
workloads. `consolidateAfter: 60m` gives users time to submit follow-up jobs.

---

## Cluster Configuration (`cluster.yaml`)

### Bootstrap → deploy handoff

`lambdactl k8s bootstrap` writes a `cluster.yaml` file (plus kubeconfig) and a
`lambda-karpenter-values.yaml` file to a cluster directory
(`configs/<cluster-name>/` by default). The cluster config captures all facts
discovered during bootstrap: cluster name, region, controller IPs, instance type,
and join token. The Helm values file is pre-populated with the correct
`cluster.type`, controller IP, join token, node class, and node pool settings
so a single `helm install -f` deploys everything.

### Multi-cluster separation

Each cluster gets its own directory under `configs/`:

```
configs/
  cluster-a/
    cluster.yaml
    kubeconfig
  cluster-b/
    cluster.yaml
    kubeconfig
```

The `--cluster-dir` flag on `bootstrap` and resource commands makes it easy to
manage multiple clusters from the same working directory.

### File permissions

`cluster.yaml` is written with `0600` permissions because it contains the
join token. The Lambda API token is never stored in `cluster.yaml`.

---

## RKE2 Bootstrap

### No version pin

The RKE2 installer (`get.rke2.io`) defaults to the stable channel. Users can
override with `INSTALL_RKE2_CHANNEL` or `INSTALL_RKE2_VERSION` environment
variables in the cloud-init template. Pinning to a specific version would
require manual updates and provides no benefit over the stable channel default.

### Node self-discovery

Worker nodes join the cluster using the RKE2 token + controller IP passed via
cloud-init user-data. This is the standard RKE2 join pattern.

---

## Deferred

### Image repository

The Docker image currently publishes to a personal Docker Hub account
(`apsu/lambda-karpenter`). Moving to an org registry is an operational
decision, not a code change.

### E2E / integration tests

Requires a kind cluster with Karpenter installed and a mock Lambda API server.
Deferred until unit test coverage is sufficient and CI infrastructure is in
place.

### Full documentation

- Deployment sequence: `lambdactl k8s bootstrap` → `lambdactl k8s deploy` →
  `lambdactl k8s apply`. See README.md for the current workflow.
