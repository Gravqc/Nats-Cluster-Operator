#!/usr/bin/env python3
"""Compare rollouts while one Ready stream replica lags. See docs/catchup-experiment.md.

Run after local-up in the dedicated lab. Refuses to overwrite existing fixtures.
Results are 1-second samples on one Docker host, not Raft traces.
"""

from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import subprocess
import time
import urllib.request

from e2e import ROOT, SEED_COUNT, get, job, kubectl, probe, ready, wait
from upgrade_comparison import (
    CHART_SHA256,
    CHART_URL,
    NEW,
    OLD,
    helm,
    jsz,
    pod_ready,
    pods,
)

NAMES = ("lag-helm", "lag-operator")
CONTROL_NODE = "nats-operator-dev-control-plane"
PAD_BYTES = 8192
INTERVAL_MS = 5
DURATION_S = 330
THROTTLE = "512kbit"
LAG_HOLD_S = 15
CONTROL_S = 30
GATED_S = 90
REPORT = {
    "from": OLD,
    "to": NEW,
    "chart": "nats-2.12.5",
    "chartSHA256": CHART_SHA256,
    "parameters": {
        "padBytes": PAD_BYTES,
        "intervalMs": INTERVAL_MS,
        "durationSeconds": DURATION_S,
        "routeThrottle": THROTTLE,
        "lagHoldSeconds": LAG_HOLD_S,
        "controlSeconds": CONTROL_S,
        "gatedSeconds": GATED_S,
    },
    "trials": {},
}


def docker(*args, check=True):
    return subprocess.run(
        ["docker", *args], check=check, capture_output=True, text=True
    ).stdout


def vm_offset_ms():
    # Probe timestamps come from the Docker VM clock; samples use the host clock.
    before = time.time() * 1000
    vm = int(docker("exec", CONTROL_NODE, "date", "+%s%3N").strip())
    after = time.time() * 1000
    return vm - (before + after) / 2


def stream_state(name):
    servers = [p["metadata"]["name"] for p in pods(name)]
    with ThreadPoolExecutor(max_workers=3) as pool:
        reports = dict(zip(servers, pool.map(jsz, servers), strict=True))
    # Only the stream leader's own report is authoritative for its followers.
    for server, report in reports.items():
        for account in (report or {}).get("account_details", []):
            for stream in account.get("stream_detail", []):
                cluster = stream.get("cluster") or {}
                if stream.get("name") == "EVAL" and cluster.get("leader") == server:
                    replicas = {
                        r["name"]: {
                            "current": bool(r.get("current")),
                            "offline": bool(r.get("offline")),
                            "lag": r.get("lag", 0),
                        }
                        for r in cluster.get("replicas", [])
                    }
                    return {
                        "leader": server,
                        "replicas": replicas,
                        "allCurrent": len(replicas) == 2
                        and all(
                            r["current"] and not r["offline"] and not r["lag"]
                            for r in replicas.values()
                        ),
                    }
    return {"leader": None, "replicas": {}, "allCurrent": False}


def sample(name):
    # Read replica state before the pod list. A restart is attributed to the
    # previous sample, so its replica state must predate that sample's pod list;
    # the other order can attribute a just-deleted server's shutdown to "before".
    started = time.time()
    stream = stream_state(name)
    servers = pods(name)
    observation = {
        "at": started,
        "stream": stream,
        "pods": {
            p["metadata"]["name"]: {
                "uid": p["metadata"]["uid"],
                "ready": pod_ready(p),
                "terminating": bool(p["metadata"].get("deletionTimestamp")),
                "image": p["spec"]["containers"][0]["image"],
            }
            for p in servers
        },
    }
    if name == NAMES[1]:
        cluster = get("natscluster", name)
        conditions = cluster.get("status", {}).get("conditions", [])
        condition = next(
            (
                c
                for c in conditions
                if c["type"] == "Ready"
                and c.get("observedGeneration") == cluster["metadata"]["generation"]
            ),
            {},
        )
        observation["reason"] = condition.get("reason")
        observation["message"] = condition.get("message", "")
    return observation


def ready_with_lag(observation, lagging):
    servers = observation["pods"]
    replica = observation["stream"]["replicas"].get(lagging)
    return (
        len(servers) == 3
        and all(p["ready"] and not p["terminating"] for p in servers.values())
        and replica is not None
        and not replica["current"]
        and not replica["offline"]
        and replica["lag"] > 0
    )


