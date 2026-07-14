# Release evidence runbook

Task 23 produces a local, machine-readable release-evidence bundle. It is a
nonpublishing validation and retention step: the workflow never signs an
image, sends an image to a registry, creates a release, obtains provider
credentials, or calls a live LLM provider. Publication controls remain a
separate task.

## Trusted boundary

The `release-evidence` job runs only after `verify` on a `push` to `master`.
Pull-request, scheduled, and manual workflow executions do not collect or
retain this bundle. The job has only `contents: read` permission.

The job builds one Linux OCI archive at `$RUNNER_TEMP/image.oci.tar`, outside
`release-artifacts/`, runtime-tests the image loaded from that archive, then
obtains the immutable image subject from the archive's single OCI manifest
descriptor:

```sh
digest="$(go run ./tools/releaseverify layout-digest -layout "$RUNNER_TEMP/image.oci.tar")"
reference="llm-temporal-worker@$digest"
```

The descriptor, rather than a local image ID or mutable tag, is the source of
truth. The retained CycloneDX SBOM and Trivy JSON scan are both bound to the
same `reference` and `digest`; the verifier rejects stale or mismatched
subjects. The raw archive is CI-temporary evidence only: it is used for the
descriptor, Syft, and Trivy, never recorded or uploaded, and is removed after
the artifact-upload step. Trusted CI uses explicit Syft `v1.44.0` and Trivy
`v0.72.0` inputs, with the checked-in
[Trivy configuration](../../scripts/release/trivy.yaml) applied to that
temporary archive.

Before compact manifest collection, trusted CI installs `kubectl v1.32.6` with
the immutable `azure/setup-kubectl` `v4.0.1` action commit. The collector
checks that exact client version before using `kubectl kustomize`; it has no
cluster configuration and only renders the checked-in local manifests.

## Bundle contents

The record follows [the evidence schema](evidence.schema.json). It binds every
retained artifact to a byte count and SHA-256 digest, and requires a redaction
assertion for:

- compact test, race, and deterministic fuzz summaries;
- fixture manifest records with upstream source dates;
- compact Redis, Temporal, and Compose health summaries from the local test
  stack;
- three redacted boundary-log records derived from actual `docker compose logs`
  output for Redis, Temporal, and the combined stack;
- rendered Kubernetes manifest digests and object counts;
- dependency/license inventory and the existing redacted vulnerability result;
- the CycloneDX SBOM and matching Trivy scan, each bound to the immutable
  descriptor subject.

The bundle has a closed, root-level filename set: every artifact uses the
canonical name shown below and the record is always `evidence.json`. Renamed,
nested, unreferenced, or symlinked files are rejected. A local caller may point
the verifier at a different artifact directory, but not at a different record
filename within that directory.

Repository, fixture, and dependency provenance URLs must use an absolute HTTPS
URL with a DNS hostname and no explicit port, userinfo, backslashes, query
strings, or fragments. This keeps credentials and mutable URL decorations out
of retained release evidence.

Command output, scanner diagnostics, the raw OCI archive, credentials, request
content, and provider payloads stay in private runner temporary storage. Raw
service output also stays there: the three retained log records contain only
fixed allowlisted event counts (`allowlist-v1`), plus the observed line and
byte counts. This preserves proof that Redis and Temporal each emitted a safe
runtime-boundary event, including both families in the combined Compose record,
without retaining arbitrary message text. The verifier scans every retained
artifact for secret-like values and rejects unsafe paths, symlinks, residual
raw OCI archives, unreferenced files, digest or byte-count changes, incomplete
fixture records, mismatched image subjects, and HIGH or CRITICAL image findings.

## Collect, record, and verify

The trusted job first produces the compact summaries and a temporary OCI
archive outside the retained directory:

```sh
oci_archive="$RUNNER_TEMP/image.oci.tar"
bash scripts/release/collect.sh \
  --artifact-dir release-artifacts \
  --image-oci-layout "$oci_archive"
```

The collector rejects an archive path inside `release-artifacts/`. It then
generates the SBOM and scan from the temporary archive, binds both to the
descriptor subject above, and records the completed bundle. A local caller
must supply a temporary archive path outside its evidence directory and the
two final JSON files before it can record the exact same bundle:

```sh
digest="$(go run ./tools/releaseverify layout-digest -layout "$oci_archive")"
reference="llm-temporal-worker@$digest"

bash scripts/release/record.sh \
  -artifact-dir release-artifacts \
  -output release-artifacts/evidence.json \
  -repository https://github.com/mfow/llm-temporal-worker \
  -revision "$(git rev-parse HEAD)" \
  -image-reference "$reference" \
  -image-digest "$digest" \
  -artifact test_summary=test-summary.json \
  -artifact race_summary=race-summary.json \
  -artifact fuzz_summary=fuzz-summary.json \
  -artifact fixture_manifest=fixture-manifest.json \
  -artifact redis_summary=redis-summary.json \
  -artifact temporal_summary=temporal-summary.json \
  -artifact compose_summary=compose-summary.json \
  -artifact redis_log=redis-log.json \
  -artifact temporal_log=temporal-log.json \
  -artifact compose_log=compose-log.json \
  -artifact rendered_manifests=rendered-manifests.json \
  -artifact dependency_license=dependencies.json \
  -artifact vulnerability_results=vulnerabilities.json \
  -artifact sbom=sbom.cdx.json \
  -artifact image_scan=image-scan.json
```

The recorder validates the entire bundle before atomically writing
`evidence.json`; a failed validation leaves no final evidence record. Validate
an already recorded local bundle with:

```sh
make release-verify
```

To validate another local bundle, invoke the verifier directly (the Make
target deliberately seals the trusted path to `release-artifacts/evidence.json`):

```sh
bash scripts/release/verify.sh \
  --artifact-dir /path/to/release-artifacts \
  --evidence /path/to/release-artifacts/evidence.json
```

After successful verification the master-push job retains the redacted bundle
for 14 days and removes `$RUNNER_TEMP/image.oci.tar`. `release-artifacts/` is
ignored by Git and excluded from the Docker build context.
