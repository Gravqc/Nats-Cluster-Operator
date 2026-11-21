# Contributing

Read AGENTS.md for repository instructions used by coding assistants. Human contributors should start with README.md, docs/scope.md and docs/usage.md.

Start with a reproducible issue or a small design proposal for a lifecycle operation. Explain the desired behavior, affected Kubernetes/NATS versions, failure modes and how to reproduce it with Kind. Preserve namespace isolation and retained storage.

Use Go 1.24, Docker, kubectl, Helm and Kind. Run `make generate manifests test`, `helm lint charts/nats-cluster-operator`, and the quickstart/e2e commands in README. API changes must include regenerated CRDs and deepcopy code. Include a focused regression check for any change to reconciliation, rollout ordering or storage safety.

Keep application stream policy separate from server lifecycle. New admin operations need explicit ownership, retry and interruption behavior. Share measured results against the Helm baseline before adding automation.

The Go module and API group use provisional example names until the first public release. No public project repository or issue tracker has been configured yet.
