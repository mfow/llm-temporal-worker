SHELL := /bin/sh

GO ?= go
COMPOSE ?= docker compose
KUBECTL ?= kubectl

.PHONY: fmt-check schema-verify docs-verify vet test build integration compose-smoke kustomize-verify verify

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

verify: fmt-check schema-verify docs-verify vet test build
