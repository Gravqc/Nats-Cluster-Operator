#!/usr/bin/env python3
"""Destructive evaluation limited to the named disposable Kind cluster."""

import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
os.environ["KUBECONFIG"] = str(ROOT / ".state/kubeconfig")
KUBE = ["kubectl", "--context", "kind-nats-operator-dev", "-n", "nats-demo"]
SEED_COUNT = 100
PATCH_LOAD_COUNT = 900
REPORT = {"cluster": "nats-operator-dev", "checks": {}}


def kubectl(*args, payload=None, check=True):
    result = subprocess.run(
        KUBE + list(args), input=payload, text=True, capture_output=True
    )
    if check and result.returncode:
        raise RuntimeError(result.stderr or result.stdout)
    return result


def get(kind, name=None):
    args = ["get", kind]
    if name:
        args.append(name)
    return json.loads(kubectl(*args, "-o", "json").stdout)


def wait(predicate, description, seconds=240):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(2)
    raise RuntimeError("Timed out " + description)


def ready(name):
    cluster = get("natscluster", name)
    return cluster.get("status", {}).get("observedGeneration") == cluster["metadata"][
        "generation"
    ] and any(
        item["type"] == "Ready" and item["status"] == "True"
        for item in cluster.get("status", {}).get("conditions", [])
    )


def record(name):
    REPORT["checks"][name] = "passed"
    print("PASS", name, flush=True)


def cluster(name):
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
    obj["spec"]["version"] = "2.15.2"
    kubectl("apply", "-f", "-", payload=json.dumps(obj))
    wait(lambda: ready(name), "waiting for " + name)


def probe(name, cluster_name, mode, expected=SEED_COUNT, count=None, extra=None):
    values = {
        "NATS_URL": "nats://" + cluster_name + ":4222",
        "CLUSTER": cluster_name,
        "MODE": mode,
        "EXPECTED": str(expected),
        "REPLICAS": "3",
    }
    values.update(extra or {})
    env = [{"name": key, "value": value} for key, value in values.items()]
    if count is not None:
        env.append({"name": "COUNT", "value": str(count)})
    env.append(
        {
            "name": "NATS_TOKEN",
            "valueFrom": {"secretKeyRef": {"name": "nats-auth", "key": "token"}},
        }
    )
    obj = {
        "apiVersion": "batch/v1",
        "kind": "Job",
        "metadata": {"name": name},
        "spec": {
            "backoffLimit": 0,
            "activeDeadlineSeconds": 600,
            "template": {
                "spec": {
                    "restartPolicy": "Never",
                    "containers": [
                        {
                            "name": "probe",
                            "image": "nats-cluster-operator:0.1.0",
                            "imagePullPolicy": "IfNotPresent",
                            "command": ["/probe"],
                            "env": env,
                        }
                    ],
                }
            },
        },
    }
    kubectl("apply", "-f", "-", payload=json.dumps(obj))


def job(name):
    def terminal():
        conditions = get("job", name).get("status", {}).get("conditions", [])
        for condition in conditions:
            if condition["status"] != "True":
                continue
            if condition["type"] == "Failed":
                logs = kubectl("logs", "job/" + name, check=False)
                detail = " ".join(
                    value
                    for key in ("reason", "message")
                    if (value := condition.get(key))
                )
                output = (logs.stdout or logs.stderr).strip()
                raise RuntimeError(f"Job {name} failed. {detail}\n{output}".rstrip())
        return any(
            c["type"] == "Complete" and c["status"] == "True" for c in conditions
        )

    wait(terminal, "waiting for Job " + name, seconds=360)
    logs = kubectl("logs", "job/" + name).stdout.strip()
    REPORT.setdefault("probeResults", {})[name] = json.loads(logs)


def pod_ids():
    return {
        p["metadata"]["name"]: p["metadata"]["uid"]
        for p in get("pods")["items"]
        if p["metadata"]["name"].startswith("platform-")
    }


