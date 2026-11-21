#!/usr/bin/env python3
"""Compare a pinned upstream Helm rollout and the operator on disposable R3 data.

Run after local-up. Refuses to overwrite existing fixtures. Results are sampled
observations on one Docker host, not a proof of production availability.
"""

from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import subprocess
import time
import urllib.request

from e2e import ROOT, get, job, kubectl, probe, ready, wait

OLD = "2.11.14"
NEW = "2.12.5"
CHART_URL = (
    "https://github.com/nats-io/k8s/releases/download/nats-2.12.5/nats-2.12.5.tgz"
)
CHART_SHA256 = "c4beac684b5ffc2cd021e487925788ce3abb941f84062ae6953d927bd0095261"
NAMES = ("eval-helm", "eval-operator")
COUNT = 1500
REPORT = {
    "from": OLD,
    "to": NEW,
    "chart": "nats-2.12.5",
    "chartSHA256": CHART_SHA256,
    "samples": {},
    "results": {},
}


def helm(version):
    path = ROOT / ".state" / "comparison-values.json"
    values = {
        "fullnameOverride": NAMES[0],
        "config": {
            "cluster": {"enabled": True, "replicas": 3},
            "jetstream": {"enabled": True, "fileStore": {"pvc": {"size": "1Gi"}}},
            "merge": {"authorization": {"token": "<< $NATS_AUTH_TOKEN >>"}},
        },
        "container": {
            "image": {"tag": version + "-alpine"},
            "env": {
                "NATS_AUTH_TOKEN": {
                    "valueFrom": {"secretKeyRef": {"name": "nats-auth", "key": "token"}}
                }
            },
            "resources": {
                "requests": {"cpu": "100m", "memory": "128Mi"},
                "limits": {"cpu": "1", "memory": "512Mi"},
            },
        },
        "natsBox": {"enabled": False},
        "reloader": {"enabled": False},
        "podTemplate": {
            "merge": {
                "spec": {
                    "affinity": {
                        "podAntiAffinity": {
                            "requiredDuringSchedulingIgnoredDuringExecution": [
                                {
                                    "labelSelector": {
                                        "matchLabels": {
                                            "app.kubernetes.io/instance": NAMES[0]
                                        }
                                    },
                                    "topologyKey": "kubernetes.io/hostname",
                                }
                            ]
                        }
                    }
                }
            }
        },
    }
    path.write_text(json.dumps(values))
    subprocess.run(
        [
            "helm",
            "upgrade",
            "--install",
            NAMES[0],
            str(ROOT / ".state/comparison-nats.tgz"),
            "--kube-context",
            "kind-nats-operator-dev",
            "-n",
            "nats-demo",
            "-f",
            str(path),
        ],
        check=True,
        capture_output=True,
    )


def pod_ready(pod):
    return any(
        c["type"] == "Ready" and c["status"] == "True"
        for c in pod.get("status", {}).get("conditions", [])
    )


def pods(name):
    return [
        p
        for p in get("pods")["items"]
        if p["metadata"]["name"] in {f"{name}-{i}" for i in range(3)}
    ]


def jsz(name):
    # Read from inside the pod, as the operator's monitoring NetworkPolicy does
    # not grant API-server proxy clients access. Bound both the API handshake
    # and HTTP read so a failed server cannot stall the sampler indefinitely.
    response = kubectl(
        "--request-timeout=5s",
        "exec",
        name,
        "-c",
        "nats",
        "--",
        "wget",
        "-T",
        "2",
        "-qO-",
        "http://127.0.0.1:8222/jsz?accounts=true&streams=true&consumers=true&config=true&raft=true",
        check=False,
    )
    return json.loads(response.stdout) if response.returncode == 0 else None


def sample(name):
    servers = pods(name)
    with ThreadPoolExecutor(max_workers=3) as pool:
        reports = dict(
            zip(
                (p["metadata"]["name"] for p in servers),
                pool.map(jsz, (p["metadata"]["name"] for p in servers)),
                strict=True,
            )
        )
    # A follower can retain stale leader information. Count a leader only when
    # that server's own endpoint confirms it is metadata leader.
    leaders = [
        server
        for server, report in reports.items()
        if report and report.get("meta_cluster", {}).get("leader") == server
    ]
    return {
        "at": time.monotonic(),
        "leader": leaders[0] if len(leaders) == 1 else None,
        "unavailable": 3
        - sum(
            pod_ready(p) and not p["metadata"].get("deletionTimestamp") for p in servers
        ),
        "pods": {
            p["metadata"]["name"]: {
                "uid": p["metadata"]["uid"],
                "terminating": bool(p["metadata"].get("deletionTimestamp")),
                "image": p["spec"]["containers"][0]["image"],
            }
            for p in servers
        },
    }


