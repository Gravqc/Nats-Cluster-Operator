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

Reproduce from a fresh lab after `just up`:

```sh
python3 tests/lameduck.py baseline
```

Results are written to `.state/lameduck-baseline.json`.
