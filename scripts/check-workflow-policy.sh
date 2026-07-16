#!/usr/bin/env bash
set -euo pipefail

# Pin the linter release so syntax validation stays reproducible while the
# workflow contract test below governs this repository's exact policy.
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
go test ./internal/architecturetest -run '^(TestWorkflow.*|TestLiveProviderContractsWorkflowIsManualProtectedAndSingleProfile)$'
