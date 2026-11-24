#!/usr/bin/env python3
"""Compare rollout cost of two lame-duck settings. See docs/lame-duck-experiment.md.

Usage: python3 tests/lameduck_tradeoff.py A|B   (one arm per fresh lab)
Results are written to .state/lameduck-tradeoff-<arm>.json.
"""

from datetime import datetime
import json
import subprocess
import sys
import threading
import time

from e2e import ROOT, SEED_COUNT, get, job, kubectl, probe, ready, wait
from upgrade_comparison import pod_ready, pods

ARM = sys.argv[1] if len(sys.argv) > 1 else ""
NAME = "ldt"
OLD, NEW = "2.15.2", "2.15.3"
CONNECTIONS = 1000
DURATION_S = 540
STEADY_S = 30
REPORT = {"arm": ARM, "from": OLD, "to": NEW, "connections": CONNECTIONS}


def seconds(stamp):
    return datetime.fromisoformat(stamp.replace("Z", "+00:00")).timestamp()


class Watch:
    """Collect every watched state of this cluster's pods from kubectl's stream."""

    def __init__(self):
        self.states = []
        self.process = subprocess.Popen(
            [
                "kubectl",
                "--context",
                "kind-nats-operator-dev",
                "-n",
                "nats-demo",
                "get",
                "pods",
                "-l",
                "app.kubernetes.io/instance=" + NAME,
                "--watch",
                "-o",
                "json",
            ],
            stdout=subprocess.PIPE,
            text=True,
        )
        threading.Thread(target=self.read, daemon=True).start()

    def read(self):
        decoder, buffer = json.JSONDecoder(), ""
        for line in self.process.stdout:
            buffer += line
            while True:
                stripped = buffer.lstrip()
                try:
                    state, end = decoder.raw_decode(stripped)
                except json.JSONDecodeError:
                    break
                self.states.append(state)
                buffer = stripped[end:]

    def stop(self):
        self.process.terminate()
        self.process.wait(timeout=10)


def restarts(states):
    by_uid = {}
    for state in states:
        by_uid.setdefault(state["metadata"]["uid"], []).append(state)
    results = []
    for history in by_uid.values():
        deleting = next(
            (s for s in history if s["metadata"].get("deletionTimestamp")), None
        )
        if not deleting:
            continue
        requested = seconds(deleting["metadata"]["deletionTimestamp"]) - deleting[
            "metadata"
        ].get("deletionGracePeriodSeconds", 0)
        terminated = None
        for state in history:
            for status in state.get("status", {}).get("containerStatuses", []):
                if status["name"] == "nats" and "terminated" in status.get("state", {}):
                    terminated = status["state"]["terminated"]
        results.append(
            {
                "pod": deleting["metadata"]["name"],
                "exitCode": terminated["exitCode"] if terminated else None,
                "exitSeconds": seconds(terminated["finishedAt"]) - requested
                if terminated
                else None,
            }
        )
    return sorted(results, key=lambda r: r["pod"])


def main():
    assert ARM in ("A", "B"), "Pass the arm: A or B"
    assert kubectl("get", "statefulset/" + NAME, check=False).returncode != 0, (
        "Use a fresh lab for the trade-off fixture"
    )
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
    obj["metadata"]["name"] = NAME
    obj["spec"]["version"] = OLD
    kubectl("apply", "-f", "-", payload=json.dumps(obj))
    wait(lambda: ready(NAME), "waiting for " + NAME, 300)
    config = get("configmap", NAME)["data"]["nats.conf"]
    REPORT["config"] = config
    expected = 'lame_duck_duration: "30s"' in config
    assert expected == (ARM == "B") and (ARM == "B" or "lame_duck" not in config), (
        "Generated configuration does not match arm " + ARM
    )
    probe("seed-" + NAME, NAME, "seed")
    job("seed-" + NAME)
    timed = {"DURATION_S": str(DURATION_S)}
    probe(
        "churn-" + NAME,
        NAME,
        "churn",
        extra={**timed, "CONNECTIONS": str(CONNECTIONS)},
    )
    probe("load-" + NAME, NAME, "load", extra={**timed, "INTERVAL_MS": "20"})
    clients_end = time.time() + DURATION_S
    wait(
        lambda: sum(
            json.loads(
                kubectl(
                    "--request-timeout=5s",
                    "exec",
                    p["metadata"]["name"],
                    "-c",
                    "nats",
                    "--",
                    "wget",
                    "-T",
                    "2",
                    "-qO-",
                    "http://127.0.0.1:8222/varz",
                ).stdout
            )["connections"]
            for p in pods(NAME)
        )
        >= CONNECTIONS + 1,
        "waiting for client connections",
    )
    time.sleep(STEADY_S)
    watch = Watch()
    started = time.time()
    kubectl(
        "patch",
        "natscluster/" + NAME,
        "--type=merge",
        "-p",
        json.dumps({"spec": {"version": NEW}}),
    )
    deadline = time.time() + DURATION_S
    while time.time() < deadline:
        servers = pods(NAME)
        if (
            ready(NAME)
            and len(servers) == 3
            and all(
                pod_ready(p)
                and not p["metadata"].get("deletionTimestamp")
                and p["spec"]["containers"][0]["image"] == f"nats:{NEW}-alpine"
                for p in servers
            )
        ):
            break
        time.sleep(1)
    else:
        raise RuntimeError("Rollout did not finish")
    REPORT["rolloutSeconds"] = time.time() - started
    time.sleep(3)
    watch.stop()
    REPORT["restarts"] = restarts(watch.states)
    # Every restart must fall inside the clients' fixed run to be measured.
    assert time.time() < clients_end - 10, "Rollout outlasted the measuring clients"
    time.sleep(max(0, clients_end - time.time()))
    job("churn-" + NAME)
    REPORT["clients"] = json.loads(kubectl("logs", "job/churn-" + NAME).stdout)
    job("load-" + NAME)
    load = json.loads(kubectl("logs", "job/load-" + NAME).stdout)
    REPORT["publisher"] = {
        "acknowledged": load["acknowledged"],
        "retry_errors": load["retry_errors"],
        "max_ack_gap_seconds": load["max_gap_ms"] / 1000,
    }
    probe("verify-" + NAME, NAME, "verify", SEED_COUNT + load["acknowledged"])
    job("verify-" + NAME)
    REPORT["verified"] = json.loads(kubectl("logs", "job/verify-" + NAME).stdout)
    REPORT["completed"] = True
    print(
        ARM, json.dumps({k: v for k, v in REPORT.items() if k != "config"}), flush=True
    )


if __name__ == "__main__":
    output = ROOT / ".state" / f"lameduck-tradeoff-{ARM}.json"
    try:
        main()
    except BaseException as error:
        REPORT["error"] = str(error) or type(error).__name__
        raise
    finally:
        output.write_text(json.dumps(REPORT, indent=2) + "\n")
