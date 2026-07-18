#!/usr/bin/env bash
set -euo pipefail

# Keep the package's immutable SDK pin and both CI install paths aligned. The
# hash is intentionally repeated in package metadata, documentation, and
# workflows so a reviewer can see exactly which upstream SDK commit is used;
# this check prevents one copy from silently drifting.
files=(
  ocaml/llm_temporal_worker/llm-temporal-ocaml.opam
  ocaml/llm_temporal_worker/README.md
  .github/workflows/master.yml
  .github/workflows/pull-request.yml
)

pins="$(
  grep -hEo 'ocaml-temporal\.git#[0-9a-f]{40}' "${files[@]}" \
    | sed 's/.*#//' \
    | sort -u
)"
pin_count="$(printf '%s\n' "$pins" | awk 'NF {count++} END {print count + 0}')"

if [[ "$pin_count" != 1 ]]; then
  printf 'Expected one OCaml Temporal commit across package metadata, docs, and CI; found %s.\n' "$pin_count" >&2
  printf '%s\n' "$pins" >&2
  exit 1
fi

printf 'OCaml Temporal dependency pin: %s\n' "$pins"