def longest_gap(gaps, start, end):
    overlaps = (min(g["end"], end) - max(g["start"], start) for g in gaps)
    return max(0.0, max(overlaps, default=0.0))


def sample_for(name, seconds, samples):
    deadline = time.time() + seconds
    while time.time() < deadline:
        samples.append(sample(name))
        time.sleep(1)


def headless_service(name):
    for service in get("services")["items"]:
        spec = service["spec"]
        if (
            spec.get("clusterIP") == "None"
            and spec.get("selector", {}).get("app.kubernetes.io/instance") == name
        ):
            return service["metadata"]["name"]
    raise RuntimeError("No headless Service for " + name)


def throttle(pod):
    node = get("pod", pod)["spec"]["nodeName"]
    assert node.startswith("nats-operator-dev-"), "Refusing to shape outside the lab"
    index = kubectl(
        "exec", pod, "-c", "nats", "--", "cat", "/sys/class/net/eth0/iflink"
    ).stdout.strip()
    device = next(
        line.split(": ")[1].split("@")[0]
        for line in docker("exec", node, "ip", "-o", "link").splitlines()
        if line.split(": ")[0] == index
    )

    def tc(*args):
        docker("exec", node, "tc", *args)

    # Only route traffic entering the pod is shaped. Client, monitoring and
    # kubelet probe traffic stays in the unshaped prio bands.
    tc("qdisc", "replace", "dev", device, "root", "handle", "1:", "prio")
    tc(
        "qdisc",
        "add",
        "dev",
        device,
        "parent",
        "1:3",
        "handle",
        "30:",
        "netem",
        "rate",
        THROTTLE,
    )
    for direction in ("dport", "sport"):
        tc(
            "filter",
            "add",
            "dev",
            device,
            "protocol",
            "ip",
            "parent",
            "1:0",
            "prio",
            "1",
            "u32",
            "match",
            "ip",
            direction,
            "6222",
            "0xffff",
            "flowid",
            "1:3",
        )
    return node, device


def unthrottle(node, device):
    docker("exec", node, "tc", "qdisc", "del", "dev", device, "root", check=False)


def all_ready(name, image=None):
    servers = pods(name)
    return len(servers) == 3 and all(
        pod_ready(p)
        and not p["metadata"].get("deletionTimestamp")
        and (image is None or p["spec"]["containers"][0]["image"] == image)
        for p in servers
    )


def create(name, version):
    if name == NAMES[0]:
        helm(version, name)
        return
    obj = json.loads(
        kubectl(
            "create",
            "--dry-run=client",
            "-f",
            str(ROOT / "examples/ha.yaml"),
            "-o",
            "json",
        ).stdout
    )
    obj["metadata"]["name"] = name
    obj["spec"]["version"] = version
    kubectl("apply", "-f", "-", payload=json.dumps(obj))


def restarts(samples, lagging):
    events = []
    for before, after in zip(samples, samples[1:]):
        for pod, state in before["pods"].items():
            if state["terminating"]:
                continue
            now = after["pods"].get(pod)
            if now is None or now["terminating"] or now["uid"] != state["uid"]:
                events.append(
                    {
                        "pod": pod,
                        "at": after["at"],
                        "streamAllCurrentBefore": before["stream"]["allCurrent"],
                        "readyWithLagBefore": ready_with_lag(before, lagging),
                        "replicasBefore": before["stream"]["replicas"],
                        "oldUID": state["uid"],
                    }
                )
    return events


def outage(samples, event):
    for observation in samples:
        state = observation["pods"].get(event["pod"])
        if (
            observation["at"] > event["at"]
            and state
            and state["uid"] != event["oldUID"]
            and state["ready"]
            and not state["terminating"]
        ):
            return observation["at"]
    return None


