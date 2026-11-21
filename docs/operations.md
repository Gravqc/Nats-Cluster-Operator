# Maintenance scope and runbook decisions

## Acceptance rule

Ship an automatic operation only when its purpose is clear and its behavior has
an executed, reproducible test. An existing CLI command is a useful building
block. A controller also needs authorization, persisted progress, retry bounds,
partial-failure recovery and observable results. Proposals below are not API
fields and are not implemented.

## Implemented

**Guarded rolling upgrades.** The controller pins a target, waits for healthy
metadata, stream and consumer groups, restarts outdated followers before the
current metadata leader, and waits for catch-up before proceeding. Kubernetes
still replaces failed processes. NATS still performs elections and replication.
See [design](design.md), [usage](usage.md) and [executed evidence](evaluation.md).

**Optional Go memory policy.** Per-broker `GOMEMLIMIT` and `GOGC` are startup
settings, validated in Kind. They do not flush filesystem caches or guarantee
that a live workload will fit. See [memory policy](memory.md).

## Connection redistribution — candidate, not automatic

The upstream `nats server cluster balance` command uses system-account KICK
requests to disconnect selected clients. Reconnecting clients choose their new
server, so equal connection counts are not guaranteed. Counts also say little
about traffic volume or a single client's CPU cost. Broadly draining every
server can cause reconnect storms.

A useful acceptance scenario would start many long-lived clients, cause a
verified restart, measure sustained skew, then compare a no-action control with
a bounded opt-in batch. Require stable replica health, a cooldown, exclusion of
administrative clients, a finite disconnect budget persisted before each action,
and a stop condition on reconnect/publish errors. Test an operator restart
between saving and executing the batch. The existing single-publisher upgrade
test does **not** validate a connection-balancing feature.

The prototype's client token runs in the implicit application account. Cluster
management requires separate system-account credentials. NATS does not allow a
global token alongside users defined in accounts. Introducing that authentication
model requires an explicit client/data migration design. Do not silently grant
application clients system privileges to make rebalancing work.

References checked 2026-11-21:
[maintainer discussion #8739](https://github.com/nats-io/nats-server/discussions/8739),
[KICK/LDM support #4298](https://github.com/nats-io/nats-server/pull/4298),
[upstream connection balancer](https://github.com/nats-io/jsm.go/tree/main/connbalancer),
[two-node behavior #748](https://github.com/nats-io/jsm.go/issues/748),
[account/token validation](https://github.com/nats-io/nats-server/blob/v2.15.3/server/opts.go).

## Stream and consumer leader balancing — opt-in candidate

The CLI commands are `nats stream balance` and `nats consumer balance`. The
upstream balancer requires server 2.11 or later and requests preferred leaders
among an asset's existing replicas. Moving a leader differs from moving its
stored replicas. A recovered cluster does not necessarily need either action.

Do not automatically step down leaders after every rollout. First establish
sustained skew or a workload bottleneck, then evaluate a bounded explicit action.
Test placement constraints, R1/R3 streams, persistent consumers, request timeouts,
controller restarts and message/consumer state. Require opt-in and measure
benefit against a no-action control. This prototype has not run that experiment
and exposes no leader-balancing switch.

[Upstream leader-balancer implementation](https://github.com/nats-io/jsm.go/tree/main/balancer)
is the reference, checked 2026-11-21.

## Explicit scaling and retirement — deferred

The API accepts creation with one or three servers and rejects changes to
that count. Server count and each stream's replication count are separate. These
counts are an initial project scope, not a universal NATS requirement.

A retirement operation needs this sequence:

1. Validate remaining capacity and the placement/replication needs of every asset.
2. Persist the exact server and stage before taking action.
3. Request evacuation while the server remains available.
4. Verify assets moved and retained their requested replica count, with healthy,
   caught-up replacement replicas.
5. Remove metadata membership, confirm removal, then reduce the StatefulSet.
6. Verify no stale member remains and retain the retired PVC for explicit review.

`nats server cluster evacuate` is best-effort. A successful API response does not
prove replication was preserved when replacement placement is impossible. A
controller must not turn that response directly into pod deletion. The newer
API also needs explicit server-version compatibility tests.

A future test must cover 3 → 5 → 3 with R3 acknowledged traffic, real five-worker
placement, insufficient capacity, placement constraints, retained PVC reuse,
partial failures and controller restarts at each stage. The current Kind lab has
three workers, so it cannot validate five-server required anti-affinity. No
scale-down feature or partial workflow is included.

[Evacuation CLI](https://github.com/nats-io/natscli/blob/main/cli/server_cluster_command.go),
[server evacuation/removal handlers](https://github.com/nats-io/nats-server/blob/v2.15.3/server/jetstream_api.go),
[placement and clustering](https://docs.nats.io/learn/clustering/), checked 2026-11-21.

## Excluded actions

**Metrics-driven autoscaling:** outside the project's current scope. An HPA
changing StatefulSet replicas would bypass membership and placement sequencing.
No scaling policy has been justified or tested here. Supporting an explicit
retirement operation would not by itself justify automatic scaling.

**Host page-cache purging:** not an operator maintenance action. Linux reclaims
cache; privileged `drop_caches` can harm other workloads and disk performance.
Go heap collection, NATS buffers and Linux cache are distinct. The controller
never deletes JetStream data in response to a memory threshold.

**Blindly executing CLI runbooks:** no shell-command escape hatch. Supported
operations should use documented administration APIs with finite permissions
and tested retry behavior. The controller does not override Raft elections or
replace NATS's replication algorithm.

## Other gaps and adoption

Volume expansion, TLS, credential rotation, backup/restore and lost-disk recovery
need their own designs and tests. Helm adoption remains a separate-cluster
cutover in [migration](migration.md). A storage class, worker capacity and any
Prometheus installation remain prerequisites.

For community feedback, publish the small reproducible comparison and its
limitations. Report both positive and negative findings. A CR can simplify a
platform interface, but that alone does not prove safer production maintenance.
Do not claim upstream endorsement, production readiness, or loss protection
beyond the scenarios actually tested.
