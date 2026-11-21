# Evaluation record

The scripted evaluation runs only against `kind-nats-operator-dev` using a repository-local kubeconfig. Runtime results are written to `.state/evaluation.json`. The clean local run completed on 2026-11-20 using Kubebuilder v4.6.0, Go 1.24.9, Kubernetes v1.35.8 in Kind v0.33.0, and NATS 2.15.2 to 2.15.3.

All nine e2e checks passed. The upgraded cluster retained all 900 unique test messages, and the independent control cluster retained all 100. The concurrent publisher received 900 acknowledgements with zero retry errors in this run. At most one server was unavailable during the rollout. Runtime pause of a NATS task was verified before testing the blocked upgrade. The operator was restarted during reconciliation, then completed the rollout. Pod recovery and retained-workload deletion checks preserved the same payloads.

Go unit/race tests, go vet, generated schema equality, Helm lint, API-server validation of install manifests, and focused Python lint/format checks also passed. GitHub Actions workflows are defined; the initial commit has been pushed to the public repository; hosted CI results are tracked separately from local evidence.

The original signal-based fault injection was replaced because PID 1 ignored the attempted in-container pause. The final clean run used the container runtime's verified task pause. This matters when reproducing the health-gate result.

## Stop and resume check

On 2026-11-20 the existing lab was resumed with just up, its local NATS pod reached 2/2 Ready, and its live monitoring endpoint returned {"status":"ok"}. The lab was then stopped again. Startup now waits for the Kubernetes API, nodes, controller Deployment and actual local pod readiness before reporting success. This closes a race where a retained Ready condition could have been returned before the resumed pod was ready.

## Required checks

- Generation and CRD validity against the Kubernetes API server.
- Resource isolation, replica health gate behavior, retention after interrupted finalization, and rejection of conflicting ownership.
- Operator installation from its own Helm chart.
- Single-server local cluster and two independent three-server clusters.
- Readiness and versions read from NATS monitoring endpoints.
- Application-driven R3 stream creation and acknowledged publications.
- Upgrade blocked while one server is unresponsive.
- Forward patch upgrade from 2.15.2 to 2.15.3 with traffic and controller restart.
- Exact payload and count verification for every acknowledged publication.
- Pod recovery using the retained PVC.
- Cluster CR deletion retains the running workload and messages.
- Unsupported scale-down rejected at admission.

## Limits

Kind shares one Docker host. These tests cannot establish physical host/AZ resilience, EBS behavior, production throughput, internet security, or loss protection beyond the tested replicas. The default Kind CNI does not enforce NetworkPolicy. TLS, backups, credential rotation, persistent consumer failover, large monitoring payloads, minor upgrades, autoscaling, client rebalancing and in-place Helm adoption require additional work. The test suite checks a finite set of acknowledged messages; it is not a proof of losslessness for every failure timing.

PodMonitor is opt-in and requires an external CRD. The local test installs the pinned upstream PodMonitor CRD v0.80.1 and verifies the emitted object and its cluster selector. Real Prometheus scraping/selection must be verified with a real Prometheus Operator installation.

## Memory policy — 2026-11-21

`python3 tests/memory.py` passed against the resumed Kind lab. A real NATS 2.15.3 process reported `gomemlimit=429496720` for a 512Mi container with an 80% policy, and received `GOGC=75`. Kubernetes rejected the policy without a memory limit. Resource-generation regression tests verify opt-in behavior. This validates configuration delivery, not throughput, memory-pressure performance or OOM prevention.