def evaluate(name):
    print("Starting lagging-replica trial:", name, flush=True)
    trial = {"samples": []}
    REPORT["trials"][name] = trial
    samples = trial["samples"]
    pad = {"PAD_BYTES": str(PAD_BYTES)}
    probe("seed-" + name, name, "seed", extra=pad)
    job("seed-" + name)
    for attempt in range(6):
        leader = stream_state(name)["leader"]
        if leader in (name + "-0", name + "-1"):
            break
        probe(f"stepdown-{name}-{attempt}", name, "stepdown")
        job(f"stepdown-{name}-{attempt}")
        time.sleep(5)
    else:
        raise RuntimeError("Stream leader stayed on the first restarted ordinal")
    lagging = name + ("-1" if leader == name + "-0" else "-0")
    trial["streamLeader"], trial["laggingReplica"] = leader, lagging
    lagging_uid = get("pod", lagging)["metadata"]["uid"]
    publisher_uid = get("pod", leader)["metadata"]["uid"]
    node, device = throttle(lagging)
    throttled = True
    try:
        url = f"nats://{leader}.{headless_service(name)}:4222"
        trial["publisherURL"] = url
        probe(
            "load-" + name,
            name,
            "load",
            extra={
                **pad,
                "NATS_URL": url,
                "DURATION_S": str(DURATION_S),
                "INTERVAL_MS": str(INTERVAL_MS),
            },
        )
        wait(
            lambda: any(
                p["metadata"]["name"].startswith("load-" + name + "-")
                and p.get("status", {}).get("phase") == "Running"
                for p in get("pods")["items"]
            ),
            "waiting for publisher",
        )
        # Require a sustained, leader-reported lag before the control window.
        held_since = None
        deadline = time.time() + 180
        while time.time() < deadline:
            observation = sample(name)
            samples.append(observation)
            behind = observation["stream"]["leader"] == leader and ready_with_lag(
                observation, lagging
            )
            held_since = (held_since or observation["at"]) if behind else None
            if held_since and observation["at"] - held_since >= LAG_HOLD_S:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Throttled replica did not stay behind")
        assert all_ready(name), "Every server must be Ready before the upgrade"
        trial["controlStart"] = time.time()
        sample_for(name, CONTROL_S, samples)
        assert ready_with_lag(samples[-1], lagging), (
            "Expected a Ready but lagging replica immediately before the upgrade"
        )
        trial["upgradeRequested"] = time.time()
        if name == NAMES[0]:
            helm(NEW, name)
        else:
            kubectl(
                "patch",
                "natscluster/" + name,
                "--type=merge",
                "-p",
                json.dumps({"spec": {"version": NEW}}),
            )
        sample_for(name, GATED_S, samples)
        trial["gatedEnd"] = time.time()
        current = get("pod", lagging)["metadata"]["uid"]
        trial["laggingReplicaReplacedDuringGate"] = current != lagging_uid
        if current == lagging_uid:
            unthrottle(node, device)
        throttled = False
        trial["throttleRemoved"] = time.time()
        deadline = time.time() + 480
        while time.time() < deadline:
            samples.append(sample(name))
            if all_ready(name, f"nats:{NEW}-alpine") and (
                name == NAMES[0] or ready(name)
            ):
                break
            time.sleep(1)
        else:
            raise RuntimeError("Rollout did not finish for " + name)
        trial["rolloutFinished"] = time.time()
    finally:
        if throttled:
            unthrottle(node, device)
    job("load-" + name)
    load = json.loads(kubectl("logs", "job/load-" + name).stdout)
    offset = vm_offset_ms()
    trial["vmClockOffsetMs"] = offset
    assert (load["started_ms"] - offset) / 1000 <= trial["controlStart"] and (
        load["ended_ms"] - offset
    ) / 1000 >= trial["gatedEnd"], "Publisher must cover both measurement windows"
    probe(
        "verify-" + name,
        name,
        "verify",
        SEED_COUNT + load["acknowledged"],
        extra=pad,
    )
    job("verify-" + name)
    verified = json.loads(kubectl("logs", "job/verify-" + name).stdout)
    gaps = [
        {
            "start": (g["start_ms"] - offset) / 1000,
            "end": (g["end_ms"] - offset) / 1000,
        }
        for g in load["gaps"]
    ]
    for g in gaps:
        g["seconds"] = g["end"] - g["start"]

    requested = trial["upgradeRequested"]
    gated_end = trial["gatedEnd"]
    events = restarts(samples, lagging)
    rollout_events = [e for e in events if requested <= e["at"]]
    first = rollout_events[0] if rollout_events else None
    attributed = None
    if first:
        recovered = outage(samples, first) or trial["rolloutFinished"]
        window = [s for s in samples if first["at"] - 1 <= s["at"] <= recovered]
        publisher_stable = all(
            s["pods"].get(leader, {}).get("uid") == publisher_uid for s in window
        )
        overlapping = [
            g["seconds"]
            for g in gaps
            if g["start"] < recovered and g["end"] > first["at"] - 1
        ]
        attributed = {
            "pod": first["pod"],
            "outageSeconds": recovered - first["at"],
            "publisherPodStable": publisher_stable,
            "readyWithLagBefore": first["readyWithLagBefore"],
            "longestOverlappingGapSeconds": max(overlapping, default=0.0),
        }
    # Gaps under 1 second are not recorded, so 1 second bounds a quiet control.
    control_max = max(longest_gap(gaps, trial["controlStart"], requested), 1.0)
    result = {
        "acknowledged": load["acknowledged"],
        "publish_retry_errors": load["retry_errors"],
        "max_ack_gap_seconds": load["max_gap_ms"] / 1000,
        "control_max_gap_seconds_bound": control_max,
        "gated_max_gap_seconds": longest_gap(gaps, requested, gated_end),
        "restarts_while_replica_not_current": sum(
            not e["streamAllCurrentBefore"] for e in rollout_events
        ),
        "restarts_during_gated_window": sum(
            e["at"] < gated_end for e in rollout_events
        ),
        "first_restart": attributed,
        "rollout_seconds": trial["rolloutFinished"] - trial["upgradeRequested"],
        "verified_unique": verified["verified"],
        "missing": verified["missing"],
        "integrity": verified["verified"] == SEED_COUNT + load["acknowledged"]
        and verified["missing"] == 0,
    }
    if name == NAMES[1]:
        gated_samples = [s for s in samples if requested <= s["at"] < gated_end]
        result["blocked_with_ready_lag_samples"] = sum(
            ready_with_lag(s, lagging)
            and s["reason"] == "HealthGateBlocked"
            and s["message"].startswith("A stream replica is unhealthy on ")
            for s in gated_samples
        )
        result["gated_reasons"] = sorted({s["reason"] for s in gated_samples} - {None})
    trial["gaps"] = gaps
    trial["restarts"] = events
    trial["result"] = result
    print(name, json.dumps(result), flush=True)
    return result


