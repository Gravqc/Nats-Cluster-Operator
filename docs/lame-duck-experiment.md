# Lame-duck shutdown timing

Status: designed 2026-11-24, before any trial was run. The design, expected
outcomes and decision rule were fixed before collecting results.

## Question

Does a NATS server restarted by the operator finish its lame-duck shutdown
within the pod's termination grace period when many clients are connected?

The operator's preStop hook signals lame-duck mode. Its NATS configuration sets
neither `lame_duck_duration` nor `lame_duck_grace_period`, so the server
defaults apply. In nats-server v2.12.5 these are 2 minutes and 10 seconds
(`server/const.go`). The pod's `terminationGracePeriodSeconds` is 120.

Upstream source behavior relevant to the timing (`server/signal.go`,
`server/server.go`, v2.12.5):

- SIGTERM is ignored while lame-duck mode is in progress. Kubernetes' SIGTERM
  after the preStop hook does not shorten it; only SIGKILL at the end of the
  grace period does.
- After the grace period the server closes clients spread over the remaining
  duration. With more than about 110 clients, the sleep between closes is
  random between half and all of `(duration − grace) / clients`. The expected
  eviction span is therefore about three quarters of 110 seconds under the
  defaults, before the final shutdown.
- `lame_duck_duration` must be at least 30 seconds in a configuration file, and
  the grace period must be strictly shorter.

The official chart uses 10 seconds of grace, a 30-second duration and a
60-second termination grace period. It documents the rule
`terminationGracePeriodSeconds ≥ grace + duration + 20s` for shutdown overhead.
The operator's defaults give 10 + 120 + 20 = 150 seconds against 120.

## Setup

One operator-managed three-server cluster from `examples/ha.yaml`, with an
application-created R3 file stream holding 2,000 padded 8 KiB messages so the
final JetStream shutdown has data to close.

For each of three trials:

1. Wait for every pod and the resource to be Ready.
2. Open 300 core NATS connections from one client pod to ordinal 2's own
   address, with reconnection disabled. Each connection records when it was
   closed.
3. Confirm ordinal 2's `/varz` reports at least 300 connections.
4. Delete ordinal 2 and watch it until the replacement appears.
5. From the last watched state of the old pod, take the NATS container's
   exit code and finish time. The delete request time is `deletionTimestamp`
   minus `deletionGracePeriodSeconds`.

Measured per trial: seconds from the delete request to container exit, exit
code, and the first and last client close relative to the delete request.

## Decision rule

The current timing **needs a fix** if any baseline trial exits non-zero
(including 137 from SIGKILL) or takes longer than 100 seconds. 100 seconds
leaves less than the 20-second shutdown overhead the chart documents.

A proposed fix is **accepted** only if every trial with it exits 0, closes the
first client no earlier than 9 seconds after the delete (the grace period is
honored, with 1 second of polling slack), closes all 300 clients, and exits
within 60 seconds. The unit tests and the Kind regression suite must also pass.

If the baseline does not need a fix, the result is recorded as negative and the
operator code stays unchanged. Shorter rollouts alone would be a separate
trade-off for the maintainer, because a shorter duration also concentrates
client reconnections.

## Results — 2026-11-24: negative

Kubernetes v1.35.8 in Kind v0.33.0 on one Docker host, NATS 2.15.3 from
`examples/ha.yaml`, operator at commit `c9c4a9e`. The generated configuration
set no lame-duck options. The source was read at v2.12.5. The measured server
ran 2.15.3, so the measurement, not the source reading, decides the result.

| Trial | Exit code | Exit after delete request, s | First client closed, s | Last client closed, s |
| --- | ---: | ---: | ---: | ---: |
| 1 | 0 (Completed) | 81 | 11.2 | 81.2 |
| 2 | 0 (Completed) | 80 | 10.4 | 80.7 |
| 3 | 0 (Completed) | 80 | 10.7 | 80.2 |

All 300 clients were closed in every trial. Exit times come from container
finish timestamps with 1-second resolution, so the last-closed time can
appear slightly later than the exit.

