SHELL := /bin/sh

GO ?= go

.PHONY: fmt-check schema-verify docs-verify vet test build verify

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

verify: fmt-check schema-verify docs-verify vet test build
