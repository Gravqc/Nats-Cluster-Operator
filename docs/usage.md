# Using the operator

## Prerequisites and installation

Follow the local commands in README.md. A fresh clone needs Docker, Kind, Helm, kubectl, Python 3, OpenSSL, Just and Make. Local builds use Go inside Docker. Published installation requires a reachable operator image; the release checklist describes preparing one. Each managed namespace gets its own operator installation and Secret. Every cluster name in that namespace must be unique.

## Describe a cluster

```yaml
apiVersion: nats.gravqc.github.io/v1alpha1
kind: NatsCluster
metadata:
  name: messaging
spec:
  version: "2.15.3"
  replicas: 3
  credentialsSecret: nats-auth
  storage: 10Gi
  storageClassName: standard
  resources:
    requests:
      cpu: 250m
      memory: 256Mi
    limits:
      cpu: "1"
      memory: 512Mi
  podMonitor: false
```

Use the storage class available in your cluster. Omit storageClassName to use its default. The cluster name must be a lowercase DNS label beginning with a letter, with at most 40 characters. Set replicas to one for a local standalone instance, or three for a replicated cluster. Three-server clusters require separate eligible workers for their pods. The optional nodeSelector and tolerations fields use Kubernetes pod scheduling syntax.

The Secret must exist in the same namespace and contain a `token` key. New
clusters accept 1 to 3,072 bytes, leaving room for the rest of the NATS
`CONNECT` command. Use `nats_` followed by random hexadecimal characters as in the
quickstart. Other supported tokens start with an ASCII letter, digit or
underscore and use only ASCII letters, digits or the characters `_+./=-`.
The operator rejects numeric forms and NATS boolean literals for new
clusters. Previously healthy clusters keep their legacy format and longer
tokens whose minimal `CONNECT` JSON argument fits the broker's 4,096-byte
limit. Additional client fields count toward the same limit.

`version` is an exact upstream NATS server version of the form
`2.<minor>.<patch>`. Minor and patch each have at most nine digits, with no
leading zeros. For example, use `2.15.4`. The operator constructs
`nats:<version>-alpine`. Verify image availability for your architecture
before applying the resource.

`storage` is the per-broker claim size, expressed as a positive whole number
of `Mi`, `Gi` or `Ti`. The storage provider determines quota enforcement.
`resources` applies to the NATS container. The metrics exporter has its own
requests and limits. `podMonitor: true` requires the monitoring CRD at
creation and a Prometheus installation configured to select the monitor.

Every field except version is immutable after creation. Changing the Secret contents is also outside the supported rotation workflow because credentials are read into the server environment at startup.

Optional `goMemoryLimitPercent` and `goGCPercent` are creation-time settings. See [memory policy](memory.md) for their meaning, limits and the difference from Linux page cache.

## Observe readiness

```sh
just status
just logs
export KUBECONFIG="$PWD/.state/kubeconfig"
kubectl --context kind-nats-operator-dev -n nats-demo describe natsclusters.nats.gravqc.github.io/local
```

status.clientURL contains the in-cluster address. status.servers reports observed server versions, connections, memory and health. status.metaLeader is available for clustered JetStream. Ready=False includes the current reason, such as CredentialsMissing, HealthGateBlocked, WaitingForPod, Stabilizing or Upgrading. Missing prerequisites can also produce ReconcileFailed. Ready=True confirms the monitored server topology and pod readiness at the last observation. Applications must provision the streams and consumers they use.

Use a client connection to verify authentication and reachability from the
application's network. Enforcement of the generated NetworkPolicy depends on
the cluster's CNI.

The default local example has one server. The monitored, platform and other workloads seen after the e2e suite are test fixtures; other remains running as part of the retained-workload deletion scenario. Completed probe Jobs can also remain in the namespace. Use a fresh lab for another evaluation run.

## Connect a local client

Run the port-forward from README.md in a separate terminal, then load the generated token into your current shell:

```sh
export KUBECONFIG="$PWD/.state/kubeconfig"
export NATS_TOKEN="$(kubectl --context kind-nats-operator-dev -n nats-demo get secret nats-auth -o jsonpath='{.data.token}' | python3 -c 'import base64,sys; sys.stdout.write(base64.b64decode(sys.stdin.read()).decode())')"
export NATS_URL="nats://127.0.0.1:14222"
```

