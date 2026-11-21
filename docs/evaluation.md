# Evaluation record

The scripted evaluation runs only against `kind-nats-operator-dev` using a repository-local kubeconfig. Runtime results are written to `.state/evaluation.json`. The clean local run completed on 2026-11-20 using Kubebuilder v4.6.0, Go 1.24.9, Kubernetes v1.35.8 in Kind v0.33.0, and NATS 2.15.2 to 2.15.3.

All nine e2e checks passed. The final reads verified 900 unique messages on the upgraded cluster and all 100 seed messages on the independent control cluster. The upgrade load replayed the first 100 IDs, so that run verified final contents rather than preservation of the original seed messages without replay. The concurrent publisher received 900 acknowledgements with zero retry errors in this run. At most one server was unavailable during the rollout. Runtime pause of a NATS task was verified before testing the blocked upgrade. The operator was restarted during reconciliation, then completed the rollout. Pod recovery and retained-workload deletion checks preserved the same payloads.

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

Kind shares one Docker host. These tests cannot establish physical host/AZ resilience, EBS behavior, production throughput, internet security, or loss protection beyond the tested replicas. This suite does not certify every NetworkPolicy path; monitoring measurements use pod-local reads rather than assuming API-server proxy access. TLS, backups, credential rotation, persistent consumer failover, large monitoring payloads, other minor-version pairs, autoscaling, client rebalancing and in-place Helm adoption require additional work. The test suite checks a finite set of acknowledged messages; it is not a proof of losslessness for every failure timing.

PodMonitor is opt-in and requires an external CRD. The local test installs the pinned upstream PodMonitor CRD v0.80.1 and verifies the emitted object and its cluster selector. Real Prometheus scraping/selection must be verified with a real Prometheus Operator installation.

## Memory policy — 2026-11-21

`python3 tests/memory.py` passed against the resumed Kind lab. A real NATS 2.15.3 process reported `gomemlimit=429496720` for a 512Mi container with an 80% policy, and received `GOGC=75`. Kubernetes rejected the policy without a memory limit. Resource-generation regression tests verify opt-in behavior. This validates configuration delivery, not throughput, memory-pressure performance or OOM prevention.

## Reproducing the minor-upgrade comparison

From a fresh lab after `just up`, run:

```sh
python3 tests/memory.py
python3 tests/upgrade_comparison.py
```

The comparison downloads the official NATS chart **2.12.5**, verifies its SHA-256
before use, and creates separate `eval-helm` and `eval-operator`
fixtures. Both start with three NATS **2.11.14** servers on separate workers,
1Gi claims, the same NATS CPU/memory requests and limits, and an application-created
file-backed R3 stream. Both upgrade to **2.12.5** under acknowledged publishing
at approximately ten messages per second. Each trial then kills the confirmed
metadata-leader process through the Kind container runtime while publishing
continues. Kubernetes/NATS recovery is measured separately from rollout time. The test verifies that the original container exited with code 137 and a new container became Ready. A fast restart can recover with the same metadata leader; a change in leader name is recorded, not required.

The current comparison seeds 100 payloads before the upgrade, then sends 1,500
new IDs during the trial. Retries reuse the ID of the message being sent. After
both events, the verifier checks all 1,600 payloads by sequence. The patch suite
uses the same separation with 900 new IDs and expects 1,000 total payloads. Retry errors cover the **combined rollout and
hard-failure trial**, not the rollout alone. They include retryable request
errors, not only terminal message failures.

Monitoring samples read `/jsz` through bounded, pod-local `kubectl exec` calls.
The API-server proxy path timed out for the operator pods during an exploratory
attempt, so it is not used as evidence of leader absence. The script checks its
monitoring path before load and requires a fresh leader observation before
injecting the next failure. It records partial results and an error if a run
fails; a complete run sets `completed=true` in `.state/upgrade-comparison.json`.

The reported no-confirmed-leader duration is estimated from polling intervals.
Zero means no absence was sampled. Leader-change counts compare consecutive
successful samples and can miss changes during gaps. These are not continuous
Raft traces. The official chart keeps its readiness/lifecycle behavior; the
operator has its own stricter replica gate and exporter sidecar. Trials run
sequentially on one Docker host. This is a reproducible functional comparison,
not a statistical benchmark or proof that either deployment method loses data.

Admission checks also verify that overlapping requests and skipped minors
relative to the last healthy version are rejected. Five-server creation is
rejected until that topology is evaluated. No connection redistribution, leader
balancing, five-server scale cycle or metrics-driven scaling experiment is
claimed by this record.

## Minor-upgrade results — 2026-11-21

At commit `9cdb28b`, both local trials verified 1,500 unique payloads at the end of the rollout and a verified metadata-leader process kill. That probe replayed the 100 seed IDs during load. The recorded counts establish final contents, and do not independently prove that the original seed messages survived without replay.

| Measurement | Official Helm chart | Operator |
| --- | ---: | ---: |
| Verified unique messages | 1500 | 1500 |
| Missing messages | 0 | 0 |
| Publisher retry errors across both events | 6 | 11 |
| Maximum unavailable during rollout | 1 | 1 |
| Observed leader changes during rollout | 1 | 1 |
| Rollout seconds | 46.99 | 51.98 |
| Sampled seconds without a confirmed leader during rollout | 0.00 | 0.00 |
| Confirmed leader after hard kill, seconds | 5.03 | 4.88 |
| Replacement process Ready, seconds | 15.01 | 4.88 |

The comparison establishes these finite recovery and message-integrity cases. It does not demonstrate a general availability or performance advantage over Helm. Retry counts include the injected failure, and one trial per approach cannot establish a statistical difference. A verified process restart may retain the same leader name.

Go race tests, vet, generated CRD parity, Kubernetes admission, Helm lint, focused Python lint/format checks and three recovery-measurement regression tests passed. The original nine-case record above remains dated 2026-11-20; these are additional targeted runs. The [hosted branch run](https://github.com/Gravqc/Nats-Cluster-Operator/actions/runs/44881286843) passed the full suite at the same commit. The [main run](https://github.com/Gravqc/Nats-Cluster-Operator/actions/runs/44882506084) failed in quickstart because its readiness check ran before the broker pod existed.

## Updating an existing lab — 2026-11-21

`make local-up` was run against the existing lab after the changes. It applied
the current CRD, replaced the controller pod despite the stable local image tag,
and waited for the local NATS pod and resource to become Ready. The check
confirmed the controller UID changed and the installed schema contained the
memory policy, applied-version status and one/three-server validation.

Helm does not upgrade CRDs from `crds/` during an upgrade. Kind loading a rebuilt
image also does not replace an existing pod. The local script now handles both
steps explicitly. This check covers resume/update of an existing installation;
the hosted runs above cover fresh installation.
