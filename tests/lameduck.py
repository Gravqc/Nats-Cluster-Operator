#!/usr/bin/env python3
"""Time lame-duck shutdown with many clients. See docs/lame-duck-experiment.md.

Run after local-up in the dedicated lab. Refuses to overwrite its fixture.
Results are written to .state/lameduck.json.
"""

from datetime import datetime
import json
import subprocess
import sys
import threading
import time

from catchup_comparison import vm_offset_ms
from e2e import ROOT, get, job, kubectl, probe, ready, wait
from upgrade_comparison import pod_ready, pods

NAME = "ldm"
VICTIM = NAME + "-2"
CONNECTIONS = 300
TRIALS = 3
LABEL = sys.argv[1] if len(sys.argv) > 1 else "run"
REPORT = {"label": LABEL, "connections": CONNECTIONS, "trials": []}


def seconds(stamp):
    return datetime.fromisoformat(stamp.replace("Z", "+00:00")).timestamp()


def connections(pod):
    response = kubectl(
        "--request-timeout=5s",
        "exec",
        pod,
        "-c",
        "nats",
        "--",
        "wget",
        "-T",
        "2",
        "-qO-",
        "http://127.0.0.1:8222/varz",
        check=False,
    )
    return json.loads(response.stdout)["connections"] if response.returncode == 0 else 0


class Watch:
    """Collect every watched state of one pod name, decoded from kubectl's stream."""

    def __init__(self, pod):
        self.states = []
        self.process = subprocess.Popen(
            [
                "kubectl",
                "--context",
                "kind-nats-operator-dev",
                "-n",
                "nats-demo",
                "get",
                "pod",
                pod,
                "--watch",
                "-o",
                "json",
            ],
            stdout=subprocess.PIPE,
            text=True,
        )
        self.thread = threading.Thread(target=self.read, daemon=True)
        self.thread.start()

    def read(self):
        decoder, buffer = json.JSONDecoder(), ""
        for line in self.process.stdout:
            buffer += line
            try:
                state, end = decoder.raw_decode(buffer.lstrip())
            except json.JSONDecodeError:
                continue
            self.states.append(state)
            buffer = buffer.lstrip()[end:]

    def stop(self):
        self.process.terminate()
        self.process.wait(timeout=10)


def trial(index):
    wait(
        lambda: ready(NAME)
        and len(pods(NAME)) == 3
        and all(map(pod_ready, pods(NAME))),
        "waiting for a Ready cluster",
        300,
    )
    service = NAME + "-headless"
    name = f"hold-{LABEL}-{index}"
    probe(
        name,
        NAME,
        "hold",
        extra={
            "NATS_URL": f"nats://{VICTIM}.{service}:4222",
            "CONNECTIONS": str(CONNECTIONS),
            "HOLD_S": "400",
        },
    )
    wait(lambda: connections(VICTIM) >= CONNECTIONS, "waiting for client connections")
    old_uid = get("pod", VICTIM)["metadata"]["uid"]
    watch = Watch(VICTIM)
    time.sleep(2)
    kubectl("delete", "pod", VICTIM, "--wait=false")
    deadline = time.time() + 200
    while time.time() < deadline:
        if any(s["metadata"]["uid"] != old_uid for s in watch.states):
            break
        time.sleep(1)
    watch.stop()
    old = [s for s in watch.states if s["metadata"]["uid"] == old_uid]
    deleting = next(s for s in old if s["metadata"].get("deletionTimestamp"))
    requested = seconds(deleting["metadata"]["deletionTimestamp"]) - deleting[
        "metadata"
    ].get("deletionGracePeriodSeconds", 0)
    terminated = None
    for state in old:
        for status in state.get("status", {}).get("containerStatuses", []):
            if status["name"] == "nats" and "terminated" in status.get("state", {}):
                terminated = status["state"]["terminated"]
    assert terminated, "Watch did not capture the old NATS container's exit"
    job(name)
    held = json.loads(kubectl("logs", "job/" + name).stdout)
    offset = vm_offset_ms()
    result = {
        "exitCode": terminated["exitCode"],
        "reason": terminated.get("reason"),
        "terminationGraceSeconds": deleting["metadata"].get(
            "deletionGracePeriodSeconds"
        ),
        "exitSeconds": seconds(terminated["finishedAt"]) - requested,
        "connections": held["connections"],
        "closed": held["closed"],
        "firstClosedSeconds": (held["first_closed_ms"] - offset) / 1000 - requested,
        "lastClosedSeconds": (held["last_closed_ms"] - offset) / 1000 - requested,
    }
    REPORT["trials"].append(result)
    print(LABEL, index, json.dumps(result), flush=True)


def main():
    configmap = (
        get("configmap", NAME)
        if kubectl("get", "configmap/" + NAME, check=False).returncode == 0
        else None
    )
    assert configmap is None, "Use a fresh lab for the lame-duck fixture"
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
    kubectl("apply", "-f", "-", payload=json.dumps(obj))
    wait(lambda: ready(NAME), "waiting for " + NAME, 300)
    REPORT["version"] = obj["spec"]["version"]
    REPORT["config"] = get("configmap", NAME)["data"]["nats.conf"]
    pad = {"PAD_BYTES": "8192"}
    probe("seed-" + NAME, NAME, "seed", extra=pad)
    job("seed-" + NAME)
    probe("fill-" + NAME, NAME, "load", count=2000, extra={**pad, "INTERVAL_MS": "1"})
    job("fill-" + NAME)
    for index in range(TRIALS):
        trial(index)
    REPORT["completed"] = True


if __name__ == "__main__":
    output = ROOT / ".state" / f"lameduck-{LABEL}.json"
    try:
        main()
    except BaseException as error:
        REPORT["error"] = str(error) or type(error).__name__
        raise
    finally:
        output.write_text(json.dumps(REPORT, indent=2) + "\n")
