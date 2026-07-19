# Exact-response cache fingerprints

The `golang/cache` package owns the v1 semantic cache identity. `cache.Input`
is canonicalized after request validation and HMACed with a deployment-scoped
secret. Its `RouteIdentity` requires provider, endpoint, resolved model and
revision, and compiler profile; account and region are included even when
empty. This keeps OpenAI, Azure/OpenRouter, account, region, revision,
quantization, and compiler changes isolated until a future ADR proves a safe
equivalence.

Operation keys, service class/fallback consent, actor and observability tags,
continuation handles, and cache age are per-call controls and do not change a
fingerprint. Tenant/project namespace, semantic request fields, immutable
conversation/provider-state digests, route identity, capability lowering,
configuration digest, cache epoch, operation domain, and variant do. Compact
is domain-separated from Generate and accepts only variant zero.

The package returns only a fixed-size HMAC digest for lookup. Callers may retain
the canonical manifest for audit, but large transcript content should be
represented by immutable digest/blob references rather than copied into the
cache row. Changing canonicalization, semantic profiles, compiler lowering, or
route identity requires a cache epoch bump.