def upgraded(name):
    servers = pods(name)
    return (
        len(servers) == 3
        and all(
            pod_ready(p)
            and not p["metadata"].get("deletionTimestamp")
            and p["spec"]["containers"][0]["image"] == f"nats:{NEW}-alpine"
            for p in servers
        )
        and (name == NAMES[0] or ready(name))
    )


def recovery_state(observation, statuses, old_id, old_leader):
    status = next((c for c in statuses if c["name"] == "nats"), {})
    terminated = status.get("lastState", {}).get("terminated", {})
    killed = (
        terminated.get("containerID", "").split("://")[-1] == old_id
        and terminated.get("exitCode") == 137
    )
    current_id = status.get("containerID", "").split("://")[-1]
    process_ready = (
        killed
        and bool(current_id)
        and current_id != old_id
        and bool(status.get("ready"))
    )
    # A fast process restart can recover before another server wins an election.
    # A leader with the old name is valid only after proving the old process died.
    leader_confirmed = bool(observation["leader"]) and (
        observation["leader"] != old_leader or killed
    )
    return leader_confirmed, process_ready


def evaluate(name):
    print("Starting upgrade and hard-failure trial:", name, flush=True)
    probe("seed-" + name, name, "seed")
    job("seed-" + name)
    probe("load-" + name, name, "load", count=COUNT)
    wait(
        lambda: any(
            p["metadata"]["name"].startswith("load-" + name + "-")
            and p.get("status", {}).get("phase") == "Running"
            for p in get("pods")["items"]
        ),
        "waiting for publisher",
    )
    time.sleep(3)
    observations = [sample(name)]
    REPORT["samples"][name] = observations
    if name == NAMES[0]:
        helm(NEW)
    else:
        kubectl(
            "patch",
            "natscluster/" + name,
            "--type=merge",
            "-p",
            json.dumps({"spec": {"version": NEW}}),
        )
        wait(
            lambda: get("natscluster", name).get("status", {}).get("upgradeVersion")
            == NEW,
            "waiting for durable upgrade intent",
        )
        # Admission must prevent an overlapping request, including a valid next
        # minor, while a rollout is in progress.
        rejected = kubectl(
            "patch",
            "natscluster/" + name,
            "--dry-run=server",
            "--type=merge",
            "-p",
            '{"spec":{"version":"2.13.0"}}',
            check=False,
        )
        assert rejected.returncode and "current upgrade" in rejected.stderr, (
            rejected.stderr
        )
    deadline = time.monotonic() + 420
    while time.monotonic() < deadline:
        observations.append(sample(name))
        # Ready is a persisted observation, not a synchronized leader report.
        # Require this fresh sample to confirm a leader before the next fault.
        if upgraded(name) and observations[-1]["leader"]:
            break
        time.sleep(1)
    else:
        raise RuntimeError("Upgrade timed out for " + name)
    # A hard process failure is a separate recovery experiment. Keep its
    # observations separate from the deliberate rollout's measurements.
    assert not get("job", "load-" + name).get("status", {}).get("succeeded"), (
        "Publisher finished before fault injection"
    )
    leader = observations[-1]["leader"]
    assert leader, "No confirmed metadata leader for failure injection"
    victim = get("pod", leader)
    node = victim["spec"]["nodeName"]
    assert node.startswith("nats-operator-dev-"), (
        "Refusing fault injection outside the dedicated lab"
    )
    container_id = next(
        c["containerID"].split("://", 1)[1]
        for c in victim["status"]["containerStatuses"]
        if c["name"] == "nats"
    )
    failure_started = time.monotonic()
    subprocess.run(
        [
            "docker",
            "exec",
            node,
            "ctr",
            "--namespace",
            "k8s.io",
            "tasks",
            "kill",
            "--signal",
            "SIGKILL",
            container_id,
        ],
        check=True,
        capture_output=True,
    )
    recovery = []
    REPORT.setdefault("recoverySamples", {})[name] = recovery
    first_new_leader = None
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        observation = sample(name)
        recovery.append(observation)
        statuses = get("pod", leader)["status"].get("containerStatuses", [])
        leader_confirmed, restarted = recovery_state(
            observation, statuses, container_id, leader
        )
        if leader_confirmed and first_new_leader is None:
            first_new_leader = observation["at"] - failure_started
        if (
            first_new_leader is not None
            and restarted
            and observation["unavailable"] == 0
        ):
            break
        time.sleep(1)
    else:
        raise RuntimeError("Metadata-leader process recovery timed out")
    REPORT.setdefault("recoverySamples", {})[name] = recovery
    job("load-" + name)
    load = json.loads(kubectl("logs", "job/load-" + name).stdout)
    probe("verify-" + name, name, "verify", COUNT)
    job("verify-" + name)
    verified = json.loads(kubectl("logs", "job/verify-" + name).stdout)
    assert load["acknowledged"] == COUNT and verified["missing"] == 0
    no_leader = sum(
        b["at"] - a["at"]
        for a, b in zip(observations, observations[1:])
        if a["leader"] is None
    )
    changes = sum(
        a["leader"] != b["leader"]
        for a, b in zip(observations, observations[1:])
        if a["leader"] and b["leader"]
    )
    result = {
        "meta_leader_failure_recovery_seconds": first_new_leader,
        "failed_process_ready_seconds": recovery[-1]["at"] - failure_started,
        "leader_changed_after_failure": recovery[-1]["leader"] != leader,
        "hard_kill_confirmed": True,
        "acknowledged": load["acknowledged"],
        "publish_retry_errors": load["retry_errors"],
        "verified_unique": verified["verified"],
        "missing": verified["missing"],
        "rollout_seconds": observations[-1]["at"] - observations[0]["at"],
        "sampled_no_confirmed_meta_leader_seconds": no_leader,
        "observed_meta_leader_changes": changes,
        "max_unavailable": max(s["unavailable"] for s in observations),
    }
    if name == NAMES[1]:
        assert result["max_unavailable"] <= 1, result
    REPORT["samples"][name] = observations
    REPORT["results"][name] = result
    print(name, json.dumps(result), flush=True)


