#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
kubectl_bin=${KUBECTL:-kubectl}

fail() {
  printf 'deploy verification failed: %s\n' "$1" >&2
  exit 1
}

grep -Fq '@sha256:' "$root/Dockerfile" || fail 'Dockerfile builder/runtime images must be digest pinned'
grep -Fq '@sha256:' "$root/deploy/kubernetes/base/deployment.yaml" || fail 'Kubernetes worker image must be digest pinned'
grep -Fq 'CGO_ENABLED=0' "$root/Dockerfile" || fail 'worker image must be statically built'
grep -Fq 'USER 65532:65532' "$root/Dockerfile" || fail 'worker image must use uid 65532'
grep -Fq 'read_only: true' "$root/compose.yaml" || fail 'Compose worker must use a read-only root filesystem'
grep -Fq 'cap_drop: [ALL]' "$root/compose.yaml" || fail 'Compose worker must drop all capabilities'

command -v "$kubectl_bin" >/dev/null 2>&1 || fail "kubectl executable '$kubectl_bin' is required to render Kustomize assets"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

for overlay in \
  "$root/deploy/kubernetes/base" \
  "$root/deploy/kubernetes/examples/redis-tls" \
  "$root/deploy/kubernetes/examples/aws-workload-identity" \
  "$root/deploy/kubernetes/examples/azure-workload-identity"; do
  name=$(basename "$overlay")
  rendered="$tmp/$name.yaml"
  "$kubectl_bin" kustomize "$overlay" >"$rendered"
  grep -Fq 'runAsNonRoot: true' "$rendered" || fail "$name does not enforce non-root execution"
  grep -Fq 'fsGroup: 65532' "$rendered" || fail "$name does not grant the worker group access to mounted secrets"
  grep -Fq 'readOnlyRootFilesystem: true' "$rendered" || fail "$name does not enforce read-only root"
  grep -Fq 'type: RuntimeDefault' "$rendered" || fail "$name does not set the default seccomp profile"
  grep -Fq 'path: /health/live' "$rendered" || fail "$name is missing liveness probe"
  grep -Fq 'path: /health/ready' "$rendered" || fail "$name is missing readiness probe"
  grep -Fq 'terminationGracePeriodSeconds: 90' "$rendered" || fail "$name has an unsafe termination grace"
  grep -Fq 'service_classes: [economy, standard, priority]' "$rendered" || fail "$name does not declare all public service classes"
  if grep -Eq '(^|[[:space:]])(latest|password|api[_-]?key):[[:space:]]+[^"[:space:]]' "$rendered"; then
    fail "$name appears to contain an unreviewed mutable tag or credential value"
  fi
done

printf 'deployment assets render and satisfy static security checks\n'
