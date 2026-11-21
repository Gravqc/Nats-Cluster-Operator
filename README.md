# NATS Cluster Operator

An experimental Kubernetes operator for provisioning independent NATS clusters from a small `NatsCluster` resource, with persistent JetStream storage and guarded rolling upgrades.

Built with Kubebuilder v4.6.0, Go and controller-runtime. Install the controller once per managed namespace, then apply one resource for each cluster. Application code owns streams and consumers. Each cluster has its own StatefulSet, client Service, headless Service, ConfigMap, PVCs, PDB and NetworkPolicy. An optional per-cluster PodMonitor integrates with an existing Prometheus Operator.

## Why this project?

The goal is to let an engineer request an independent NATS cluster with an explicit server version and a small configuration, then inspect its readiness and perform a guarded rollout. Helm already provides server deployment. This project evaluates whether a Kubernetes API plus monitoring-aware maintenance makes repeated cluster operation easier. See [scope and acceptance criteria](docs/scope.md) for the current boundaries and [design](docs/design.md) for the reasoning.

## Current scope

- Explicit NATS server versions, with no floating `latest` tag.
- One server for local development, or three servers spread across workers.
- File-backed JetStream, resource requests/limits and scheduling configuration.
- Optional per-broker [Go memory and GC controls](docs/memory.md).
- A token Secret supplied by the user. Same-namespace client access by default.
- Status from `/healthz`, `/varz` and `/jsz`, including versions, connections and metadata leader.
- Forward patch or adjacent-minor upgrades. Before each deliberate restart, all observed replicas must be current and every pod Ready. Metadata leader is considered last. The controller waits for ten seconds of observed stability between restarts.
- CR deletion retains workloads and disks. Recreating the CR does not automatically adopt retained resources.

This is a local prototype. Read [evaluation](docs/evaluation.md) for executed results and the limits of those results. Scaling, volume expansion, credential rotation, client rebalancing and automatic adoption remain unimplemented operations discussed in [operations](docs/operations.md).

## One local environment

Prerequisites are Docker, Kind v0.33.0, Helm 3, kubectl, Python 3, OpenSSL, Just and Make. The local flow builds Go inside Docker, so a host Go installation is optional. Kind creates one named environment, `nats-operator-dev`, using four node containers. The controller and NATS pods run inside that environment. Docker's container view shows the four Kind nodes; these are separate from Compose project groups. Run the commands below from this repository. `just` is a task runner that provides short names for the lab commands. The local recipes delegate to the Makefile used by the Go development tooling. `just status` shows the NATS pods; `just nodes` shows the Docker containers that host Kubernetes.

```sh
just up
export KUBECONFIG="$PWD/.state/kubeconfig"
kubectl --context kind-nats-operator-dev -n nats-demo get natsclusters,pods,pvc
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

## Install on your own cluster

Build and push your own operator image, then set its repository and tag explicitly. The following commands assume a reachable image has already been published. The Helm chart lives in this repository; no external chart repository is required for this install.

```sh
helm upgrade --install nats-operator ./charts/nats-cluster-operator \
  --namespace messaging --create-namespace \
  --set image.repository=YOUR_REGISTRY/nats-cluster-operator \
  --set image.tag=0.1.0
export NATS_TOKEN="$(openssl rand -hex 24)"
kubectl -n messaging create secret generic nats-auth --from-literal=token="$NATS_TOKEN"
kubectl -n messaging apply -f examples/ha.yaml
kubectl -n messaging wait natscluster/platform --for=condition=Ready --timeout=240s
```

The controller watches its installation namespace and uses namespace-scoped RBAC. Use one installation per namespace. Different clusters in one namespace use distinct names and can share workers. PodMonitor requires `monitoring.coreos.com/v1` to be installed first; set `spec.podMonitor: true` when initially creating the cluster, and configure Prometheus to select it.

CRDs under Helm's `crds/` directory install on first install and are retained on uninstall. Apply the versioned generated CRD explicitly before upgrading a controller release that changes its API. Deleting the namespace or CRD is destructive and bypasses the normal retained-workload workflow. See [migration](docs/migration.md).

## Development

With Go 1.24 and Make installed:

```sh
make generate manifests test
make image
```

`make generate manifests` uses controller-gen v0.18.0. Generated API deepcopy code and the CRD are checked in. `PROJECT` records the Kubebuilder scaffold. The runtime image is built from scratch and runs without root privileges. CI is configured for formatting, generation drift, unit/race checks, Helm lint and the Kind evaluation. Hosted CI execution is pending publication.

## Adoption and publication

The API group `messaging.nats.example` and Go module `example.com/nats-cluster-operator` are provisional, reserved example names. Before the first public release, choose the repository/module path and a stable API group, regenerate files, publish signed versioned images and chart packages, and configure private vulnerability reporting. Changing the API group after adoption requires migration.

See [scope](docs/scope.md), [usage](docs/usage.md), [design](docs/design.md), [operations](docs/operations.md), [migration](docs/migration.md), [contributing](CONTRIBUTING.md) and [release checklist](docs/releasing.md). This project is independent and makes no upstream endorsement claim.
