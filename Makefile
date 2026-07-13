SHELL := /bin/sh

GO ?= go

.PHONY: fmt-check vet test build verify

fmt-check:
	@test -z "$$(gofmt -l .)"

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

build:
	$(GO) build ./...

verify: fmt-check vet test build
