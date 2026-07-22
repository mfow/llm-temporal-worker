# Provider registry implementer report

## Scope

This change completes the bounded provider-adapter registration and fixture
completeness slice from Task 8 of the provider-adapter plan. It adds an
explicit `(family, profile_id)` registry, private adapter-owned SDK parameter
checks, compile probes, one-shot client-construction validation, and defensive
capability/service-class validation. It also checks the reviewed
`required-cases.yaml` inventory against the code-owned case registry and
requires every checked-in fixture profile to be `enforced`.

No provider SDK type or client is retained by the registry, no network request
is made by validation, and no historical streaming requirement is reintroduced
into the v1 boundary.

## Validation

- `go test ./llm/provider/... -count=1`
- `go test -race ./llm/provider/... -count=1`
- `make adapter-contracts`

All passed on the branch. The adapter-contracts gate validated all eight
checked-in profiles, the redaction scan, the complete case inventory, and the
registry unit tests.

## Self-review

- Duplicate family/profile keys fail without replacement.
- Capability maps and service-tier maps are copied on registration and lookup.
- Typed-nil adapters, missing capabilities, streaming claims, incomplete tier
  mappings, compile-probe mismatches, and failed client construction fail
  closed.
- Factory errors are intentionally sanitized so credentials or endpoint details
  cannot enter startup diagnostics.
- The registry is immutable-by-convention after startup; callers must complete
  registration before sharing it across goroutines.

## Remaining concern

Concrete runtime route construction still owns how configured profiles are
turned into registrations. This PR supplies the validated boundary and does
not invent provider-specific constructors or credentials for that composition
layer.
