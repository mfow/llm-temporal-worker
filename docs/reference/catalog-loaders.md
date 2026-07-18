# Catalog loader contract

`golang/internal/catalog` is the only package that turns operator-supplied capability
and price YAML into runtime snapshots. It is deliberately separate from the
main configuration decoder so a reload can authenticate and compile every
referenced file before publishing one new snapshot.

## File integrity and resource limits

Each `config.CatalogRef` supplies an absolute `file` path and a lowercase or
uppercase SHA-256 digest. The loader:

1. rejects relative paths and malformed digests;
2. checks the file size before reading and reads at most 4 MiB (a lower bound
   may be selected with `Load*WithOptions`);
3. computes SHA-256 over the exact bytes on disk and compares it in constant
   time; and
4. decodes only after the digest matches.

An empty document, a second YAML document, duplicate mapping key, unknown
field, malformed scalar, or duplicate catalog identity is a hard error. A
failed load never returns a partial catalog.

## Capability documents

The production shape is a versioned `entries` list:

```yaml
version: llmtw-capabilities/v1
entries:
  - id: openai-prod
    family: openai_responses
    model: {exact: gpt-example}
    verified_at: 2026-07-13T00:00:00Z
    features:
      input.text: {level: native}
      tools.auto: {level: native}
      output.json_schema: {level: emulated, transform: json-schema-tool}
    limits:
      context_tokens: 400000
      output_tokens: 32768
```

The local fixture's `profiles` map is also supported. Its `input`, `output`,
and `service_classes` lists are validated against the same closed vocabulary.
The loader converts claims to `provider.CapabilitySet`; `reference` is
validated as a known catalog claim but is not emitted because the provider
port has no external-reference feature. An `emulated` claim must name a
transform. Family aliases used by config (`azure_openai_responses` and
`bedrock_anthropic_messages`) are normalized to their provider family.

## Price documents

Price documents contain a version, immutable `id`, currency, and entries. The
canonical entry identity is provider, endpoint ID, endpoint family, region,
model, provider tier, and effective start time. Prices are quoted decimal
strings and are compiled by `pricing.CompileCatalog`; floating-point values are
not accepted. The local fixture's `endpoint` and `service_class` aliases are
accepted, but `service_class` is still exactly one of `economy`, `standard`, or
`priority`. There is no `provider_default` class.

```yaml
version: llmtw-prices/v1
id: catalog-2026-07-13
currency: USD
entries:
  - provider: openai
    endpoint_id: openai-production
    endpoint_family: openai_responses
    region: global
    model: gpt-example
    provider_tier: standard
    input_per_million: "1.250000"
    output_per_million: "10.000000"
    source: operator-verified
```

`Load` resolves every capability and pricing reference in `config.Config` and
fails if an endpoint names a missing profile/catalog, if its family does not
match the profile, or if its price catalog has no entry for that endpoint and
family. It also rejects duplicate profile IDs and duplicate price-catalog IDs
across files. Model-specific price selection remains a routing concern.
