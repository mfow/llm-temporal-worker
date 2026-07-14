# Deployment assets

The worker image is built by the repository [Dockerfile](../Dockerfile) as a
static, non-root binary on a digest-pinned Go builder and a digest-pinned
Distroless runtime. It has no shell, writes no files by default, and expects an
orchestrator-provided `/tmp` volume when the root filesystem is read-only. The
Kubernetes base deliberately contains a `REPLACE_WITH_RELEASE_DIGEST` marker;
release automation must substitute the signed digest before applying it.

Kubernetes manifests live under `kubernetes/base` and include:

- two replicas, rolling-update safety, a disruption budget, resource bounds,
  dropped capabilities, RuntimeDefault seccomp, and read-only root storage;
- fail-closed `/health/live` and `/health/ready` probes plus `/metrics`;
- ConfigMap-mounted non-secret configuration/catalogs and externally provisioned
  Secret volumes for Redis/TLS/continuation material;
- an ingress/egress NetworkPolicy that permits only probe/metrics traffic and
  DNS, Redis, Temporal, and TLS egress;
- AWS and Azure workload-identity examples that opt into service-account token
  mounting only in the selected overlay.

Render and check every manifest offline with:

```sh
./deploy/verify.sh
```

The script uses the local `kubectl kustomize` implementation and never contacts
a cluster. Replace the example image/config/catalog/identity values in a
reviewed overlay before production use; no credentials belong in this tree.