def set_server_paused(paused):
    pod = get("pod", "platform-0")
    node = pod["spec"]["nodeName"]
    if not node.startswith("nats-operator-dev-"):
        raise RuntimeError("Fault injection requires the dedicated Kind nodes")
    container_id = next(
        c["containerID"].split("://", 1)[1]
        for c in pod["status"]["containerStatuses"]
        if c["name"] == "nats"
    )
    action = "pause" if paused else "resume"
    subprocess.run(
        [
            "docker",
            "exec",
            node,
            "ctr",
            "--namespace",
            "k8s.io",
            "tasks",
            action,
            container_id,
        ],
        check=True,
        capture_output=True,
    )
    if paused:
        tasks = subprocess.check_output(
            ["docker", "exec", node, "ctr", "--namespace", "k8s.io", "tasks", "list"],
            text=True,
        )
        if not any(
            container_id in line and "PAUSED" in line for line in tasks.splitlines()
        ):
            raise RuntimeError("Container runtime did not pause the NATS task")


def run():
    # The namespace belongs to the quickstart. Re-running requires local-down/up.
    kubectl(
        "apply",
        "--server-side",
        "-f",
        "https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.80.1/example/prometheus-operator-crd/monitoring.coreos.com_podmonitors.yaml",
    )
    monitored = json.loads(
        kubectl(
            "create",
            "--dry-run=client",
            "-f",
            str(ROOT / "examples/local.yaml"),
            "-o",
            "json",
        ).stdout
    )
    for name in ("dotted.name", "1leading-digit"):
        monitored["metadata"]["name"] = name
        rejected = kubectl(
            "create",
            "--dry-run=server",
            "-f",
            "-",
            payload=json.dumps(monitored),
            check=False,
        )
        if rejected.returncode == 0 or "DNS label" not in rejected.stderr:
            raise RuntimeError(
                f"Expected DNS label validation for {name}: {rejected.stderr}"
            )
    monitored["metadata"]["name"] = "monitored"
    monitored["spec"]["podMonitor"] = True
    kubectl("apply", "-f", "-", payload=json.dumps(monitored))
    wait(lambda: ready("monitored"), "waiting for monitored cluster")
    monitor = get("podmonitor", "monitored")
    if (
        monitor["spec"]["selector"]["matchLabels"]["app.kubernetes.io/instance"]
        != "monitored"
    ):
        raise RuntimeError("PodMonitor selector crosses cluster boundary")
    record("podmonitor_accepted_by_upstream_CRD")
    cluster("platform")
    rejected = kubectl(
        "patch",
        "natscluster/platform",
        "--dry-run=server",
        "--type=merge",
        "-p",
        '{"spec":{"version":"2.15.03"}}',
        check=False,
    )
    if rejected.returncode == 0 or "spec.version" not in rejected.stderr:
        raise RuntimeError("Expected canonical version validation: " + rejected.stderr)
    record("invalid_names_and_version_spelling_rejected")
    cluster("other")
    record("two_independent_clusters_ready_from_monitoring")
    for cluster_name in ["platform", "other"]:
        probe("seed-" + cluster_name, cluster_name, "seed")
        job("seed-" + cluster_name)
    record("application_provisions_R3_streams_and_receives_publish_acknowledgements")
    rejected = kubectl(
        "patch",
        "natscluster/platform",
        "--type=merge",
        "-p",
        '{"spec":{"replicas":1}}',
        check=False,
    )
    if rejected.returncode == 0:
        raise RuntimeError("Unimplemented scale-down was accepted")
    record("unsupported_scale_down_rejected")
    # Suspend a server, request an upgrade, and ensure the health gate blocks it.
    old_ids = pod_ids()
    set_server_paused(True)
    try:
        kubectl(
            "patch",
            "natscluster/platform",
            "--type=merge",
            "-p",
            '{"spec":{"version":"2.15.3"}}',
        )
        time.sleep(18)
        if pod_ids() != old_ids:
            raise RuntimeError(
                "Operator restarted a server while another was unresponsive"
            )
        condition = get("natscluster", "platform")["status"]["conditions"][0]
        if condition["status"] != "False":
            raise RuntimeError("Health gate did not report blocked status")
        record("upgrade_blocked_while_server_unhealthy")
        # Prepare traffic while the gate is blocked; the existing stream remains.
        probe("load-upgrade", "platform", "load", count=PATCH_LOAD_COUNT)
    finally:
        set_server_paused(False)
    # Restart the operator during reconciliation. Progress must come from real pods.
    kubectl("rollout", "restart", "deployment/nats-operator")
    deadline = time.monotonic() + 360
    max_unavailable = 0
    while not ready("platform"):
        if time.monotonic() > deadline:
            raise RuntimeError("Guarded upgrade timed out")
        pods = [
            pod
            for pod in get("pods")["items"]
            if pod["metadata"]["name"].startswith("platform-")
        ]
        available = sum(
            any(
                c.get("type") == "Ready" and c.get("status") == "True"
                for c in pod.get("status", {}).get("conditions", [])
            )
            and not pod["metadata"].get("deletionTimestamp")
            for pod in pods
        )
        max_unavailable = max(max_unavailable, 3 - available)
        if max_unavailable > 1:
            raise RuntimeError(
                "More than one server became unavailable during the upgrade"
            )
        time.sleep(1)
    REPORT["maxUnavailableDuringUpgrade"] = max_unavailable
    job("load-upgrade")
    if get("natscluster", "other")["spec"]["version"] != "2.15.2":
        raise RuntimeError("Other cluster version changed")
    for server in get("natscluster", "platform")["status"]["servers"]:
        if server["version"] != "2.15.3":
            raise RuntimeError("Status did not observe upgraded server version")
    record("rolling_patch_upgrade_resumes_after_operator_restart")
    probe("verify-platform", "platform", "verify", SEED_COUNT + PATCH_LOAD_COUNT)
    job("verify-platform")
    probe("verify-other", "other", "verify", 100)
    job("verify-other")
    record("all_acknowledged_messages_retained_and_clusters_isolated")
    # Pod loss must retain the disk and all acknowledged messages.
    kubectl("delete", "pod/platform-1", "--wait=true")
    wait(
        lambda: len(pod_ids()) == 3
        and all(
            any(
                condition.get("type") == "Ready" and condition.get("status") == "True"
                for condition in pod.get("status", {}).get("conditions", [])
            )
            for pod in get("pods")["items"]
            if pod["metadata"]["name"].startswith("platform-")
        ),
        "waiting for replacement pod readiness",
    )
    wait(lambda: ready("platform"), "waiting for pod recovery")
    probe("verify-recovery", "platform", "verify", SEED_COUNT + PATCH_LOAD_COUNT)
    job("verify-recovery")
    record("pod_recovery_preserves_messages")
    services = {
        name: get("service", name)["metadata"]["uid"]
        for name in ("other", "other-headless")
    }
    kubectl(
        "delete",
        "natscluster/other",
        "--cascade=orphan",
        "--wait=true",
        "--timeout=60s",
    )
    if len(get("statefulset", "other")["metadata"].get("ownerReferences", [])):
        raise RuntimeError("Retained StatefulSet still has owner reference")
    for name, uid in services.items():
        if get("service", name)["metadata"]["uid"] != uid:
            raise RuntimeError("Retained Service identity changed for " + name)
    probe("publish-retained", "other", "load", count=1)
    job("publish-retained")
    probe("verify-retained", "other", "verify", SEED_COUNT + 1)
    job("verify-retained")
    record("cluster_deletion_retains_running_workload_and_data")


if __name__ == "__main__":
    try:
        run()
    except Exception as error:
        REPORT["error"] = str(error)
        raise
    finally:
        (ROOT / ".state/evaluation.json").write_text(
            json.dumps(REPORT, indent=2) + "\n"
        )
