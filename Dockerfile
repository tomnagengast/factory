# syntax=docker/dockerfile:1

ARG CLOUDFLARED_VERSION=2026.7.3
ARG CLOUDFLARED_SOURCE_SHA=8e452b1630064f5951e18a2537e66274e006eb2e83daa0d42a0adb3fab3ee788
ARG GH_VERSION=2.93.0

FROM oven/bun:1.3.11 AS web-build
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run typecheck && bun run build

FROM golang:1.26.5-bookworm AS go-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM go-base AS github-auth-build
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/github-token-broker ./cmd/github-token-broker \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/github-token-client ./cmd/github-token-client

FROM go-base AS go-build
COPY api/ api/
COPY cli/ cli/
COPY --from=web-build /src/web/dist/ api/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/factory-api ./api \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/factory ./cli

FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm AS cloudflared-build
ARG TARGETARCH
ARG CLOUDFLARED_VERSION
ARG CLOUDFLARED_SOURCE_SHA
WORKDIR /src
RUN curl -fsSL "https://github.com/cloudflare/cloudflared/archive/refs/tags/${CLOUDFLARED_VERSION}.tar.gz" -o /tmp/cloudflared.tar.gz \
    && echo "${CLOUDFLARED_SOURCE_SHA}  /tmp/cloudflared.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/cloudflared.tar.gz --strip-components=1 \
    && go mod edit -require=google.golang.org/grpc@v1.82.1 \
    && go mod download \
    && CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" \
       go build -mod=mod -trimpath -buildvcs=false \
       -ldflags="-s -w -X main.Version=${CLOUDFLARED_VERSION}" \
       -o /out/cloudflared ./cmd/cloudflared

FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm AS gh-build
ARG TARGETARCH
ARG GH_VERSION
WORKDIR /src
# Build the tagged CLI with patched dependency versions so the runtime image
# does not inherit known CVEs from the upstream prebuilt binary.
RUN go mod download "github.com/cli/cli/v2@v${GH_VERSION}" \
    && cp -R "/go/pkg/mod/github.com/cli/cli/v2@v${GH_VERSION}/." . \
    && chmod -R u+w . \
    && go get \
       github.com/in-toto/in-toto-golang@v0.11.0 \
       github.com/sigstore/rekor@v1.5.2 \
       github.com/sigstore/sigstore-go@v1.2.1 \
       github.com/sigstore/timestamp-authority/v2@v2.1.2 \
       golang.org/x/text@v0.39.0 \
       google.golang.org/grpc@v1.82.1 \
    && CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" \
       go build -mod=mod -trimpath -buildvcs=false \
       -ldflags="-s -w -X github.com/cli/cli/v2/internal/build.Version=${GH_VERSION}" \
       -o /out/gh ./cmd/gh

