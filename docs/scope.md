# Project purpose and v0.1 scope

## Why build this?

A team running multiple NATS clusters repeatedly configures server versions, persistent JetStream storage, Services, monitoring, scheduling and maintenance. This project offers a Kubernetes NatsCluster resource that supplies one explicit request for each independent cluster and reports its observed condition.

The official NATS Helm chart already provisions servers. The additional behavior evaluated here is a continuously running controller that inspects NATS replica health and coordinates a server rollout. A small CR alone can also simplify a platform interface, but adoption should be justified against the existing Helm workflow using measured setup and maintenance effort.

This is an independent personal project. There is no upstream endorsement or production support commitment.

## Included in v0.1

- A Kubebuilder-generated NatsCluster CRD with validation and a Go controller.
- Pinned upstream server image versions and file-backed JetStream.
- One or three servers, with per-cluster StatefulSet, Services, ConfigMap, PDB, NetworkPolicy and persistent claims.
- Optional per-cluster PodMonitor when the Prometheus Operator CRD is installed.
- Namespace-scoped operation, Secret references, resource requests/limits and node placement.
- Monitoring-derived status and guarded forward patch and adjacent-minor upgrades.
- Retention of running workloads and disks when the CR is deleted.
- A Kind quickstart, unit/race tests and an e2e upgrade scenario with acknowledged-message verification.

The clean evaluation covers one-server and three-server clusters and the 2.15.2 to 2.15.3 patch upgrade, plus the 2.11.14 to 2.12.5 minor-upgrade and hard-process-failure trials. Five-server creation is rejected until a topology-specific evaluation has been executed. The CRD supports explicit 2.x image versions; the available compatibility evidence is limited to the versions recorded in evaluation.md.

## Ownership

The controller manages server infrastructure inside its namespace. Kubernetes schedules the pods and provisions volumes through an existing storage class. NATS performs replication and leader election. Application code provisions streams and consumers. Operators using this project supply worker capacity, credentials and monitoring infrastructure.

## Deferred work

Autoscaling, connection rebalancing, safe retirement, volume expansion, credential rotation, TLS, backups, major version upgrades and in-place adoption from Helm require additional design and tests. Replica count, storage and scheduling fields are immutable in the current API. See operations.md for the evaluation of each proposed workflow and migration.md for a separate-cluster cutover.

## Acceptance evidence

The initial acceptance conditions are cluster isolation, an observable Ready condition, a blocked upgrade during verified server unresponsiveness, at most one unavailable server during the tested rollout, preserved acknowledged payloads, recovery after controller and pod restarts, and retained data on CR deletion. The executed record and its limits are in evaluation.md.