def verify_admission_baseline():
    # Simulate an unapplied request without starting servers or racing a live
    # rollout. The recorded healthy version must also constrain the next target.
    name = "comparison-admission"
    secret = "comparison-deliberately-missing"
    assert kubectl("get", "natscluster/" + name, check=False).returncode != 0
    assert kubectl("get", "secret/" + secret, check=False).returncode != 0
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
    obj["spec"].update(version=NEW, credentialsSecret=secret)
    obj["spec"]["replicas"] = 5
    rejected = kubectl(
        "apply", "--dry-run=server", "-f", "-", payload=json.dumps(obj), check=False
    )
    assert rejected.returncode and "replicas" in rejected.stderr, rejected.stderr
    REPORT["unverified_five_server_topology_rejected"] = True
    obj["spec"]["replicas"] = 3
    kubectl("apply", "-f", "-", payload=json.dumps(obj))
    try:
        kubectl(
            "patch",
            "natscluster/" + name,
            "--subresource=status",
            "--type=merge",
            "-p",
            json.dumps({"status": {"appliedVersion": OLD}}),
        )
        rejected = kubectl(
            "patch",
            "natscluster/" + name,
            "--dry-run=server",
            "--type=merge",
            "-p",
            '{"spec":{"version":"2.13.0"}}',
            check=False,
        )
        assert (
            rejected.returncode and "last healthy server version" in rejected.stderr
        ), rejected.stderr
        assert kubectl("get", "statefulset/" + name, check=False).returncode != 0
        REPORT["rapid_minor_skip_rejected"] = True
    finally:
        kubectl("delete", "natscluster/" + name, "--wait=true", "--timeout=60s")


def main():
    for name in NAMES:
        assert kubectl("get", "statefulset/" + name, check=False).returncode != 0, (
            "Use a fresh lab or remove only the previous comparison fixtures"
        )
    chart = ROOT / ".state/comparison-nats.tgz"
    if not chart.exists():
        with urllib.request.urlopen(CHART_URL, timeout=60) as response:
            chart.write_bytes(response.read())
    assert hashlib.sha256(chart.read_bytes()).hexdigest() == CHART_SHA256, (
        "Official chart digest mismatch"
    )
    verify_admission_baseline()
    helm(OLD)
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
    obj["metadata"]["name"] = NAMES[1]
    obj["spec"]["version"] = OLD
    kubectl("apply", "-f", "-", payload=json.dumps(obj))
    wait(
        lambda: len(pods(NAMES[0])) == 3
        and all(pod_ready(p) for p in pods(NAMES[0]))
        and ready(NAMES[1]),
        "waiting for both three-server clusters",
        300,
    )
    # Validate the monitoring path before starting a multi-minute load trial.
    for name in NAMES:
        observation = sample(name)
        assert observation["leader"] and observation["unavailable"] == 0, observation
    assert get("natscluster", NAMES[1])["status"]["appliedVersion"] == OLD
    # Run sequentially to avoid one rollout contending with another on this host.
    for name in NAMES:
        evaluate(name)
    assert get("natscluster", NAMES[1])["status"]["appliedVersion"] == NEW
    REPORT["completed"] = True


if __name__ == "__main__":
    try:
        main()
    except BaseException as error:
        REPORT["error"] = str(error) or type(error).__name__
        raise
    finally:
        (ROOT / ".state/upgrade-comparison.json").write_text(
            json.dumps(REPORT, indent=2) + "\n"
        )