Supply NATS_URL and NATS_TOKEN through your client's connection options. The token variable is local to this shell. Keep its value out of issue reports and committed configuration. The operator exposes the client port inside Kubernetes; the host connection uses the explicit port-forward.

## Guarded upgrade

Create a separate HA example using version 2.15.2, wait until Ready, then request 2.15.3:

```sh
kubectl --context kind-nats-operator-dev -n nats-demo patch natsclusters.nats.gravqc.github.io/platform \
  --type=merge -p '{"spec":{"version":"2.15.3"}}'
kubectl --context kind-nats-operator-dev -n nats-demo get natsclusters.nats.gravqc.github.io/platform -w
```

Forward patches and adjacent-minor upgrades are accepted. Downgrades and
skipped minors are rejected. Once the controller persists an active target
in `status.upgradeVersion`, admission rejects a different target until the
rollout finishes. Compatibility evidence covers the version pairs in
[evaluation.md](evaluation.md). Check upstream release notes and image
availability before requesting an upgrade. An unpullable image can strand
a replacement pod while the target is locked. Recovery requires administrator
intervention outside the upgrade API.

For a resource admitted with an invalid legacy version, restore the last
healthy canonical version shown in `status.appliedVersion` when available. An owned
StatefulSet tag that differs only by leading zeros can be corrected to the
equivalent canonical tag. The normal health gate still controls restarts.
If the broker container never started and is stuck in `ErrImagePull` or
`ImagePullBackOff`, have an administrator confirm the StatefulSet template
is canonical and replace the failed pod while keeping its PVC. Check that
the replacement becomes Ready and verify existing payloads.

A previously admitted oversized tag has a narrower automatic recovery
path. For an owned StatefulSet without a recorded healthy version, the
controller can correct a scaled-down template or replace a pod in image
pull failure when Kubernetes records no broker start. The controller keeps
the PVCs and stops automatic recovery if it sees evidence that an outdated
broker started. Ordinary upgrades still use the health gate.

Use status.observedGeneration together with the Ready condition when observing a spec change; an immediately returned Ready condition can belong to the previous generation. The e2e script performs this check. Upgrades can wait indefinitely while replica health is uncertain. A single-server upgrade interrupts service during its restart. The controller uses OnDelete StatefulSets and coordinates individual replacement pods. Read design.md for the gate, persistence and failure model.

## Stop, resume and remove

just stop stops the four Docker containers hosting this Kind lab and preserves their storage. just up starts them again and reapplies the local installation. just down deletes the whole disposable Kubernetes cluster and its local data. Other Docker projects and Kubernetes clusters are outside these commands.

Use `--cascade=orphan` when deleting a NatsCluster to keep its running workload, endpoint and disks. A later CR with the same name does not adopt them automatically. Keep the controller running until finalization completes. See migration.md for ownership and data-transition details.

## Troubleshooting

- Docker shows worker and control-plane containers. Those are the Kubernetes nodes. Use just status to see the NATS pods inside them.
- Connection refused after just stop. Run just up and wait for the API server and cluster readiness.
- Pending pods. Check PVC binding, the storage class, free CPU/memory, node selectors, tolerations and the required spread across workers.
- CredentialsMissing. Confirm the referenced Secret name and its token key in the managed namespace. Keep the token out of logs and issue reports.
- CredentialsInvalid. Check the token format and byte limit. Before provisioning, correct the referenced Secret and wait for reconciliation. For a running cluster, plan credential recovery separately because servers read the Secret at startup. Keep the token out of logs and issue reports.
- InvalidVersion. Correct the legacy version in the CR before reconciliation can continue. The guarded upgrade section describes the recovery path.
- PodMonitor reconciliation fails. Install the upstream PodMonitor CRD before setting podMonitor to true.
- Upgrade is blocked. Inspect the condition message, pod readiness and NATS monitoring. Restore replica health and allow the controller to observe recovery.
- Existing resources conflict. Provision with a new cluster name. Automatic resource adoption is outside the current implementation.
- Fresh e2e run fails on existing fixtures. Confirm the lab's data is disposable, then run just down, just up and just evaluate.

For a report, include versions, redacted conditions and the exact commands. The community issue template asks for those details.
