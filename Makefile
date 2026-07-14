SHELL := /bin/sh

GO ?= go
SECURITY_GO_TOOLCHAIN ?= go1.26.5
COMPOSE ?= docker compose
KUBECTL ?= kubectl
READINESS_REDIS_IMAGE ?= redis:7.4.2-alpine@sha256:02419de7eddf55aa5bcf49efb74e88fa8d931b4d77c07eff8a6b2144472b6952
READINESS_REDIS_CONTAINER_PREFIX ?= llmtw-readiness-integration
READINESS_REDIS_PORT ?= 16379

.PHONY: fmt-check schema-verify docs-verify workflow-verify vet test build integration readiness-integration compose-smoke deployment-policy-verify kustomize-verify adapter-contracts security-verify verify

fmt-check:
	@bash scripts/check-go-format.sh

schema-verify:
	$(GO) test ./llm/schema ./config

docs-verify:
	$(GO) test ./internal/documentationtest -run TestDocumentationLinksAndInvariants

workflow-verify:
	bash scripts/check-workflow-policy.sh

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

build:
	$(GO) build ./...

integration:
	$(GO) test -race ./integration/...

# Runs the readiness recovery gate against exactly one fresh, digest-pinned
# Redis daemon. The test itself explicitly provisions the immutable Function;
# the worker only verifies it and never writes shared Redis code.
readiness-integration:
	@command -v docker >/dev/null 2>&1 || { \
		echo "readiness-integration requires Docker" >&2; \
		exit 2; \
	}
	@docker info >/dev/null 2>&1 || { \
		echo "readiness-integration requires a running Docker daemon" >&2; \
		exit 2; \
	}
	@container="$(READINESS_REDIS_CONTAINER_PREFIX)-$$$$"; \
	cleanup() { docker rm -f "$$container" >/dev/null 2>&1 || true; }; \
	trap cleanup EXIT INT TERM; \
	docker run --detach --name "$$container" --publish 127.0.0.1:$(READINESS_REDIS_PORT):6379 "$(READINESS_REDIS_IMAGE)" redis-server --appendonly yes --save 60 1 --maxmemory-policy noeviction >/dev/null; \
	if ! LLMTW_READINESS_REDIS_ADDR="127.0.0.1:$(READINESS_REDIS_PORT)" LLMTW_READINESS_REDIS_CONTAINER="$$container" $(GO) test -count=1 -tags=readinessintegration ./integration; then \
		docker logs "$$container" >&2 || true; \
		exit 1; \
	fi; \
	if ! LLMTW_REDIS_ADDR="127.0.0.1:$(READINESS_REDIS_PORT)" $(GO) test -count=1 -tags=integration ./storage/redis -run '^TestLiveRedis'; then \
		docker logs "$$container" >&2 || true; \
		exit 1; \
	fi

compose-smoke:
	@command -v "$(firstword $(COMPOSE))" >/dev/null 2>&1 || { \
		echo "compose-smoke requires '$(firstword $(COMPOSE))'; install Docker Compose or set COMPOSE to an equivalent command" >&2; \
		exit 2; \
	}
	$(GO) test ./integration/compose
	$(COMPOSE) config --quiet
	@if [ "$${LLMTW_COMPOSE_LIVE:-0}" = "1" ]; then \
		echo "LLMTW_COMPOSE_LIVE=1 requested, but the worker live gate is intentionally opt-in and is not started by this offline-safe target." >&2; \
		echo "Run the documented Compose commands only in an authorized environment after supplying a continuation key and provider/state runtime." >&2; \
		exit 2; \
	else \
		echo "compose smoke passed (Compose model and offline fixture checks; live services not started)"; \
	fi

deployment-policy-verify:
	KUBECTL= $(GO) test ./integration/kubernetes

kustomize-verify:
	@command -v "$(KUBECTL)" >/dev/null 2>&1 || { \
		echo "kustomize-verify requires '$(KUBECTL)'; install kubectl or set KUBECTL to a pinned executable" >&2; \
		exit 2; \
	}
	KUBECTL="$(KUBECTL)" $(GO) test ./integration/kubernetes
	KUBECTL="$(KUBECTL)" ./deploy/verify.sh

adapter-contracts:
	$(GO) test -v ./llm/provider/contracttest ./llm/provider/...

security-verify:
	@workspace="$$(mktemp -d)"; \
	cleanup() { rm -rf "$$workspace"; }; \
	trap cleanup EXIT HUP INT TERM; \
	status=0; \
	test_status=pass; \
	source_status=pass; \
	go_mod_status=pass; \
	vulnerability_status=pass; \
	GOTOOLCHAIN=$(SECURITY_GO_TOOLCHAIN) $(GO) test -json ./... >"$$workspace/test-output.json" 2>&1 || { status=1; test_status=fail; }; \
	GOTOOLCHAIN=$(SECURITY_GO_TOOLCHAIN) $(GO) run ./tools/sourceverify -root . -test-output "$$workspace/test-output.json" || { status=1; source_status=fail; }; \
	GOTOOLCHAIN=$(SECURITY_GO_TOOLCHAIN) $(GO) mod edit -json >"$$workspace/go-mod.json" 2>&1 || { status=1; go_mod_status=fail; }; \
	GOTOOLCHAIN=$(SECURITY_GO_TOOLCHAIN) $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -format json ./... >"$$workspace/govulncheck.json" 2>"$$workspace/govulncheck.stderr" || { status=1; vulnerability_status=fail; }; \
	GOTOOLCHAIN=$(SECURITY_GO_TOOLCHAIN) $(GO) run ./tools/supplychainverify -baseline tools/supplychainverify/baseline.json -go-mod "$$workspace/go-mod.json" -vulnerability-output "$$workspace/govulncheck.json" -report "$$workspace/security-verify.json" -test-status "$$test_status" -source-status "$$source_status" -go-mod-status "$$go_mod_status" -vulnerability-status "$$vulnerability_status" || status=1; \
	if [ -n "$${SECURITY_REPORT:-}" ] && [ -f "$$workspace/security-verify.json" ]; then \
		mkdir -p "$$(dirname "$$SECURITY_REPORT")"; \
		cp "$$workspace/security-verify.json" "$$SECURITY_REPORT"; \
	fi; \
	exit "$$status"

verify: fmt-check schema-verify docs-verify vet test build
