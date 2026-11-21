# Security

This unreleased prototype is intended for local evaluation on trusted networks. It uses token-based client authentication, namespace-scoped controller permissions, a non-root workload and restricted monitoring ingress. Client TLS and route TLS are future work. NetworkPolicy requires an enforcing CNI.

The controller can read Secrets and create StatefulSets in its managed namespace. Only trusted users should be able to create or modify its custom resources. Monitoring payloads contain operational information. The operator never publishes credential values in status or logs.

Before publishing, enable a private vulnerability reporting channel on the public repository and record the supported versions here. Until that channel is configured, this repository provides no private vulnerability intake. Avoid filing sensitive credentials or exploit details in public issues.
