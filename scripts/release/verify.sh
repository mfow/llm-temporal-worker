#!/usr/bin/env bash

set -euo pipefail

root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
module_root="$root/golang"

arguments=("$@")
for ((index = 0; index < ${#arguments[@]} - 1; index++)); do
  case "${arguments[index]}" in
    --artifact-dir|--evidence)
      if [[ "${arguments[index + 1]}" != /* ]]; then
        arguments[index + 1]="$root/${arguments[index + 1]}"
      fi
      ;;
  esac
done
exec go -C "$module_root" run ./tools/releaseverify verify \
  -schema "$root/docs/release/evidence.schema.json" \
  "${arguments[@]}"
