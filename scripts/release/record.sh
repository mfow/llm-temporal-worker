#!/usr/bin/env bash

set -euo pipefail

root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"

exec go run "$root/tools/releaseverify" record \
  -schema "$root/docs/release/evidence.schema.json" \
  "$@"
