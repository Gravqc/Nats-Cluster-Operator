# Working in this repository

## Purpose and scope

This is an independent personal open-source project for provisioning NATS clusters and evaluating NATS-aware maintenance on Kubernetes. Use generic examples and project references. Start with README.md, docs/scope.md and docs/design.md. The implementation supports forward patches and adjacent-minor upgrades, with executed compatibility limited to the version pairs in docs/evaluation.md. Deferred features live in docs/operations.md and must be described as proposals until implemented and evaluated.

## Repository map

- api/v1alpha1 contains the API types and Kubebuilder validation markers.
- internal/controller contains reconciliation, resource generation, monitoring gates and unit tests.
- cmd/main.go starts the namespace-scoped operator; cmd/probe is the e2e message-integrity client.
- charts/nats-cluster-operator installs the controller, permissions and CRD.
- config/crd/bases contains the generated CRD; examples contains user-facing resources.
- scripts/local-up.sh and justfile manage the isolated Kind lab; tests/e2e.py verifies its initial lifecycle scenarios.
- tests/memory.py checks process memory policy. tests/upgrade_comparison.py compares pinned Helm and operator rollouts; tests/test_upgrade_comparison.py covers recovery-measurement edge cases.
- docs/evaluation.md and docs/local-evaluation.json record the last executed evaluation and its limits.

## Feature acceptance

Only add features with a concrete operational purpose and executed verification.
A CLI command existing upstream is not sufficient justification for an automatic
controller action. Unverified or unsuitable workflows stay documented in
docs/operations.md, without speculative API fields. Autoscaling and host page-cache
flushing are outside scope. Do not weaken health gates to make experiments pass.
Record negative or inconclusive comparisons as such; never assume Helm loses data.

## Changes and generated files

Inspect the working tree and relevant callers before editing. Keep changes small and preserve existing user work. Read the affected tests. Change API types and validation markers first, then run make generate manifests. Keep the generated CRD and chart CRD byte-identical. Edit generated deepcopy code through its generator. The public repository is github.com/Gravqc/Nats-Cluster-Operator. API group and module names remain provisional; any future rename must update them together and document the API migration.

Use gofmt for changed Go files, then focused tests and go vet. Before a handoff involving reconciliation, storage or upgrade logic, run make test and the relevant Kind scenario. For changed Python use the local virtual environment to run ruff check --fix and ruff format. Run helm lint charts/nats-cluster-operator for chart changes, bash -n for shell changes, and git diff --check before committing. If tools are unavailable, report which check remains unverified.

## Local environment and data

Use just up/status/logs/nodes/stop/down from the repo root. All Kubernetes actions must use the repository kubeconfig and explicit kind-nats-operator-dev context. Other local clusters and Docker projects may belong to the user. just stop preserves data; just down deletes the disposable lab and its storage. E2e tests require a fresh lab and write results to .state/evaluation.json. Upgrade changes also run `python3 tests/upgrade_comparison.py` in a lab with no previous comparison fixtures. Report sampled monitoring gaps separately from actual message integrity. Monitoring reads must have short timeouts. Recovery checks must prove the old process died and a new process became Ready; do not require a different leader name. Run `python3 -m unittest discover -s tests -p 'test_*.py'` when changing measurement logic. Memory-policy changes also run `python3 tests/memory.py` against the lab. Keep the last requested running or stopped state unless the user authorizes a lifecycle change needed for a test.

Keep credentials, kubeconfigs, binaries, logs and compiler caches out of Git. Use referenced Secrets and synthetic test data. Avoid logging credentials. Changes to namespace permissions, data deletion, adoption or maintenance actions need explicit behavior and focused regression checks. Persist progress before disruptive work and derive recovery from observed state. Preserve the health gate and retained-volume behavior.

## Documentation and publication

Document the implemented behavior, why it exists, commands to reproduce it, and limitations. Distinguish executed local tests from planned tests and hosted CI. A passing Kind test establishes the tested scenario on one Docker host. Update examples, scope, usage and test records when their behavior changes. Do not invent maintainer identities, security inboxes, release artifacts or upstream endorsement. Follow docs/releasing.md before publication. Commit and push only when explicitly authorized.
