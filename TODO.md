# TODO

Design decisions and rationale are documented in DESIGN.md.

---

## Recent changes (v0.3.0)

- [x] **Bug fixes** — List() filters terminal instances for GC, Delete()
  handles terminating/unhealthy correctly, Get() preserves transient errors,
  CapacityTypeLabelKey always set
- [x] **ImageID tagging** — Instances tagged at launch with image ID/family,
  read back in nodeClaimFromInstance, enables IsDrifted image detection
- [x] **Dotenv configuration** — `.env` for project defaults, `.env.local`
  (gitignored) for secrets/overrides. Loaded by godotenv in lambdactl and
  by `Taskfile.yml` dotenv. `task set-version` syncs to Helm chart.
- [x] **Provider test coverage** — 38 tests covering all CloudProvider methods,
  validation, edge cases, and utility functions

## Unit test coverage — `internal/provider/` (38 tests)

- [x] **Create** — happy path, no-limit, capacity error, non-capacity error,
  empty IDs, GetInstance failure, idempotent-by-tag, ImageID tag,
  missing nodeClassRef, wrong GVK, no SSH keys
- [x] **Delete** — not found, terminated, terminating, unhealthy, API error
- [x] **Get** — happy path, invalid ID, not found, transient error, list fallback
- [x] **List** — happy path, cluster filter, terminal instance filter
- [x] **IsDrifted** — region, instance type, image, empty-ImageID backwards compat,
  nodeclass not found
- [x] **nodeClaimFromInstance** — providerID, labels, zone, allocatable, zero specs,
  capacity type label, ImageID from tag
- [x] **GetInstanceTypes / instanceTypeFromItem** — offerings, requirements,
  architecture detection (gh200 arm64 vs amd64), pricing, regions, no-regions fallback
- [x] **buildLaunchRequest** — full spec (firewall, publicIP, pool, custom tags,
  image ID vs family), missing region/instanceType validation
- [x] **Utilities** — sanitizeHostname (7 cases), sanitizeTagKey (8 cases),
  RepairPolicies

---

## Cleanup — remove redundant scripts

All shell scripts removed. lambdactl and Taskfile cover everything.

- [x] **Deleted `generate-kubeconfig.sh`** → `lambdactl k8s user create`
- [x] **Deleted `rotate-kubeconfig.sh`** → `lambdactl k8s user rotate`
- [x] **Deleted `cleanup-kubeconfig.sh`** → `lambdactl k8s user cleanup`
- [x] **Deleted `examples/deploy.sh`** → `lambdactl k8s deploy`
- [x] **Deleted `examples/bootstrap-controller.sh`** → `lambdactl k8s bootstrap`
- [x] **Deleted `build.sh`** → `task build`
- [x] **Deleted `scripts/set-version.sh`** + `scripts/` dir → `task set-version`
- [x] **Created `Taskfile.yml`** — `build`, `build-local`, `test`, `test-race`,
  `vet`, `set-version`, `helm-template`, `run`. Reads `.env` + `.env.local`
  via task dotenv.

---

## Infrastructure

- [ ] **Image repository** — Move from personal Docker Hub (`apsu/lambda-karpenter`)
  to an org registry. Not a code change.

- [ ] **E2E / integration tests** — Kind cluster + Karpenter + mock Lambda API
  server. Blocked on CI infrastructure.

- [ ] **Documentation** — Clarify deployment sequence (bootstrap → kubeconfig →
  deploy → apply). Now that lambdactl covers the full lifecycle, docs can
  reference `lambdactl k8s` commands instead of shell scripts.
