#!/usr/bin/env bash

# Check formatting without changing the checkout. Bash arrays preserve each
# NUL-delimited filename as one argument, including names containing spaces or
# newlines.
set -euo pipefail

files=()
while IFS= read -r -d '' file; do
  files+=("$file")
done < <(
  find . \
    -type d \( -name vendor -o -name .worktrees \) -prune -o \
    -type f -name '*.go' -print0
)

if (( ${#files[@]} == 0 )); then
  echo "no Go source files found outside vendor and .worktrees." >&2
  exit 1
fi

if unformatted="$(gofmt -l "${files[@]}")"; then
  if [[ -n "$unformatted" ]]; then
    printf '%s\n' "$unformatted" >&2
    exit 1
  fi
else
  status=$?
  printf '%s\n' "$unformatted" >&2
  echo "gofmt failed while checking Go formatting." >&2
  exit "$status"
fi
