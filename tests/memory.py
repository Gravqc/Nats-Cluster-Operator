#!/usr/bin/env python3
"""Verify optional runtime memory policy in the dedicated Kind lab."""

import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
os.environ["KUBECONFIG"] = str(ROOT / ".state/kubeconfig")
KUBE = ["kubectl", "--context", "kind-nats-operator-dev", "-n", "nats-demo"]


def kube(*args, payload=None, check=True):
    return subprocess.run(
        KUBE + list(args), input=payload, text=True, capture_output=True, check=check
    )


def main():
    resource = json.loads(
        kube(
            "create",
            "--dry-run=client",
            "-f",
            str(ROOT / "examples/local.yaml"),
            "-o",
            "json",
        ).stdout
    )
    resource["metadata"]["name"] = "memory-policy"
    resource["spec"].update(goMemoryLimitPercent=80, goGCPercent=75)
    # The API must reject policies without a memory limit before any pod exists.
    invalid = json.loads(json.dumps(resource))
    invalid["metadata"]["name"] = "memory-invalid"
    invalid["spec"]["resources"].pop("limits", None)
    rejected = kube(
        "apply", "--dry-run=server", "-f", "-", payload=json.dumps(invalid), check=False
    )
    assert rejected.returncode and "goMemoryLimitPercent" in rejected.stderr, (
        rejected.stderr
    )
    kube("apply", "-f", "-", payload=json.dumps(resource))
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        pod = kube("get", "pod/memory-policy-0", "-o", "json", check=False)
        if pod.returncode == 0:
            data = json.loads(pod.stdout)
            if any(
                c["type"] == "Ready" and c["status"] == "True"
                for c in data.get("status", {}).get("conditions", [])
            ):
                break
        time.sleep(2)
    else:
        raise RuntimeError("Memory-policy broker did not become Ready")
    report = json.loads(
        kube(
            "exec",
            "memory-policy-0",
            "-c",
            "nats",
            "--",
            "wget",
            "-qO-",
            "http://127.0.0.1:8222/varz",
        ).stdout
    )
    limit = int(
        kube("exec", "memory-policy-0", "-c", "nats", "--", "printenv", "GOMEMLIMIT")
        .stdout.strip()
        .removesuffix("B")
    )
    assert report["gomemlimit"] == limit, report
    assert limit == 512 * (1 << 20) * 80 // 100, limit
    assert (
        kube(
            "exec", "memory-policy-0", "-c", "nats", "--", "printenv", "GOGC"
        ).stdout.strip()
        == "75"
    )
    print(
        json.dumps(
            {
                "memory_policy": "passed",
                "gomemlimit": limit,
                "gogc": 75,
                "missing_limit_rejected": True,
            }
        )
    )


if __name__ == "__main__":
    main()
