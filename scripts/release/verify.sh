#!/usr/bin/env bash

set -euo pipefail

root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
module_root="$root/golang"

arguments=("$@")
for ((index = 0; index < ${#arguments[@]} - 1; index++)); do
  if [[ "${arguments[index]}" == "--evidence" && "${arguments[index + 1]}" != /* ]]; then
    arguments[index + 1]="$root/${arguments[index + 1]}"
  fi
done
cd "$module_root"
exec go run ./tools/releaseverify verify \
  -schema "$root/docs/release/evidence.schema.json" \
  "${arguments[@]}"
