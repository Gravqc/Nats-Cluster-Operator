# First public release

1. Choose the public repository, stable API group and Go module path. Update PROJECT, imports, generated API/CRDs, RBAC, samples and docs together.
2. Configure maintainers, private security reporting, contributor conduct/reporting expectations and project governance. Review the Apache-2.0 license and all bundled dependencies. Keep personal credentials and local evaluation data out of Git.
3. Run generation, race tests, Helm lint and a clean Kind evaluation in CI. Review the measured interruption and message-integrity results. Verify PodMonitor against a real Prometheus Operator and review NetworkPolicy with an enforcing CNI.
4. Build images for linux/amd64 and linux/arm64 from a version tag, scan dependencies/images, publish an SBOM, sign images and record immutable digests. Package and publish a versioned install chart referencing the published image. Keep generated schema and chart CRDs byte-identical.
5. Publish compatibility, retained-storage behavior and known limits alongside the release. Mark the API alpha. Ask upstream maintainers for feedback using the reproducible local comparison and explicit use case.

These are publication tasks. The local repository and CI definitions do not imply a public release, hosted registry or upstream endorsement.
