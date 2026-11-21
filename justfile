# Local lab commands. Run `just` to list them.

# Show the available commands.
default:
    @just --list

# Create the Kind environment and install the operator and local NATS example.
up:
    make local-up

# Show NATS clusters, their pods, and their persistent disks.
status:
    make local-status

# Show recent operator logs.
logs:
    make local-logs

# Show the Docker containers hosting this Kubernetes environment.
nodes:
    make docker-status

# Run the end-to-end evaluation against a fresh lab.
evaluate:
    make evaluate

# Delete the dedicated Kind cluster and its local test data.
down:
    make local-down

# Stop this lab's node containers while preserving its data.
stop:
    #!/usr/bin/env bash
    set -euo pipefail
    containers=$(docker ps -q --filter label=io.x-k8s.kind.cluster=nats-operator-dev)
    if [[ -n "$containers" ]]; then
        docker stop $containers
    fi
