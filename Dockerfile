# syntax=docker/dockerfile:1.7
#
# Atlas container image.
#  - Multi-stage: the Go toolchain never ships to production.
#  - Static, CGO-free, stripped, reproducible (-trimpath) binaries.
#  - Distroless "static:nonroot" runtime: no shell, no package manager,
#    runs as UID 65532. Nothing for an attacker to live off.

ARG GO_VERSION=1.27

# ---- build ------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w \
        -X github.com/manziosee/atlas/internal/version.Version=${VERSION} \
        -X github.com/manziosee/atlas/internal/version.Commit=${COMMIT} \
        -X github.com/manziosee/atlas/internal/version.Date=${BUILD_DATE}" \
      -o /out/ ./cmd/...

RUN mkdir -p /data && chmod 0750 /data

# ---- runtime ----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="atlas" \
      org.opencontainers.image.description="Distributed edge service discovery over SWIM-style gossip" \
      org.opencontainers.image.source="https://github.com/manziosee/atlas" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

COPY --from=build /out/atlas-node /out/atlas-cli /usr/local/bin/
COPY --from=build --chown=65532:65532 /data /data

USER 65532:65532

# 7946/udp gossip + 7946/tcp full-state sync · 8080/tcp HTTP API · 8082/tcp gRPC · 9090/tcp metrics
EXPOSE 7946/udp 7946/tcp 8080/tcp 8082/tcp 9090/tcp

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/atlas-cli", "health"]

ENTRYPOINT ["/usr/local/bin/atlas-node"]
