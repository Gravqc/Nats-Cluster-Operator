# Which runbooks belong in the operator?

The v0.1 implementation provisions clusters, reports health and performs guarded forward patch upgrades. The operations below are evaluated design candidates. They are not implemented API fields.

## Client connection redistribution

There is an explicit upstream use case after a pod restart or rolling upgrade. Existing clients can stay connected to surviving servers while the recovered server has few connections. The maintainer points to `nats server cluster balance` and its implementation in `jsm.go`, and explains that orchestration belongs outside the server.

A useful next feature is opt-in, bounded rebalancing after sustained imbalance. Observe connection counts, traffic and resource pressure; require a recovered cluster to be healthy for a configurable interval. Limit disconnects per batch, per run and per time period. Re-measure after each batch and abort on reconnect errors or worsening load. Clients choose their reconnect destination, so a balanced count is not guaranteed. Count alone is a weak proxy for CPU or memory load. Do not rebalance during OOM recovery until the returned server is stable. Redistributing connections can move client traffic while JetStream stream placement continues to determine storage and replication load.

Prefer the supported NATS management APIs or `jsm.go` in the controller over spawning a shell CLI. System-account credentials need a distinct Secret and narrowly scoped permissions. Persist operation IDs and results so a controller restart cannot repeat an unbounded batch. A periodic Job remains a useful baseline to compare operational complexity.

Evaluate three nodes with many long-lived clients. Kill one NATS process, wait for recovery, and compare connection skew before and after bounded rebalancing. Record reconnect time, publish acknowledgement errors, duplicates, message integrity, CPU, memory and a no-action control run. Include the reported two-node ping-pong case before supporting small clusters.

Sources checked on 2026-11-20. [Upstream discussion #8739](https://github.com/nats-io/nats-server/discussions/8739), [connection imbalance #1556](https://github.com/nats-io/nats-server/issues/1556), [two-node balance issue](https://github.com/nats-io/jsm.go/issues/748).

## Autoscaling and safe retirement

Server count and stream replica count are separate settings. Adding a server provides potential capacity; useful stream/connection redistribution still needs evaluation. Reducing server count requires evacuation, capacity validation, replica catch-up and membership removal before deleting pods or disks. The current API rejects replica changes.

Start future work with an explicit, reviewed scale operation. Measure sustained publish latency, disk pressure, replica placement, CPU and traffic. Connection count alone should not drive scaling. Add bounds, headroom, cooldowns and storage-class/zone checks. Test manual scale-up and scale-down with acknowledged data before enabling any metrics-driven policy. A Kubernetes HPA directly changing StatefulSet replicas would bypass the NATS lifecycle controller.

The initial API supports one, three and five servers as a deliberate evaluation scope. NATS deployments can use other server counts. Quorum safety depends on metadata and each stream/consumer replication group, not a global rule that server counts must always be odd.

Sources checked on 2026-11-20. [Clustering, placement and peer management](https://docs.nats.io/learn/clustering/).

## Other operations

Disk expansion depends on CSI support. In-place claim expansion and waiting for filesystem completion should be evaluated before any replica replacement approach. Disk loss requires explicitly identifying the lost replica and reconstructing it while preserving quorum. Snapshots/restores need per-stream consistency and restore tests. Credential rotation and TLS require their own staged procedures. A CLI command is a building block; retries, authorization, cancellation, status, concurrency and partial failure turn it into an operator operation.

## Open-source adoption

Share a small reproducible evaluation and ask NATS maintainers which workflows they would welcome automated. Explain the existing Helm baseline and the exact extra behavior, using measured results. An early release should have a one-command local quickstart, stable status conditions, a documented uninstall/retention policy, published image/chart versions and a migration guide. Measure time to first cluster, configuration size, failed-install explanations, upgrade interruption and operational steps versus Helm. Community interest and production experience determine which operation to build next.
