# Lagging-replica rollout experiment

Status: designed 2026-11-24, before any trial was run. The design, expected
outcomes and decision rule were fixed before collecting results. Results are
at the end.

## Question

Does the operator's replica gate change the outcome of a rolling upgrade when
one stream replica is Ready but has not caught up?

The official chart's readiness probe is `/healthz?js-server-only=true`, which
does not consider stream catch-up. Its startup probe uses the full `/healthz`,
so a *restarted* server must catch up before the StatefulSet moves on. Neither
probe stops the StatefulSet from restarting a current replica while a
*different* replica is lagging. The operator inspects every stream and consumer
group through the Raft leader and refuses to restart any server while a replica
is not current.

If the lagging replica is the only other member, restarting a current follower
leaves one current replica in an R3 group. The group cannot commit until the
restarted server returns or the lagging one catches up. Publishers should see
acknowledgement stalls.

## Setup

Both trials use the same Kind lab, pinned chart `nats-2.12.5` (SHA-256
verified), servers on NATS 2.11.14 upgraded to 2.12.5, three workers with
required anti-affinity, the same NATS CPU/memory requests and limits, and 1Gi
claims. Trials run one after the other, never concurrently.

1. Create three servers and an application-created R3 file stream (`EVAL`).
2. Seed 100 messages, then read the stream leader from its own `/jsz`. Step the
   stream leader down until it is on ordinal 0 or 1. Ordinal 2 is the first pod
   a StatefulSet rolling update restarts.
3. The lagging replica is the other server among ordinals 0 and 1. It is a
   follower, and it is not the first pod the chart restarts.
4. Throttle route traffic (TCP port 6222 in either direction) entering that pod.
   This uses a `prio` qdisc with a `netem` rate band on its veth inside the
   Kind node. Client, monitoring and probe traffic is not shaped, so the pod
   stays Ready under the chart's probe.
5. Start one publisher sending padded 8 KiB messages, faster than the throttle
   allows, for a fixed duration. Each retry reuses the message ID. The
   publisher connects to the stream leader's pod, which neither rollout
   restarts first, so a gap during the first restart reflects the stream's
   commit path rather than a client reconnect.
6. Wait until the stream leader has reported the throttled replica as
   not current for 15 consecutive seconds. The 30 seconds of load before the
   upgrade are the **control window**.
7. Request the upgrade. Sample pods and the leader's stream replica state every
   second for a 90-second **gated window**.
8. Remove the throttle if its pod still exists, let the rollout finish, wait
   for the publisher to end, then verify every acknowledged payload by
   sequence.

## Measurements

- Restarts the controller started while the leader reported a non-current
  stream replica. A restart is a pod becoming terminating or getting a new UID.
  Its state is taken from the last sample before that transition.
- Publisher acknowledgement gaps over 1 second, with wall-clock start and end.
  Each window counts the portion of a gap that overlaps it. The
  **attributed gap** is the longest gap overlapping the first restart's
  outage, from the first terminating sample to the replacement becoming
  Ready. It counts only if the publisher's pod kept its UID throughout.
- Retry errors, the longest acknowledgement gap, and how long the rollout took.
- Final integrity: acknowledged count equals verified count, no missing or
  altered payloads.

Samples come from bounded pod-local `wget` calls through `kubectl exec`. They
are 1-second polls, not Raft traces. Pod and host clocks share one Docker VM.

## Expected outcomes

- **Chart:** the StatefulSet restarts ordinal 2 within seconds of the upgrade,
  while the throttled replica is not current. The publisher shows a gap much
  longer than any gap in the control window. Data is still intact afterwards.
- **Operator:** no restart during the gated window, with Ready reporting
  `HealthGateBlocked`. No gap beyond the control-window level. The rollout
  finishes after the throttle is removed, so the operator's rollout takes
  longer.

## Decision rule

The result **supports the gate** only if all of these hold:

- the chart trial restarts at least one pod while a replica is not current;
- that trial's attributed gap is at least 3 times its control-window maximum
  and at least 5 seconds;
- the operator trial restarts nothing while a replica is not current, and its
  gated-window maximum gap is no more than 2 times its control-window maximum;
- both trials verify every acknowledged payload.

