# syntax=openaiapibase.azurecr.io/mirror/docker/dockerfile:1@sha256:9857836c9ee4268391bb5b09f9f157f3c91bb15821bb77969642813b0d00518d

ARG BASE_BUILDER_IMAGE=golang:1.26.2-alpine
ARG BASE_UI_BUILDER_IMAGE=node:22-alpine
ARG BASE_IMAGE=alpine:3.22
ARG GIT_SHA=dev
ARG PROJECT_ROOT=.

FROM ${BASE_UI_BUILDER_IMAGE} AS ui-builder
ARG PROJECT_ROOT=.
WORKDIR /repo
COPY package.json /tmp/openai-root-package.json
RUN --mount=type=secret,id=COREPACK_NPM_REGISTRY,env=COREPACK_NPM_REGISTRY \
    --mount=type=secret,id=NPM_CONFIG_REGISTRY,env=NPM_CONFIG_REGISTRY \
    --mount=type=secret,id=npm_config_registry,env=npm_config_registry \
    --mount=type=secret,id=PNPM_CONFIG_REGISTRY,env=PNPM_CONFIG_REGISTRY \
    --mount=type=secret,id=pnpm_config_registry,env=pnpm_config_registry \
    corepack enable pnpm \
    && corepack prepare "$(node -p 'require("/tmp/openai-root-package.json").packageManager')" --activate
COPY ${PROJECT_ROOT}/adminui/package.json ./adminui/
COPY ${PROJECT_ROOT}/adminui/pnpm-lock.yaml ./adminui/
COPY ${PROJECT_ROOT}/adminui/pnpm-workspace.yaml ./adminui/
COPY ${PROJECT_ROOT}/adminui/ ./adminui/
RUN --mount=type=secret,id=COREPACK_NPM_REGISTRY,env=COREPACK_NPM_REGISTRY \
    --mount=type=secret,id=NPM_CONFIG_REGISTRY,env=NPM_CONFIG_REGISTRY \
    --mount=type=secret,id=npm_config_registry,env=npm_config_registry \
    --mount=type=secret,id=PNPM_CONFIG_REGISTRY,env=PNPM_CONFIG_REGISTRY \
    --mount=type=secret,id=pnpm_config_registry,env=pnpm_config_registry \
    CI=true pnpm --dir adminui install --frozen-lockfile --config.shared-workspace-lockfile=false --config.confirmModulesPurge=false \
    && pnpm --dir adminui build

FROM ${BASE_BUILDER_IMAGE} AS builder
ARG PROJECT_ROOT=.
ARG GIT_SHA=dev
WORKDIR /go/src/app

COPY ${PROJECT_ROOT}/go.mod ${PROJECT_ROOT}/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY ${PROJECT_ROOT}/ ./
COPY --from=ui-builder /repo/pkg/adminui/assets ./pkg/adminui/assets
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build \
    -ldflags "-X github.com/openai/tunnel-client/pkg/version.GitSHA=${GIT_SHA}" \
    -o /usr/local/bin/tunnel-client ./cmd/client

FROM ${BASE_BUILDER_IMAGE} AS cloudflared-builder
ARG PROJECT_ROOT=.
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG GOPROXY=https://proxy.golang.org
ENV GOPROXY=${GOPROXY}
WORKDIR /repo
RUN apk add --no-cache bash python3
COPY ${PROJECT_ROOT}/pkg/cloudflared/manifest.json ./pkg/cloudflared/manifest.json
COPY ${PROJECT_ROOT}/scripts/build_cloudflared.sh ./scripts/build_cloudflared.sh
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    bash ./scripts/build_cloudflared.sh \
    --goos "${TARGETOS}" \
    --goarch "${TARGETARCH}" \
    --output /usr/local/bin/cloudflared

FROM ${BASE_IMAGE} AS runtime-base
WORKDIR /app

COPY --from=builder /usr/local/bin/tunnel-client /usr/bin/tunnel-client
COPY --from=cloudflared-builder /usr/local/bin/cloudflared /usr/bin/cloudflared

FROM runtime-base AS unittest
RUN printf '#!/bin/sh\nexec "$@"\n' > /entrypoint.sh \
    && chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]

FROM runtime-base
EXPOSE 8080

ENTRYPOINT ["/usr/bin/tunnel-client", "run"]
