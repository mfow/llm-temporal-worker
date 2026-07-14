#!/usr/bin/env bash

# The mutation tool uses Go overlays, so it never writes mutant source into
# the checkout. Its private build cache is removed on every exit path.
set -euo pipefail

GO="${GO:-go}"
created_cache=""
if [[ -z "${GOCACHE:-}" ]]; then
  created_cache="$(mktemp -d "${TMPDIR:-/tmp}/llmtw-task18-mutation-gocache.XXXXXX")"
  export GOCACHE="$created_cache"
fi
cleanup() {
  if [[ -n "$created_cache" ]]; then
    rm -rf "$created_cache"
  fi
}
trap cleanup EXIT HUP INT TERM

"$GO" run ./tools/mutationverify -root . -go "$GO"