def decide(chart, operator):
    first = chart["first_restart"] or {}
    checks = {
        "chart_first_restart_with_ready_lag": bool(first.get("readyWithLagBefore")),
        "chart_attributed_gap_large": bool(first.get("publisherPodStable"))
        and first.get("longestOverlappingGapSeconds", 0)
        >= max(3 * chart["control_max_gap_seconds_bound"], 5),
        "operator_blocked_with_ready_lag": operator["blocked_with_ready_lag_samples"]
        > 0,
        "operator_no_restart_while_gated": operator["restarts_during_gated_window"] == 0
        and operator["restarts_while_replica_not_current"] == 0,
        "operator_gated_gap_bounded": operator["gated_max_gap_seconds"]
        <= 2 * operator["control_max_gap_seconds_bound"],
        "both_integrity": chart["integrity"] and operator["integrity"],
    }
    return {"checks": checks, "supportsGate": all(checks.values())}


def main():
    for name in NAMES:
        assert kubectl("get", "statefulset/" + name, check=False).returncode != 0, (
            "Use a fresh lab or remove only the previous lagging-replica fixtures"
        )
    chart = ROOT / ".state/comparison-nats.tgz"
    if not chart.exists():
        with urllib.request.urlopen(CHART_URL, timeout=60) as response:
            chart.write_bytes(response.read())
    assert hashlib.sha256(chart.read_bytes()).hexdigest() == CHART_SHA256, (
        "Official chart digest mismatch"
    )
    results = {}
    # Run sequentially; only one fixture carries load at any time.
    for name in NAMES:
        create(name, OLD)
        wait(
            lambda: all_ready(name) and (name == NAMES[0] or ready(name)),
            "waiting for " + name,
            300,
        )
        results[name] = evaluate(name)
    REPORT["decision"] = decide(results[NAMES[0]], results[NAMES[1]])
    REPORT["completed"] = True
    print(json.dumps(REPORT["decision"]), flush=True)


if __name__ == "__main__":
    output = Path(ROOT / ".state/catchup-comparison.json")
    try:
        main()
    except BaseException as error:
        REPORT["error"] = str(error) or type(error).__name__
        raise
    finally:
        output.write_text(json.dumps(REPORT, indent=2) + "\n")
