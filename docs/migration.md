# Evaluation and migration from a Helm deployment

Begin with a separate operator-managed cluster. Existing Helm resources are never automatically adopted by name. That rule avoids competing owners for pods, disks and configuration.

1. Inventory endpoints, auth/TLS, max payload, disk size/class/zones, stream and consumer settings, replica counts, retained messages, queue backlog, application lifecycle and monitoring. Confirm v0.1's token-only trusted-network evaluation scope meets the environment.
2. Install the operator into the new namespace. Supply a new credential Secret and apply a NatsCluster with an explicit server version. Check its readiness and server status. Let application initialization provision its stream policy on the new endpoint.
3. Choose each flow's data transition. Drain work queues, transfer required history using a separately tested snapshot/restore or source/mirror strategy, and coordinate publishers with consumers. Changing the endpoint alone does not copy retained data or durable consumer state.
4. Test application traffic and failures, then move a complete flow. Keep old data and record exactly which messages were processed on each side. Define rollback against both endpoints and queue contents; reversing DNS alone can strand or duplicate work.
5. Retire the old infrastructure after the chosen drain/retention condition and restore verification. Preserve data according to the storage policy.

An in-place ownership handoff is future work. It must preserve server identity, PVC bindings, config paths and immutable Service/StatefulSet fields, pause the previous GitOps owner, transfer owner references, and survive interruption with rollback. Deleting a StatefulSet with orphan propagation is only one step of that migration. It is not sufficient evidence that a running deployment can be adopted safely.

To delete a NatsCluster while keeping its running workload and endpoint, use
`kubectl -n YOUR_NAMESPACE delete natsclusters.nats.gravqc.github.io YOUR_CLUSTER --cascade=orphan`.
Keep the controller running until finalization completes. The StatefulSet,
Services, ConfigMap, PDB, NetworkPolicy, optional PodMonitor and PVCs remain
for manual management. Foreground cascading deletion can remove resources
before the controller detaches them, including the client and headless
Services. Recreating the CR with the same name does not adopt its resources.
To remove a disposable evaluation and its local storage, use `make local-down`.

## Moving from the prototype API group

The alpha uses `nats.gravqc.github.io/v1alpha1`. The earlier
`messaging.nats.example/v1alpha1` resources belong to a different Kubernetes API.
Their owned Services and StatefulSets cannot be adopted by an alpha resource.
For a disposable lab, start a clean lab after you have explicitly decided to
discard the old one. To keep data, use a separate namespace or cluster and
follow the migration steps above. Keep the prototype controller available for
any prototype resources that still need reconciliation or finalization.
