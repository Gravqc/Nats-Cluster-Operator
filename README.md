# NATS Cluster Operator

An experimental Kubernetes operator for provisioning independent NATS clusters from a small `NatsCluster` resource, with persistent JetStream storage and guarded rolling upgrades.

**Status: alpha in development.** The `v1alpha1` API may change. The public image
and chart are pending publication. The [Kind quickstart](#try-it-in-kind) builds
the operator locally.

**Start here:** [Try it in Kind](#try-it-in-kind) ·
[Install on Kubernetes](#install-on-kubernetes) ·
[Add another cluster](#add-another-cluster) ·
[Usage and troubleshooting](docs/usage.md)

## How it fits with NATS

The Helm chart installs the controller and registers the `NatsCluster` resource
type. Each `NatsCluster` you apply requests a separate set of NATS brokers,
Services and disks. The operator reports health and sequences deliberate
restarts during upgrades using live JetStream replica status. Applications can
continue to create streams and consumers through NATS.

The [official NATS Helm chart](https://github.com/nats-io/k8s/tree/main/helm/charts/nats)
deploys NATS directly. [NACK](https://github.com/nats-io/nack) manages JetStream
resources such as streams and consumers, which are outside this operator's
scope. This is an independent project, separate from the
[archived NATS Operator](https://github.com/nats-io/nats-operator).

## Current scope

- Canonical `2.<minor>.<patch>` server versions, with up to nine digits per component and an exact upstream image tag.
- One server for local development, or three servers spread across workers.
- File-backed JetStream, resource requests/limits and scheduling configuration.
- Optional per-broker [Go memory and GC controls](docs/memory.md).
- A user-supplied token Secret, with a 3,072-byte limit for new clusters. Same-namespace client access by default.
- Status from `/healthz`, `/varz` and `/jsz`, including versions, connections and metadata leader. `Ready` reports observed broker and pod health. Verify client authentication and network access separately.
- Forward patch or adjacent-minor upgrades. Before each deliberate restart, all observed replicas must be current and every pod Ready. Metadata leader is considered last. The controller waits for ten seconds of observed stability between restarts.
- Delete a CR with `--cascade=orphan` to retain its running workload, endpoint and disks. Foreground cascading deletion can remove the endpoint. Recreating the CR does not automatically adopt retained resources.

The alpha uses token authentication over a trusted network. Client and route
TLS, scaling, storage expansion, and in-place Helm adoption are outside its
current scope. All spec fields except version are creation-time settings.
See [input formats and recovery steps](docs/usage.md), [tested versions and results](docs/evaluation.md),
[scope](docs/scope.md), and [maintenance limits](docs/operations.md).

## Try it in Kind

Prerequisites are Docker, Kind v0.33.0, Helm 3, kubectl, Python 3, OpenSSL, Just and Make. The local flow builds Go inside Docker, so a host Go installation is optional. Kind creates one named environment, `nats-operator-dev`, using four node containers. The controller and NATS pods run inside that environment. Docker's container view shows the four Kind nodes; these are separate from Compose project groups. Run the commands below from this repository. `just` is a task runner that provides short names for the lab commands. The local recipes delegate to the Makefile used by the Go development tooling. `just status` shows the NATS pods; `just nodes` shows the Docker containers that host Kubernetes.

```sh
just up
export KUBECONFIG="$PWD/.state/kubeconfig"
kubectl --context kind-nats-operator-dev -n nats-demo get natsclusters.nats.gravqc.github.io,pods,pvc
just evaluate
just status
just logs
just nodes
just stop  # Preserve the lab and its data; just up resumes it
just down  # Delete the lab and its local test data
```

`just up` creates one single-server NATS example. On an existing lab it also applies the current CRD and restarts the controller to pick up the rebuilt local image. The e2e suite additionally creates two independent three-server clusters, provisions test streams from a client, blocks an upgrade on an unhealthy server, upgrades under publish load, restarts the controller, verifies acknowledged messages, and checks retained workloads. Start from a fresh local environment when rerunning the suite. Results are written to `.state/evaluation.json`. Additional memory and pinned Helm-versus-operator upgrade checks are described in [evaluation](docs/evaluation.md#reproducing-the-minor-upgrade-comparison).

To access the single-server example from the host, forward its client Service to local port 14222 in one terminal. Read its generated token locally and use that token in your client configuration. The Secret is generated at quickstart time and stays outside Git.

```sh
kubectl --context kind-nats-operator-dev -n nats-demo port-forward service/local 14222:4222
```

Clients on the host then use `nats://127.0.0.1:14222` with the token in the `nats-auth` Secret. The [usage guide](docs/usage.md) explains the API, status, upgrade observation, retained workloads and troubleshooting.

## Install on Kubernetes

The following uses a source checkout and an operator image you have already
built and pushed to a registry your workers can pull from. Use Helm 3, kubectl,
OpenSSL, and a storage class that can provision PVCs. The HA example requires
three eligible workers. Use Kubernetes 1.33 or later for CRD validation
ratcheting with default feature gates. See [the release checklist](docs/releasing.md) for the
planned packaged install and [evaluation](docs/evaluation.md) for the Kubernetes
and NATS versions exercised so far.

### Install the controller

```sh
helm upgrade --install nats-operator ./charts/nats-cluster-operator \
  --namespace messaging --create-namespace \
  --set image.repository=YOUR_REGISTRY/nats-cluster-operator \
  --set image.tag=0.1.0 --wait
```

### Create and verify a cluster

From the checkout, run:

```sh
export NATS_TOKEN="nats_$(openssl rand -hex 24)"
kubectl -n messaging create secret generic nats-auth --from-literal=token="$NATS_TOKEN"
kubectl -n messaging apply -f examples/ha.yaml
kubectl -n messaging wait natsclusters.nats.gravqc.github.io/platform --for=condition=Ready --timeout=240s
kubectl -n messaging get natsclusters.nats.gravqc.github.io/platform
```

The last command should show `True` in the `READY` column and the in-cluster NATS URL. Connect
using the token from the Secret above. The local example shows how to use a
port-forward for a client outside Kubernetes.

The controller watches its installation namespace and uses namespace-scoped RBAC. Use one installation per namespace. Different clusters in one namespace use distinct names and can share workers. PodMonitor requires `monitoring.coreos.com/v1` to be installed first; set `spec.podMonitor: true` when initially creating the cluster, and configure Prometheus to select it.

**Before cleanup:** Use orphan deletion to leave a `NatsCluster`'s brokers,
endpoint and disks in place. Follow the [migration and retention guide](docs/migration.md) before
removing a deployment you want to preserve.

CRDs under Helm's `crds/` directory install on first install and are retained on uninstall. Apply the versioned generated CRD explicitly before upgrading a controller release that changes its API. Deleting the namespace or CRD is destructive and bypasses the normal retained-workload workflow. See [migration](docs/migration.md).

## Add another cluster

Install the controller once in the namespace. Each `NatsCluster` is one
independent cluster with its own Service and disks. To create a second cluster,
copy [the HA example](examples/ha.yaml), set a different `metadata.name`, and
set the exact NATS server version in `spec.version`:

```yaml
apiVersion: nats.gravqc.github.io/v1alpha1
kind: NatsCluster
metadata:
  name: analytics
spec:
  version: "2.12.5"
  replicas: 3
  storage: 1Gi
  credentialsSecret: analytics-nats-auth
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
```

Create the referenced Secret with a `token` key in the same namespace, then
apply the YAML. The existing cluster can keep its own version. Read
`status.clientURL` on each resource for its endpoint. Application code creates
the streams and consumers it needs on that endpoint.

## Development

With Go 1.24 and Make installed:

```sh
make generate manifests test
make image
```

`make generate manifests` uses controller-gen v0.18.0. Generated API deepcopy code and the CRD are checked in. `PROJECT` records the Kubebuilder scaffold. The runtime image is built from scratch and runs without root privileges. CI is configured for formatting, generation drift, unit/race checks, Helm lint and the Kind evaluation. The [hosted Kind run](https://github.com/Gravqc/Nats-Cluster-Operator/actions/runs/44881286843) completed the unit and integration suites. See the [evaluation record](docs/evaluation.md) for the startup failure reproduced on main and the limits of the integrity checks.

## Adoption and publication

The public alpha uses API group `nats.gravqc.github.io` and Go module `github.com/Gravqc/Nats-Cluster-Operator`. Existing prototype resources require a separate migration. The first public release will distribute a pinned image and a matching Helm chart.

See [scope](docs/scope.md), [usage](docs/usage.md), [design](docs/design.md), [operations](docs/operations.md), [migration](docs/migration.md), [contributing](CONTRIBUTING.md) and [release checklist](docs/releasing.md). This project is independent and makes no upstream endorsement claim.

## Help and contributions

Use [GitHub Issues](https://github.com/Gravqc/Nats-Cluster-Operator/issues) for
bugs and feature ideas. Include the operator and NATS versions and a minimal
reproducer with credentials removed. See [Contributing](CONTRIBUTING.md) and the
[security reporting policy](SECURITY.md).

Licensed under [Apache 2.0](LICENSE).