The current timing **does not need a fix** under the decision rule. Every
trial exited cleanly about 40 seconds before the 120-second grace period
ended. The grace period was honored and clients were spread over about 70
seconds. A fix to 10 + 30 seconds was drafted and then reverted unmeasured,
as the rule requires.

What remains true, and was not measured:

- The configuration does not meet the upstream chart's sizing rule
  (10 + 120 + 20 = 150 seconds against 120). The 40-second margin comes from
  randomized eviction finishing before the full duration, not from the
  configuration.
- The final JetStream shutdown here closed a store of about 16 MiB. Much larger
  stores, or slow disks, could take longer to close. That case was not tested.
- With many clients, each deliberate restart spends about 80 seconds in lame
  duck, so a three-server rollout spends about four minutes. A 30-second
  duration would shorten that and concentrate reconnections. That is a
  trade-off for the maintainer, not a defect.

Reproduce from a fresh lab after `just up`. Switch the controller to the
baseline build first. The probe jobs still use the current quickstart image.

```sh
git archive ee10389 Dockerfile go.mod go.sum api cmd internal |
  docker build -t nats-cluster-operator:transition-baseline -
kind load docker-image nats-cluster-operator:transition-baseline --name nats-operator-dev
export KUBECONFIG="$PWD/.state/kubeconfig"
kubectl --context kind-nats-operator-dev -n nats-demo set image deployment/nats-operator \
  operator=nats-cluster-operator:transition-baseline
kubectl --context kind-nats-operator-dev -n nats-demo rollout status deployment/nats-operator --timeout=180s
python3 tests/lameduck.py baseline
```

Results are written to `.state/lameduck-baseline.json`.

## Trade-off experiment: 30-second duration

Status: designed 2026-11-24, after the negative result above and before any
trial of this comparison. The decision rule was fixed before collecting
results.

### Question

Is a shorter lame-duck duration worth its cost during an operator-driven
upgrade with many connected clients? The benefit is a shorter rollout. The
cost is that evicted clients reconnect in a tighter burst.

### Arms

- **A:** the current operator, which inherits NATS's defaults (10s grace,
  2m duration).
- **B:** the same operator with `lame_duck_grace_period: "10s"` and
  `lame_duck_duration: "30s"` in the generated configuration. These match the
  official chart. The termination grace period stays 120s. This is a
  fixed configuration value, not an API field.

Each arm runs on its own fresh lab built from its source. The harness checks
the generated configuration matches the arm before starting.

### Procedure

1. Create a three-server cluster on NATS 2.15.2 and an R3 stream with 100
   seed messages.
2. Open 1,000 core NATS connections through the client Service, using the
   client library's default reconnect behavior with unlimited attempts. Each
   connection records every disconnect and reconnect.
3. Start one JetStream publisher on the client Service, 1 message every
   20ms with message-ID retries.
4. After 30 seconds of steady state, request the upgrade to 2.15.3. Watch
   every pod and record each NATS container's exit code and its time from
   delete request to exit.
5. Wait for the rollout to finish, let both clients run to their fixed end,
   then verify every acknowledged payload.

### Measurements

- Rollout seconds, from the upgrade request until every pod runs 2.15.3 and the
  resource is Ready.
- Per restart: exit code and seconds from the delete request to exit.
- Clients: disconnects, reconnects, connections still disconnected at the
  end, p50/p99/max time from disconnect to reconnect, and the peak number of
  reconnects in any one second.
- Publisher: retry errors, longest acknowledgement gap, and payload integrity.

### Decision rule

Adopt B only if all of these hold:

1. B's rollout takes at most 70% of A's.
2. Every restart in both arms exits 0.
3. In B, no connection is still disconnected at the end, the publisher job
   succeeds, and every acknowledged payload verifies.
4. B's longest publisher gap is at most the larger of 2× A's and A's plus
   1 second. B's retry errors are at most 2× A's plus 5.
5. B's p99 reconnect time is at most the larger of 2× A's and 1 second.

