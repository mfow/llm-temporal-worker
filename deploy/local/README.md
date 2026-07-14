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
