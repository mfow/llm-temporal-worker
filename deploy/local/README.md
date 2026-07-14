# Local Compose fixture

The default Compose profile starts pinned Temporal, Redis, and a deterministic
OpenAI-compatible provider fixture. No provider credential is used. The worker
profile is deliberately opt-in because it needs a locally generated
continuation HMAC key:

```sh
mkdir -p .local
openssl rand -hex 32 > .local/continuation-hmac
docker compose up -d temporal redis provider-mock
docker compose --profile worker up worker
```

The fixture is for development only. It uses a local-only bearer value and a
zero-cost catalog; production deployments must use external secret delivery,
real catalog digests, and the release image digest. The worker exposes the
same probes as Kubernetes: `/health/live`, `/health/ready`, and `/metrics`.

The checked-in worker profile is a parser/configuration/readiness fixture, not
an end-to-end provider invocation path. Provider egress is deliberately
fail-closed in every environment, so provider egress is not available to the
private Docker-network address resolved for `provider-mock`. Use the adapter
and runtime tests for deterministic mock traffic. To make real provider
requests from a local worker, use a private local configuration with a reviewed
public HTTPS provider hostname and its matching `outbound_hosts` entry; do not
weaken the policy for a Docker-private destination.