Peak reconnect rate is reported, but it does not decide the result by itself.
It describes the mechanism, not harm. Otherwise the result is recorded as a
trade-off and A stays. With one rollout per arm, this decides only for the
tested scale and host. It is not a general performance claim.

### Results — 2026-11-24: adopted

One rollout per arm, each on a fresh Kind v0.33.0 lab (Kubernetes v1.35.8,
one Docker host). The harness confirmed each arm's generated configuration
before starting.

| Measurement | A: 10s + 2m (defaults) | B: 10s + 30s |
| --- | ---: | ---: |
| Rollout seconds | 283.3 | 118.6 |
| Restart exit codes | 0, 0, 0 | 0, 0, 0 |
| Seconds from delete request to exit | 78, 79, 81 | 24, 24, 24 |
| Client disconnects / reconnects | 1,560 / 1,560 | 1,587 / 1,587 |
| Still disconnected at end | 0 | 0 |
| Reconnect p50 / p99 / max, ms | 1 / 2 / 12 | 1 / 1 / 5 |
| Peak reconnects in one second | 13 | 60 |
| Publisher retry errors | 0 | 0 |
| Longest publisher gap, seconds | 0.27 | 0.38 |
| Acknowledged / verified / missing | 25,736 / 25,836 / 0 | 25,652 / 25,752 / 0 |

Verified counts include the 100 seed messages.

All five conditions held. B's rollout took 42% of A's time. Every restart
exited cleanly. No connection stayed disconnected and every payload verified.
The publisher's longest gap was 0.38s against a limit of 1.27s. The p99
reconnect time was 1ms against a limit of 1s. **B is adopted**, and the
operator now writes these two settings into every cluster's configuration.

The expected cost showed up as a burstier reconnect rate: 60 per second
against 13. At 1,000 clients on one host, that had no measured effect on
reconnect latency or on the publisher. The test did not cover much larger
client counts, servers under heavy load or clients with authentication
callouts. A separate run below tested the transition for existing clusters.

Reproduce each arm on its own fresh lab after `just up`. Switch the
controller to the baseline build as shown above for arm A. Use the quickstart
controller for arm B.

```sh
python3 tests/lameduck_tradeoff.py A   # on a build without the settings
python3 tests/lameduck_tradeoff.py B   # on the current build
```

### Trade-off rerun — 2026-11-28

With the updated reconnect probe, the baseline rollout took 279 seconds and
the short-drain rollout took 104 seconds. All six broker exits were clean.
Every client reconnected and both arms verified all acknowledged payloads.
The short-drain arm peaked at 59 reconnects per second at 3 ms p99, compared
with 13 per second at 10 ms p99 for the baseline.

The short-drain arm had one publisher retry and a 3.22-second acknowledgement
gap during the final broker's drain. The gap exceeded the 1.62-second limit
derived from the baseline's 0.62-second gap under the original decision rule.
The probe's publish timeout is three seconds, and its samples do not establish
how long the server-side delay lasted.

An isolated repeat of the short-drain arm finished in 106 seconds, with
three clean 24-second exits and all clients reconnected. The maximum
acknowledgement gap was 0.71 seconds, with zero retries and all 25,857 seed and
acknowledged payloads verified. This repeat met the timing thresholds against
the same baseline. The two short-drain runs give mixed evidence on the
publisher gap.

## Existing-cluster transition — 2026-11-28

A three-broker NATS 2.15.2 cluster started with controller `ee10389` in a
fresh namespace on the Kind lab. The controller-only update added the timing
settings to the ConfigMap, preserved all broker UIDs, and left the 100 seed
payloads readable.

Next, a guarded patch upgrade to 2.15.3 replaced all three brokers. Each
original broker held 300 clients and exited cleanly after 79–81 seconds,
using its startup settings. A subsequent deliberate restart of one replacement
broker with 300 clients exited cleanly in 24 seconds. The first client closed
after 11.9 seconds and all 300 closed. The publisher acknowledged 3,456
messages during the test with zero retries. Verification found every seed and
acknowledged payload after the rollout and subsequent restart.
