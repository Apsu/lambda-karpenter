# TODO

Design decisions and rationale are documented in DESIGN.md.

---

- [ ] **Image repository** — Move from personal Docker Hub (`apsu/lambda-karpenter`)
  to an org registry. Not a code change.

- [ ] **E2E / integration tests** — Kind cluster + Karpenter + mock Lambda API
  server. Blocked on CI infrastructure.

- [ ] **Documentation** — Clarify deployment sequence (bootstrap controller → scp
  kubeconfig → deploy GPU operator + lambda-karpenter → apply NodeClass +
  NodePool). Document how to regenerate `lambdanodeclass.generated.yaml`.
