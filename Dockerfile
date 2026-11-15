# syntax=docker/dockerfile:1

# ---- build ----------------------------------------------------------------
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/tvctl ./cmd/tvctl

# ---- runtime --------------------------------------------------------------
# Node, because plain-language requests run Claude Code headless (`claude -p`,
# WebSearch only) with the subscription token in CLAUDE_CODE_OAUTH_TOKEN.
FROM node:24-bookworm-slim
ARG CLAUDE_CODE_VERSION=2.1.299
RUN apt-get update \
 && apt-get install -y --no-install-recommends tini ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
 && npm cache clean --force \
 && useradd --uid 10001 --create-home --shell /usr/sbin/nologin app
COPY --from=build /out/tvctl /usr/local/bin/tvctl
USER 10001
ENV HOME=/home/app \
    TVCTL_DATA=/data \
    DISABLE_AUTOUPDATER=1 \
    DISABLE_TELEMETRY=1
WORKDIR /home/app
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/tvctl"]
CMD ["serve"]