Any other combination is recorded as negative or inconclusive. Either way, no
operator code changes on the strength of this experiment unless a trial shows
the operator itself misbehaving. One trial per method cannot establish a
statistical difference. It can only show that this mechanism exists on one
Docker host.

The harness now also requires the chart's first restart to follow a sample
with all three pods Ready and the throttled replica behind. The operator must
show zero restarts during the gated window and at least one sample with all
three pods Ready, a lagging replica, and `HealthGateBlocked` reporting an
unhealthy stream replica for the requested upgrade. The publisher must
cover both measurement windows. These checks were added after the November
24 trials.

## Results — 2026-11-24

Kubernetes v1.35.8 in Kind v0.33.0 on one Docker host, chart `nats-2.12.5`,
NATS 2.11.14 to 2.12.5, route throttle 512 kbit/s, 8 KiB payloads.

### Run 1: invalid instrument

Run 1 is excluded. The chart trial matched run 2. The operator trial failed one
check: it counted one restart while a replica was not current. The samples
show that was the instrument, not the gate. The operator reported `Stabilizing`
with every replica current at lag 0 for the 10 seconds before that restart.
The flagged "before" state showed the restarted server as `offline`. The
sampler listed pods before reading `/jsz`, so a deletion between the two reads
was attributed to the state before it. The sampler now reads replica state
first. Run 2 used a fresh lab and the unchanged decision rule.

### Run 2

| Measurement | Official Helm chart | Operator |
| --- | ---: | ---: |
| Restarts while a stream replica was not current | 3 of 3 | 0 of 3 |
| Restarts in the 90-second gated window | 3 | 0 |
| First restart's outage, seconds | 21.9 | 3.7 |
| Longest gap overlapping first restart, publisher pod stable, seconds | 6.0 | 0 |
| Control-window gap bound, seconds | ≤ 1 | ≤ 1 |
| Longest acknowledgement gap in the whole run, seconds | 8.2 | 0.26 |
| Publisher retry errors | 18 | 0 |
| Rollout seconds from upgrade request | 90.8 | 156.7 |
| Acknowledged / verified / missing | 52,502 / 52,602 / 0 | 55,310 / 55,410 / 0 |

Verified counts include the 100 seed messages.

All five decision checks passed. The result **supports the gate** for this
scenario. During the gated window, the operator's lagging replica stayed
1,015–23,148 messages behind its leader. Ready reported `HealthGateBlocked`
and nothing was restarted. The first restart came 21 seconds after the
throttle was removed, with both followers current at lag 0. The rollout
finished 66 seconds after removal. The chart restarted ordinal 2 within half a
second of the upgrade request. In the last sample before that restart, the
leader reported its lagging replica as not current and 993 messages behind. The publisher, connected to the stream leader's
unrestarted pod, then waited 6.0 seconds for acknowledgements.

### What this shows and does not show

- A StatefulSet rolling update driven by the chart's probes can restart a
  current replica while another Ready replica is behind. In this run that
  stalled acknowledged writes for several seconds without losing data.
- The operator's leader-reported replica gate prevents that restart. It also
  stays blocked for as long as the lag lasts, with no timeout. Rollouts are
  slower, and can be held indefinitely under sustained lag.
- The lag here is induced artificially. How often real clusters have a Ready
  but lagging replica during maintenance was not measured.
- This is one run per method on one Docker host. The chart's later 8.2-second
  gap followed a restart of the publisher's own server, so it mixes client
  reconnection with commit stalls and is not attributed to either.

Reproduce from a fresh lab after `just up`:

```sh
python3 tests/catchup_comparison.py
```

Results are written to `.state/catchup-comparison.json`.

### Rerun with stricter checks — 2026-11-28

The operator reported an unhealthy stream replica in 34 samples where all
three pods were Ready and the throttled follower was behind. It restarted
zero brokers during the gated window and completed the rollout after the
throttle was removed. Its longest acknowledgement gap inside the gated
window was 1.77 seconds, within the 2-second bound.

The chart's first restart followed a sample with a Ready but lagging replica.
Its longest overlapping acknowledgement gap was 2.33 seconds, below the
original 5-second threshold. Both arms verified every acknowledged payload
(54,801 including seeds for the chart and 56,241 for the operator). The
comparative result is **inconclusive** under the original gap threshold.
