# syntax=docker/dockerfile:1
ARG K6_VERSION=v2.3.0
ARG ENGINE_SDK_VERSION=v0.5.1-0.20261112054413-17ae897d7cc6
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG ENGINE_SDK_VERSION
ENV GOWORK=off GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Apply the SDK selection after COPY so it cannot be overwritten by go.mod.
RUN go get "github.com/reqfleet/engine-sdk@${ENGINE_SDK_VERSION}" && \
    test "$(go list -m -f '{{.Version}}' github.com/reqfleet/engine-sdk)" = "$ENGINE_SDK_VERSION"
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/k6-adapter ./k6/cmd/agent && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go test -c -tags=integration -o /out/conformance-runtime ./conformance && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go test -c -tags=integration -o /out/conformance ./k6/conformance
# Keep SDK transitive license notices with the executable distribution.
RUN cp "$(go list -m -f '{{.Dir}}' github.com/reqfleet/engine-sdk)/THIRD_PARTY_NOTICES" /out/SDK_THIRD_PARTY_NOTICES

FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS engine
ARG TARGETARCH
ARG K6_VERSION
ARG K6_CHECKSUMS_SHA256=8ebbfcd5a13d68ac67d488c2b5f28bf901ea22d2fbbfb15f5345d65c39163be2
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
WORKDIR /download
# Verify the release checksum file against its pinned digest, then the archive.
# Override K6_CHECKSUMS_SHA256 together with K6_VERSION for another release.
RUN case "$TARGETARCH" in amd64|arm64) ;; *) exit 1 ;; esac && \
    curl -fsSLO "https://github.com/grafana/k6/releases/download/${K6_VERSION}/k6-${K6_VERSION}-checksums.txt" && \
    echo "${K6_CHECKSUMS_SHA256}  k6-${K6_VERSION}-checksums.txt" | sha256sum -c - && \
    curl -fsSLO "https://github.com/grafana/k6/releases/download/${K6_VERSION}/k6-${K6_VERSION}-linux-${TARGETARCH}.tar.gz" && \
    sha256sum --check --ignore-missing "k6-${K6_VERSION}-checksums.txt" && \
    tar -xzf "k6-${K6_VERSION}-linux-${TARGETARCH}.tar.gz" && \
    cp "k6-${K6_VERSION}-linux-${TARGETARCH}/k6" /usr/local/bin/k6
# Distribute corresponding upstream source (including its licenses) alongside k6.
RUN curl -fsSL "https://github.com/grafana/k6/archive/refs/tags/${K6_VERSION}.tar.gz" -o "k6-${K6_VERSION}-source.tar.gz" && \
    curl -fsSL "https://raw.githubusercontent.com/grafana/k6/${K6_VERSION}/LICENSE.md" -o K6_LICENSE

FROM debian:bookworm-slim AS base
ARG K6_VERSION
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* && \
    mkdir -p /test-data /shibuya-agent/test-conf /shibuya-agent/test-result /shibuya-agent/run-tmp /usr/share/licenses/engine-adapters && \
    chown -R 65532:65532 /test-data /shibuya-agent
COPY --from=engine /usr/local/bin/k6 /usr/local/bin/k6
COPY --from=engine /download/k6-${K6_VERSION}-source.tar.gz /download/K6_LICENSE /usr/share/licenses/engine-adapters/
COPY --from=build /out/k6-adapter /usr/local/bin/k6-adapter
COPY --from=build /out/SDK_THIRD_PARTY_NOTICES /usr/share/licenses/engine-adapters/
COPY LICENSE NOTICE /usr/share/licenses/engine-adapters/
ENV AGENT_ROOT=/shibuya-agent HOME=/shibuya-agent
EXPOSE 8080

FROM base AS conformance
COPY --from=build /out/conformance /usr/local/bin/conformance
COPY --from=build /out/conformance-runtime /usr/local/bin/conformance-runtime
USER 0:0
ENTRYPOINT ["/bin/sh", "-c", "/usr/local/bin/conformance-runtime -test.v -test.timeout=2m && exec /usr/local/bin/conformance -test.v -test.timeout=8m"]

FROM base AS runtime
ARG ENGINE_SDK_VERSION
LABEL org.opencontainers.image.source="https://github.com/reqfleet/engine-adapters" \
      org.opencontainers.image.description="HTTP k6 adapter; SDK ${ENGINE_SDK_VERSION}, k6 ${K6_VERSION}, metrics shibuya-v1" \
      org.opencontainers.image.licenses="Apache-2.0 AND AGPL-3.0-only"
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/k6-adapter"]
