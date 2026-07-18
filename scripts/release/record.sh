#!/usr/bin/env bash

set -euo pipefail

root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
module_root="$root/golang"

arguments=("$@")
for ((index = 0; index < ${#arguments[@]} - 1; index++)); do
  case "${arguments[index]}" in
    -artifact-dir|-output)
      if [[ "${arguments[index + 1]}" != /* ]]; then
        arguments[index + 1]="$root/${arguments[index + 1]}"
      fi
      ;;
    -artifact)
      artifact_name="${arguments[index + 1]%%=*}"
      artifact_path="${arguments[index + 1]#*=}"
      if [[ "$artifact_path" != /* ]]; then
        arguments[index + 1]="$artifact_name=$root/$artifact_path"
      fi
      ;;
  esac
done
cd "$module_root"
exec go run ./tools/releaseverify record \
  -schema "$root/docs/release/evidence.schema.json" \
  "${arguments[@]}"
