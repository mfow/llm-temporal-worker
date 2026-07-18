#!/usr/bin/env bash

set -euo pipefail

root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
module_root="$root/golang"

cd "$module_root"
exec go run ./tools/releaseverify record \
  -schema "$root/docs/release/evidence.schema.json" \
  "$@"
