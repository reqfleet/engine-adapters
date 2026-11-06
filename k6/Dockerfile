# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ENV GOWORK=off GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/k6-adapter ./k6/cmd/agent && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go test -c -tags=integration -o /out/conformance ./conformance
# Keep SDK transitive license notices with the executable distribution.
RUN cp "$(go list -m -f '{{.Dir}}' github.com/reqfleet/engine-sdk)/THIRD_PARTY_NOTICES" /out/SDK_THIRD_PARTY_NOTICES

FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS engine
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
WORKDIR /download
# Pinned engine release. Check official checksum file itself against release metadata,
# then verify the matching architecture archive before extracting the executable.
RUN case "$TARGETARCH" in amd64|arm64) ;; *) exit 1 ;; esac && \
    curl -fsSLO https://github.com/grafana/k6/releases/download/v2.3.0/k6-v2.3.0-checksums.txt && \
    echo '8ebbfcd5a13d68ac67d488c2b5f28bf901ea22d2fbbfb15f5345d65c39163be2  k6-v2.3.0-checksums.txt' | sha256sum -c - && \
    curl -fsSLO "https://github.com/grafana/k6/releases/download/v2.3.0/k6-v2.3.0-linux-${TARGETARCH}.tar.gz" && \
    sha256sum --check --ignore-missing k6-v2.3.0-checksums.txt && \
    tar -xzf "k6-v2.3.0-linux-${TARGETARCH}.tar.gz" && \
    cp "k6-v2.3.0-linux-${TARGETARCH}/k6" /usr/local/bin/k6
# Distribute corresponding upstream source (including its licenses) alongside k6.
RUN curl -fsSL https://github.com/grafana/k6/archive/e0887846143ab176d4b5483c9d52cf3b3e009f1a.tar.gz -o k6-v2.3.0-source.tar.gz && \
    curl -fsSL https://raw.githubusercontent.com/grafana/k6/e0887846143ab176d4b5483c9d52cf3b3e009f1a/LICENSE.md -o K6_LICENSE

FROM debian:bookworm-slim AS base
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* && \
    mkdir -p /test-data /shibuya-agent/test-conf /shibuya-agent/test-result /shibuya-agent/run-tmp /usr/share/licenses/engine-adapters && \
    chown -R 65532:65532 /test-data /shibuya-agent
COPY --from=engine /usr/local/bin/k6 /usr/local/bin/k6
COPY --from=engine /download/k6-v2.3.0-source.tar.gz /download/K6_LICENSE /usr/share/licenses/engine-adapters/
COPY --from=build /out/k6-adapter /usr/local/bin/k6-adapter
COPY --from=build /out/SDK_THIRD_PARTY_NOTICES /usr/share/licenses/engine-adapters/
COPY LICENSE NOTICE /usr/share/licenses/engine-adapters/
ENV AGENT_ROOT=/shibuya-agent HOME=/shibuya-agent
EXPOSE 8080

FROM base AS conformance
COPY --from=build /out/conformance /usr/local/bin/conformance
USER 0:0
ENTRYPOINT ["/usr/local/bin/conformance", "-test.v", "-test.timeout=8m"]

FROM base AS runtime
LABEL org.opencontainers.image.source="https://github.com/reqfleet/engine-adapters" \
      org.opencontainers.image.description="HTTP k6 adapter; SDK bc85eb9551f9, k6 v2.3.0, metrics shibuya-v1" \
      org.opencontainers.image.licenses="Apache-2.0 AND AGPL-3.0-only"
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/k6-adapter"]