FROM debian:bookworm-slim AS tools
ARG TARGETARCH
ARG WORKFLOW_VERSION=0.0.7
ARG CODEX_VERSION=0.146.1
ARG CLAUDE_VERSION=2.1.220
COPY --from=cloudflared-build /out/cloudflared /out/cloudflared
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && case "$TARGETARCH" in \
         amd64) \
           workflow_sha=08d9600dfcf43983d7bce1948e7b1ac479347c653be6ba74253a36ded842991b; \
           codex_arch=x86_64; \
           codex_sha=f558105aec12bf6fb33570793adfc089f8b41dc32aced60b8b4fba9b451824ac; \
           claude_platform=linux-x64; \
           claude_sha=674f61f20ff306f3100cf9200e4c36c4b70278b5bef2884549819b942a89c863 ;; \
         arm64) \
           workflow_sha=25673cf8bd339973e4c11a41d8b555cc42cd2cbf898a8ef4d25004e34b6202a3; \
           codex_arch=aarch64; \
           codex_sha=05de65ee7b6bd02038e720cc313941d5ec6794718e4261bd28fd83b93fe34d43; \
           claude_platform=linux-arm64; \
           claude_sha=159e4a51d796f3bf14677577100f7efb845611b1ceaf0c30cbd8d4650d942185 ;; \
         *) echo "unsupported target architecture: $TARGETARCH" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://github.com/tomnagengast/workflow/releases/download/v${WORKFLOW_VERSION}/workflow_${WORKFLOW_VERSION}_linux_${TARGETARCH}.tar.gz" -o /tmp/workflow.tar.gz \
    && echo "${workflow_sha}  /tmp/workflow.tar.gz" | sha256sum -c - \
    && curl -fsSL "https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}/codex-${codex_arch}-unknown-linux-musl.tar.gz" -o /tmp/codex.tar.gz \
    && echo "${codex_sha}  /tmp/codex.tar.gz" | sha256sum -c - \
    && curl -fsSL "https://downloads.claude.ai/claude-code-releases/${CLAUDE_VERSION}/${claude_platform}/claude" -o /tmp/claude \
    && echo "${claude_sha}  /tmp/claude" | sha256sum -c - \
    && mkdir -p /out \
    && tar -xzf /tmp/workflow.tar.gz -C /out \
    && tar -xzf /tmp/codex.tar.gz -C /tmp \
    && mv "/tmp/codex-${codex_arch}-unknown-linux-musl" /out/codex \
    && mv /tmp/claude /out/claude \
    && chmod 0755 /out/workflow /out/codex /out/claude /out/cloudflared

FROM cgr.dev/chainguard/wolfi-base:latest@sha256:ca263a0360cca48e8fe3f86c8af61c6d5b85e484809fe187440a4206a50efc06 AS github-token-broker
RUN apk add --no-cache ca-certificates \
    && addgroup -g 10001 broker \
    && adduser -D -H -u 10001 -G broker -h /var/empty broker
COPY --from=github-auth-build /out/github-token-broker /usr/local/bin/github-token-broker
EXPOSE 8787
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/github-token-broker"]
CMD ["-listen", "0.0.0.0:8787"]

FROM cgr.dev/chainguard/wolfi-base:latest@sha256:ca263a0360cca48e8fe3f86c8af61c6d5b85e484809fe187440a4206a50efc06 AS factory
RUN apk add --no-cache bash ca-certificates curl git openssh-client \
    && addgroup -g 10001 factory \
    && adduser -D -H -u 10001 -G factory -h /var/lib/factory/home factory \
    && mkdir -p /home/repos /var/lib/factory/home /var/lib/factory/codex /var/lib/factory/projects /var/lib/factory/workflows \
    && chown -R factory:factory /home/repos /var/lib/factory
COPY --from=go-build /out/factory-api /out/factory /usr/local/bin/
COPY --from=github-auth-build /out/github-token-client /usr/local/bin/github-token-client
COPY --from=tools /out/workflow /out/codex /out/claude /out/cloudflared /usr/local/bin/
COPY --from=gh-build /out/gh /usr/local/libexec/gh
COPY --chmod=0755 github-auth/gh-wrapper.sh /usr/local/bin/gh
COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint
RUN ln -s /usr/local/bin/github-token-client /usr/local/bin/git-credential-github-app
ENV SHELL=/bin/bash \
    HOME=/var/lib/factory/home \
    CODEX_HOME=/var/lib/factory/codex \
    DISABLE_AUTOUPDATER=1 \
    PORT=8092
WORKDIR /var/lib/factory/home
EXPOSE 8092
USER 10001:10001
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8092/api/health >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint"]
CMD ["/usr/local/bin/factory-api", "-addr", "0.0.0.0:8092", "-workflow-workspace", "/var/lib/factory/workflows", "-factory", "/usr/local/bin/factory", "-workflow", "/usr/local/bin/workflow", "-codex", "/usr/local/bin/codex", "-claude", "/usr/local/bin/claude"]
