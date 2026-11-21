#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
KIND=${KIND:-kind}
mkdir -p .state
export KUBECONFIG="$PWD/.state/kubeconfig"
if ! "$KIND" get clusters | grep -qx nats-operator-dev; then
  "$KIND" create cluster --name nats-operator-dev --image kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0 --config config/kind.yaml --kubeconfig "$KUBECONFIG" --wait 180s
else
  containers=$(docker ps -aq --filter label=io.x-k8s.kind.cluster=nats-operator-dev)
  if [[ -n "$containers" ]]; then
    docker start $containers >/dev/null
  fi
  "$KIND" export kubeconfig --name nats-operator-dev --kubeconfig "$KUBECONFIG"
  api_ready=false
  for attempt in {1..60}; do
    if kubectl --context kind-nats-operator-dev --request-timeout=2s get --raw=/readyz >/dev/null 2>&1; then
      api_ready=true
      break
    fi
    sleep 2
  done
  if [[ "$api_ready" != true ]]; then
    printf 'Kubernetes API did not become ready after resuming the lab.\n' >&2
    exit 1
  fi
  kubectl --context kind-nats-operator-dev wait nodes --all --for=condition=Ready --timeout=180s
fi
# Every subsequent command is explicitly scoped to the dedicated local cluster.
docker build -t nats-cluster-operator:0.1.0 .
"$KIND" load docker-image nats-cluster-operator:0.1.0 --name nats-operator-dev
kubectl --context kind-nats-operator-dev create namespace nats-demo --dry-run=client -o yaml | kubectl --context kind-nats-operator-dev apply -f -
# Helm does not upgrade CRDs from its crds/ directory. Keep the disposable
# local API in sync before applying examples that use newly added fields.
kubectl --context kind-nats-operator-dev apply -f config/crd/bases/messaging.nats.example_natsclusters.yaml
restart_controller=false
if kubectl --context kind-nats-operator-dev -n nats-demo get deployment/nats-operator >/dev/null 2>&1; then
  restart_controller=true
fi
helm upgrade --install nats-operator ./charts/nats-cluster-operator --kube-context kind-nats-operator-dev -n nats-demo --wait --timeout 180s
# The local image tag is intentionally stable. Loading a rebuilt image into
# Kind does not change an existing Deployment's pod template or running binary.
if [[ "$restart_controller" == true ]]; then
  kubectl --context kind-nats-operator-dev -n nats-demo rollout restart deployment/nats-operator
fi
if ! kubectl --context kind-nats-operator-dev -n nats-demo get secret nats-auth >/dev/null 2>&1; then
  token=$(openssl rand -hex 24)
  kubectl --context kind-nats-operator-dev -n nats-demo create secret generic nats-auth --from-literal="token=$token"
fi
kubectl --context kind-nats-operator-dev -n nats-demo apply -f examples/local.yaml
kubectl --context kind-nats-operator-dev -n nats-demo rollout status deployment/nats-operator --timeout=180s
kubectl --context kind-nats-operator-dev -n nats-demo wait pods --selector=app.kubernetes.io/instance=local --for=condition=Ready --timeout=240s
kubectl --context kind-nats-operator-dev -n nats-demo wait natscluster/local --for=condition=Ready --timeout=240s
kubectl --context kind-nats-operator-dev -n nats-demo get natsclusters,pods,pvc
printf 'Environment ready. Run KUBECONFIG=%s kubectl -n nats-demo get natsclusters\n' "$KUBECONFIG"
