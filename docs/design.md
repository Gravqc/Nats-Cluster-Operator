# Design

## Request and reconciliation

A `NatsCluster` CR describes the desired cluster. Kubebuilder generates its API serialization and CRD schema. The controller watches that CR, owns its child objects, and periodically queries NATS. The StatefulSet uses `OnDelete`, so Kubernetes replaces failed pods while the operator decides the order of intentional version changes.

The initial implementation uses one StatefulSet per cluster. Replacing StatefulSets with a custom pod controller would introduce identity, retention and scheduling work. Per-server retirement is a candidate for a later API once its behavior is tested.

Routes use cluster-specific headless DNS records. Services and network policy selectors include the instance name. Each pod has its own ReadWriteOnce claim. Namespaces are the operational/RBAC boundary. Credentials are referenced from a Secret; the operator never writes their value into a ConfigMap or status. NATS routing within a cluster uses a restricted route port. NetworkPolicy requires an enforcing CNI, whose behavior must be checked in the target environment. This prototype has no client or route TLS and is intended for evaluation on trusted networks.

## Rolling upgrade

Only the server version can change in v0.1; admission allows forward patches and one adjacent minor at a time. Other spec fields are immutable. Version ordering is not a compatibility certification for every upstream release. Read the upstream release notes and the tested-version record before a real upgrade. The controller updates the StatefulSet pod template, observes existing pods and NATS health, waits for stability, then deletes one outdated pod. The replacement mounts the retained PVC. The preStop hook enters NATS lame-duck mode. The configuration sets a 10-second lame-duck grace period and a 30-second duration, the same as the official chart. Kubernetes grants 120 seconds for termination. NATS ignores SIGTERM during lame-duck mode, so the whole shutdown must fit within that period. Servers read the configuration only at startup and no reloader runs. After an operator upgrade, each server's first deliberate restart is therefore expected to use the timing it started with. That transition was not tested. See the [lame-duck evidence](lame-duck-experiment.md). Readiness/startup use the full JetStream `/healthz` check.

The additional gate inspects metadata, stream and consumer replica state reported by `/jsz`. It requires an authoritative report from the leader of each observed Raft group because follower reports can lack current information about other followers. Conflicting leader observations block the next action. Missing leaders, lagging/offline/non-current replicas and unavailable endpoints block progress. Remaining outdated followers are ordered before the metadata leader when it is among them. A new leader can be elected during a rollout. Stream and consumer leaders are distributed independently; this implementation gates their replica health rather than claiming every leader can be restarted last.

The controller persists `status.upgradeVersion` before updating the StatefulSet template. Admission compares a new target with `status.appliedVersion`, the last version observed healthy on every server, so rapid requests cannot skip a minor before target persistence. Admission rejects another version request until the target is healthy on every pod; the controller also checks the requested version against the current template to reject skipped minors when requests arrive before reconciliation. The target survives controller restarts.

Status is persisted before deletion, and the delete includes a pod UID precondition. After a controller restart, the next action derives from current pod versions and NATS health. Health observation and deletion are separate operations, so a failure can still occur between them. The controller stops issuing further restarts on observed degradation. Single-server upgrades have an availability interruption. The replica health gate applies to deletions initiated by this controller. Once the StatefulSet template is updated, Kubernetes can use the requested version when independently replacing a pod, even while the controller is waiting on replica health. The process-pause test exercises the controller’s deletion gate. A PDB constrains eviction requests; it does not make direct controller deletions safe on its own.

Health status represents a polling observation, not continuous availability. Large clusters can exceed the monitoring response limit, and busy replicas can hold an upgrade indefinitely. This first implementation favors a blocked rollout over proceeding through an uncertain gate. There is no timeout that overrides replica health.

## Deletion and ownership

Every child has a controller owner reference. A finalizer removes those references before allowing CR deletion, retaining the running cluster and its PVCs. There is no automatic deletion mode. Same-name resources controlled by another owner, including retained clusters, cause a reconciliation error. During namespace deletion Kubernetes removes namespaced resources independently; backups remain necessary.

Deleting the controller prevents new reconciliation while StatefulSet recovery continues. Keep the controller running until requested CR finalization has completed. The operator image uses leader election and namespace-scoped RBAC. It can read namespace Secrets and create pod workloads, so permission to write NatsCluster resources implies meaningful execution privileges in that namespace.

## Scope boundaries

A storage class and schedulable workers must already exist. Monitoring requires an existing Prometheus Operator when PodMonitor is enabled. The metrics exporter is always present and pinned. Client stream and consumer policy belongs to application code. A future NACK integration could be optional, with explicit ownership of each stream policy.

Research checked on 2026-11-20. [Kubebuilder](https://book.kubebuilder.io/) explains the scaffolding. [Upstream Kubernetes guidance](https://docs.nats.io/learn/deployment/kubernetes) remains the baseline comparison. The [archived official operator](https://github.com/nats-io/nats-operator) demonstrates the historical CR approach and recommends Helm for new deployments.
