# First public release

1. The public API is `nats.gravqc.github.io/v1alpha1` and the Go module is `github.com/Gravqc/Nats-Cluster-Operator`. Follow the [prototype migration notes](migration.md#moving-from-the-prototype-api-group) when updating an existing lab.
2. Configure maintainers, private security reporting, contributor conduct/reporting expectations and project governance. Review the Apache-2.0 license and all bundled dependencies. Keep personal credentials and local evaluation data out of Git.
3. Run generation, race tests, Helm lint and a clean Kind evaluation in CI. Review the measured interruption and message-integrity results. Verify PodMonitor against a real Prometheus Operator and review NetworkPolicy with an enforcing CNI.
4. Build images for linux/amd64 and linux/arm64 from a version tag, scan dependencies/images, publish an SBOM, sign images and record immutable digests. Package and publish a versioned install chart referencing the published image. Keep generated schema and chart CRDs byte-identical.
5. Publish compatibility, retained-storage behavior and known limits alongside the release. Mark the API alpha. Ask upstream maintainers for feedback using the reproducible local comparison and explicit use case.

These are publication tasks. The local repository and CI definitions do not imply a public release, hosted registry or upstream endorsement.

## Alpha distribution

Use Git tag `v0.1.0-alpha.1`, container image
`ghcr.io/gravqc/nats-cluster-operator:0.1.0-alpha.1` and Helm chart version
`0.1.0-alpha.1`. The chart defaults to the matching image. Attach the chart
archive to the GitHub pre-release. Test the image pull without credentials and
install the packaged chart on a fresh Kind cluster before announcing the
release. The public package and release are separate from pushing source code.

After the assets are public, repeat the user path from a clean environment:

```sh
helm upgrade --install nats-operator \
  https://github.com/Gravqc/Nats-Cluster-Operator/releases/download/v0.1.0-alpha.1/nats-cluster-operator-0.1.0-alpha.1.tgz \
  --namespace messaging --create-namespace --wait
```

Then use the [cluster creation and verification steps](../README.md#create-and-verify-a-cluster) with the example manifest from the same tag. Verify the chart's default image was pulled from GHCR. Publish the install link in the README after both downloads and the clean install have been verified.
