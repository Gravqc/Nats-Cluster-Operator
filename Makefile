IMG ?= nats-cluster-operator:0.1.0
KIND ?= $(if $(wildcard $(CURDIR)/bin/kind),$(CURDIR)/bin/kind,kind)
TOOL_PLATFORM := $(shell uname -s)-$(shell uname -m)
CONTROLLER_GEN := $(CURDIR)/bin/$(TOOL_PLATFORM)/controller-gen

.PHONY: generate manifests test build image local-up local-down
$(CONTROLLER_GEN):
	GOBIN=$(dir $(CONTROLLER_GEN)) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.18.0

generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths="./api/..."

manifests: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
	cp config/crd/bases/messaging.nats.example_natsclusters.yaml charts/nats-cluster-operator/crds/natsclusters.yaml

test:
	go test -race ./...
	go vet ./...

default: test

build:
	go build -o bin/operator ./cmd

image:
	docker build -t $(IMG) .

local-up:
	KIND=$(KIND) ./scripts/local-up.sh

local-down:
	$(KIND) delete cluster --name nats-operator-dev

.PHONY: evaluate
evaluate:
	python3 tests/e2e.py

.PHONY: local-status local-logs
local-status:
	KUBECONFIG=$(CURDIR)/.state/kubeconfig kubectl --context kind-nats-operator-dev -n nats-demo get natsclusters,pods,pvc

docker-status:
	docker ps --filter label=io.x-k8s.kind.cluster=nats-operator-dev

local-logs:
	KUBECONFIG=$(CURDIR)/.state/kubeconfig kubectl --context kind-nats-operator-dev -n nats-demo logs deployment/nats-operator --tail=100
