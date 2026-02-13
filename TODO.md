# TODO

Design decisions and rationale are documented in DESIGN.md.

---

## Open

- [ ] **Version flag** — Add `lambdactl --version` using Kong's version support
  and build-time `ldflags`.

- [ ] **Image repository** — Move from personal Docker Hub (`apsu/lambda-karpenter`)
  to an org registry. Not a code change.

- [ ] **E2E / integration tests** — Kind cluster + Karpenter + mock Lambda API
  server. Blocked on CI infrastructure.

---

## Completed

### v0.5.0 (in progress)

- [x] **InstanceTypeSelector** — NodeClass `instanceTypeSelector` filters eligible
  instance types in `GetInstanceTypes()`.
- [x] **Filesystem mounts** — NodeClass `fileSystemNames` and `fileSystemMounts`
  wired through to `LaunchRequest`.
- [x] **Image family resolution** — Controller resolves `image.family` + region to
  a concrete image ID via the Lambda Images API (`ImageCache`).
- [x] **UserData from ConfigMap** — `userDataFrom` sources userData from ConfigMap
  references with hash-based drift detection.
- [x] **EKS hybrid support** — Helm chart renders `nodeadm`-based userData
  templates for EKS hybrid clusters via `cluster.type: eks-hybrid`.
- [x] **Lambda API commands** — Added `ssh-key`, `filesystem`, and `firewall`
  command groups to lambdactl.
- [x] **Trim k8s commands** — Removed redundant `k8s apply`, `k8s delete`, and
  `k8s nodeclaims` commands (overlapped with kubectl/Helm).

### v0.4.0

- [x] **Kong CLI refactor** — Migrated lambdactl from manual `flag.NewFlagSet` +
  `switch` dispatch to `alecthomas/kong` struct-tag CLI framework. Removed all
  manual flag parsing, usage functions, and custom `stringSlice` type.
- [x] **Bootstrap config file** — `--config` support with YAML overlay pattern
  (config file -> CLI flag overrides -> defaults -> validation). Example at
  `examples/bootstrap.yaml`.
- [x] **Taskfile build-go** — `task build-go` compiles binaries to `./bin/`.
- [x] **Script cleanup** — All shell scripts replaced by lambdactl and Taskfile.

### v0.3.0

- [x] **Bug fixes** — List() filters terminal instances, Delete() handles
  terminating/unhealthy, Get() preserves transient errors.
- [x] **ImageID tagging** — Instances tagged at launch with image ID/family,
  enables IsDrifted image detection.
- [x] **Dotenv configuration** — `.env` for project defaults, `.env.local`
  (gitignored) for secrets/overrides.
- [x] **Provider test coverage** — 38 tests covering all CloudProvider methods.
- [x] **Taskfile** — Build, test, vet, helm-template, set-version, run.
