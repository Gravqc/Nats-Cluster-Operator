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

Use the storage class available in your cluster. Omit storageClassName to use its default. The Secret must exist in the same namespace and contain a nonempty token key. Set replicas to one for a local standalone instance, or three for a replicated cluster. Three-server clusters require separate eligible workers for their pods. The optional nodeSelector and tolerations fields use Kubernetes pod scheduling syntax.

version is an exact upstream NATS server version. The operator constructs the image tag nats:<version>-alpine. storage is the persistent claim size requested for each server. A PVC request is not a portable filesystem quota; the storage provider determines enforcement. Stream retention and capacity planning remain necessary. resources applies to the NATS container. The operator also adds a pinned metrics exporter with its own small resource requests. podMonitor requires the upstream monitoring CRD before creation. Configure Prometheus to select the emitted monitor.

Every field except version is immutable after creation. Changing the Secret contents is also outside the supported rotation workflow because credentials are read into the server environment at startup.

Optional `goMemoryLimitPercent` and `goGCPercent` are creation-time settings. See [memory policy](memory.md) for their meaning, limits and the difference from Linux page cache.

## Observe readiness

```sh
just status
just logs
export KUBECONFIG="$PWD/.state/kubeconfig"
kubectl --context kind-nats-operator-dev -n nats-demo describe natscluster/local
```

status.clientURL contains the in-cluster address. status.servers reports observed server versions, connections, memory and health. status.metaLeader is available for clustered JetStream. Ready=False includes the current reason, such as CredentialsMissing, HealthGateBlocked, WaitingForPod, Stabilizing or Upgrading. Missing prerequisites can also produce ReconcileFailed. Ready=True confirms the monitored server topology and pod readiness at the last observation. Applications must provision the streams and consumers they use.

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
kubectl --context kind-nats-operator-dev -n nats-demo patch natscluster/platform \
  --type=merge -p '{"spec":{"version":"2.15.3"}}'
kubectl --context kind-nats-operator-dev -n nats-demo get natscluster/platform -w
```

Forward patches and adjacent-minor upgrades are accepted. Downgrades, skipped minors, and overlapping version requests are rejected. The executed compatibility record covers specific version pairs, not all 2.x combinations. Check upstream release notes before selecting versions. `status.upgradeVersion` identifies an active request. There is no cancel, downgrade or forced-health override API. Version admission checks ordering. Before requesting an upgrade, confirm that the exact image can be pulled on the worker architecture and review upstream upgrade prerequisites. An unpullable image can strand the first replacement pod while the active upgrade locks the target. Recovery from that state requires administrator intervention outside the current API.

Use status.observedGeneration together with the Ready condition when observing a spec change; an immediately returned Ready condition can belong to the previous generation. The e2e script performs this check. Upgrades can wait indefinitely while replica health is uncertain. A single-server upgrade interrupts service during its restart. The controller uses OnDelete StatefulSets and coordinates individual replacement pods. Read design.md for the gate, persistence and failure model.

## Stop, resume and remove

just stop stops the four Docker containers hosting this Kind lab and preserves their storage. just up starts them again and reapplies the local installation. just down deletes the whole disposable Kubernetes cluster and its local data. Other Docker projects and Kubernetes clusters are outside these commands.

Deleting an individual NatsCluster retains its workloads and disks by design. A later CR with the same name does not adopt them automatically. Keep the controller running until finalization completes. See migration.md for ownership and data-transition details.

## Troubleshooting

- Docker shows worker and control-plane containers. Those are the Kubernetes nodes. Use just status to see the NATS pods inside them.
- Connection refused after just stop. Run just up and wait for the API server and cluster readiness.
- Pending pods. Check PVC binding, the storage class, free CPU/memory, node selectors, tolerations and the required spread across workers.
- CredentialsMissing. Confirm the referenced Secret name and its token key in the managed namespace. Keep the token out of logs and issue reports.
- PodMonitor reconciliation fails. Install the upstream PodMonitor CRD before setting podMonitor to true.
- Upgrade is blocked. Inspect the condition message, pod readiness and NATS monitoring. Restore replica health and allow the controller to observe recovery.
- Existing resources conflict. Provision with a new cluster name. Automatic resource adoption is outside the current implementation.
- Fresh e2e run fails on existing fixtures. Confirm the lab's data is disposable, then run just down, just up and just evaluate.

For a report, include versions, redacted conditions and the exact commands. The community issue template asks for those details.
