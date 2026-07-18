# syntax=docker/dockerfile:1.7

# The digest is the multi-platform index for the Go 1.26.5 Bookworm image.
# Renovate/security review should update the tag and digest together.
ARG GO_IMAGE=docker.io/library/golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_TIME=unknown
ARG SOURCE=https://github.com/mfow/llm-temporal-worker
ARG GO_VERSION=go1.26.5

FROM ${GO_IMAGE} AS build

ARG TARGETARCH
ARG VERSION
ARG REVISION
ARG BUILD_TIME
ARG SOURCE
ARG GO_VERSION

WORKDIR /src

# Keep dependency downloads in a cacheable layer and never copy credentials
# into the build context. BuildKit cache mounts are optional but harmless when
# the builder does not enable them.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    test "$(go env GOVERSION)" = "${GO_VERSION}" && \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w \
        -X github.com/mfow/llm-temporal-worker/golang/internal/buildinfo.Version=${VERSION} \
        -X github.com/mfow/llm-temporal-worker/golang/internal/buildinfo.Revision=${REVISION} \
        -X github.com/mfow/llm-temporal-worker/golang/internal/buildinfo.BuildTime=${BUILD_TIME} \
        -X github.com/mfow/llm-temporal-worker/golang/internal/buildinfo.Source=${SOURCE} \
        -X github.com/mfow/llm-temporal-worker/golang/internal/buildinfo.GoVersion=${GO_VERSION}" \
      -o /out/llm-temporal-worker ./cmd/llm-temporal-worker

# Distroless static includes CA roots, time-zone data, and uid 65532, but no
# shell or package manager. The digest is the immutable multi-platform image
# index for the nonroot variant.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:8dd8d3ca2cf283383304fd45a5c9c74d5f2cd9da8d3b077d720e264880077c65

ARG VERSION
ARG REVISION
ARG BUILD_TIME
ARG SOURCE
ARG GO_VERSION

LABEL org.opencontainers.image.title="llm-temporal-worker" \
      org.opencontainers.image.description="Replay-safe Temporal LLM inference worker" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${BUILD_TIME}" \
      org.opencontainers.image.source="${SOURCE}" \
      io.github.mfow.llm-temporal-worker.go.version="${GO_VERSION}"

COPY --from=build /out/llm-temporal-worker /usr/local/bin/llm-temporal-worker

USER 65532:65532
WORKDIR /home/nonroot

# Health is provided by the orchestrator. Distroless intentionally has no
# shell/curl healthcheck and the root filesystem is expected to be read-only.
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/llm-temporal-worker"]
