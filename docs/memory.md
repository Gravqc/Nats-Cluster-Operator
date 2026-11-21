# Memory policy

Each NATS process has its own Go garbage collector. Optional creation-time fields
apply the same policy to every broker:

```yaml
spec:
  goMemoryLimitPercent: 80
  goGCPercent: 100
  resources:
    requests: {cpu: 100m, memory: 128Mi}
    limits: {cpu: "1", memory: 512Mi}
```

`goMemoryLimitPercent` (50–95) sets `GOMEMLIMIT` in bytes using the **NATS
container** limit. It requires a positive memory limit. The exporter has a
separate limit. `goGCPercent` (10–500) sets `GOGC`. Both are optional and immutable
for this API version. Existing clusters keep upstream defaults.

`GOGC=100` targets heap growth relative to the live heap. It does not wait for
physical memory utilization to reach 100%. `GOMEMLIMIT` is a soft Go-runtime
limit, not an OOM guarantee or a Linux page-cache limit. Leave headroom and
measure CPU, latency and container memory under representative load. An
undersized limit can cause repeated collections and degraded throughput.

JetStream's file-store buffers and Linux's filesystem cache are different.
NATS expires inactive file-store buffers itself. Linux reclaims page cache under
memory pressure. This controller does not use privileged `drop_caches`, delete
message files, force collections through profiling endpoints, or coordinate
cluster-wide memory purges. Such actions can harm disk performance and do not
repair an undersized live heap. Stream retention and storage capacity remain
application/server configuration responsibilities.

References checked 2026-11-21:
[Go GC guide](https://go.dev/doc/gc-guide),
[Linux drop_caches documentation](https://docs.kernel.org/admin-guide/sysctl/vm.html#drop-caches),
[NATS file-store expiry](https://github.com/nats-io/nats-server/blob/v2.15.3/server/filestore.go),
[NATS memory-limit detection](https://github.com/nats-io/nats-server/blob/v2.15.3/server/jetstream.go).
