SHELL := /bin/sh

GO ?= go
COMPOSE ?= docker compose
KUBECTL ?= kubectl
READINESS_REDIS_IMAGE ?= redis:7.4.2-alpine@sha256:02419de7eddf55aa5bcf49efb74e88fa8d931b4d77c07eff8a6b2144472b6952
READINESS_REDIS_CONTAINER_PREFIX ?= llmtw-readiness-integration
READINESS_REDIS_PORT ?= 16379

.PHONY: fmt-check schema-verify docs-verify vet test build integration readiness-integration compose-smoke kustomize-verify adapter-contracts verify

fmt-check:
	@test -z "$$(gofmt -l .)"

schema-verify:
	$(GO) test ./llm/schema ./config

docs-verify:
	$(GO) test ./internal/documentationtest -run TestDocumentationLinksAndInvariants

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
	if ! LLMTW_REDIS_ADDR="127.0.0.1:$(READINESS_REDIS_PORT)" $(GO) test -count=1 -tags=integration ./storage/redis -run '^TestLiveRedisAdmission$$'; then \
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

kustomize-verify:
	@command -v "$(KUBECTL)" >/dev/null 2>&1 || { \
		echo "kustomize-verify requires '$(KUBECTL)'; install kubectl or set KUBECTL to a pinned executable" >&2; \
		exit 2; \
	}
	$(GO) test ./integration/kubernetes
	KUBECTL="$(KUBECTL)" ./deploy/verify.sh

adapter-contracts:
	$(GO) test -v ./llm/provider/contracttest ./llm/provider/...

verify: fmt-check schema-verify docs-verify vet test build
