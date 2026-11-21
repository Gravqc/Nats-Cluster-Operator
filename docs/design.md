# Design

## Request and reconciliation

A `NatsCluster` CR describes the desired cluster. Kubebuilder generates its API serialization and CRD schema. The controller watches that CR, owns its child objects, and periodically queries NATS. The StatefulSet uses `OnDelete`, so Kubernetes replaces failed pods while the operator decides the order of intentional version changes.

The initial implementation uses one StatefulSet per cluster. Replacing StatefulSets with a custom pod controller would introduce identity, retention and scheduling work. Per-server retirement is a candidate for a later API once its behavior is tested.

Routes use cluster-specific headless DNS records. Services and network policy selectors include the instance name. Each pod has its own ReadWriteOnce claim. Namespaces are the operational/RBAC boundary. Credentials are referenced from a Secret; the operator never writes their value into a ConfigMap or status. NATS routing within a cluster uses a restricted route port. NetworkPolicy requires an enforcing CNI, which Kind's default networking does not provide. This prototype has no client or route TLS and is intended for evaluation on trusted networks.

## Rolling upgrade

Only the server version can change in v0.1; admission allows forward patch changes within the same minor version. Other spec fields are immutable. The controller updates the StatefulSet pod template, observes existing pods and NATS health, waits for stability, then deletes one outdated pod. The replacement mounts the retained PVC. The preStop hook enters NATS lame-duck mode and Kubernetes grants 120 seconds for termination. Readiness/startup use the full JetStream `/healthz` check.

The additional gate inspects metadata, stream and consumer replica state reported by `/jsz`. It requires an authoritative report from the leader of each observed Raft group because follower reports can lack current information about other followers. Conflicting leader observations block the next action. Missing leaders, lagging/offline/non-current replicas and unavailable endpoints block progress. Remaining outdated followers are ordered before the metadata leader when it is among them. A new leader can be elected during a rollout. Stream and consumer leaders are distributed independently; this implementation gates their replica health rather than claiming every leader can be restarted last.

Status is persisted before deletion, and the delete includes a pod UID precondition. After a controller restart, the next action derives from current pod versions and NATS health. Health observation and deletion are separate operations, so a failure can still occur between them. The controller stops issuing further restarts on observed degradation. Single-server upgrades have an availability interruption. A PDB constrains eviction requests; it does not make direct controller deletions safe on its own.

Health status represents a polling observation, not continuous availability. Large clusters can exceed the monitoring response limit, and busy replicas can hold an upgrade indefinitely. This first implementation favors a blocked rollout over proceeding through an uncertain gate. There is no timeout that overrides replica health.

## Deletion and ownership

Every child has a controller owner reference. A finalizer removes those references before allowing CR deletion, retaining the running cluster and its PVCs. There is no automatic deletion mode. Same-name resources controlled by another owner, including retained clusters, cause a reconciliation error. During namespace deletion Kubernetes removes namespaced resources independently; backups remain necessary.

Deleting the controller prevents new reconciliation while StatefulSet recovery continues. Keep the controller running until requested CR finalization has completed. The operator image uses leader election and namespace-scoped RBAC. It can read namespace Secrets and create pod workloads, so permission to write NatsCluster resources implies meaningful execution privileges in that namespace.

## Scope boundaries

A storage class and schedulable workers must already exist. Monitoring requires an existing Prometheus Operator when PodMonitor is enabled. The metrics exporter is always present and pinned. Client stream and consumer policy belongs to application code. A future NACK integration could be optional, with explicit ownership of each stream policy.

Research checked on 2026-11-20. [Kubebuilder](https://book.kubebuilder.io/) explains the scaffolding. [Upstream Kubernetes guidance](https://docs.nats.io/learn/deployment/kubernetes) remains the baseline comparison. The [archived official operator](https://github.com/nats-io/nats-operator) demonstrates the historical CR approach and recommends Helm for new deployments.
