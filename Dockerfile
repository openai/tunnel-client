# syntax=docker/dockerfile:1.6@sha256:ac85f380a63b13dfcefa89046420e1781752bab202122f8f50032edf31be0021

ARG BASE_BUILDER_IMAGE=golang:1.27.0-alpine
ARG BASE_UI_BUILDER_IMAGE=node:22-alpine
ARG BASE_IMAGE=alpine:3.22
ARG GIT_SHA=dev
ARG PROJECT_ROOT=.

FROM --platform=${BUILDPLATFORM} ${BASE_UI_BUILDER_IMAGE} AS ui-builder
ARG PROJECT_ROOT=.
ARG PNPM_PACKAGE_MANAGER
WORKDIR /repo
RUN --mount=type=bind,source=.,target=/context,ro \
    --mount=type=secret,id=COREPACK_NPM_REGISTRY \
    --mount=type=secret,id=NPM_CONFIG_REGISTRY \
    --mount=type=secret,id=npm_config_registry \
    --mount=type=secret,id=PNPM_CONFIG_REGISTRY \
    --mount=type=secret,id=pnpm_config_registry \
    for registry_secret in \
      COREPACK_NPM_REGISTRY \
      NPM_CONFIG_REGISTRY \
      npm_config_registry \
      PNPM_CONFIG_REGISTRY \
      pnpm_config_registry; do \
      if [ -s "/run/secrets/${registry_secret}" ]; then \
        export "${registry_secret}=$(cat "/run/secrets/${registry_secret}")"; \
      fi; \
    done \
    && corepack enable pnpm \
    && corepack prepare "${PNPM_PACKAGE_MANAGER:-$(node -p 'require("/context/package.json").packageManager')}" --activate
COPY ${PROJECT_ROOT}/adminui/package.json ./adminui/
COPY ${PROJECT_ROOT}/adminui/pnpm-lock.yaml ./adminui/
COPY ${PROJECT_ROOT}/adminui/pnpm-workspace.yaml ./adminui/
COPY ${PROJECT_ROOT}/adminui/ ./adminui/
RUN --mount=type=secret,id=COREPACK_NPM_REGISTRY \
    --mount=type=secret,id=NPM_CONFIG_REGISTRY \
    --mount=type=secret,id=npm_config_registry \
    --mount=type=secret,id=PNPM_CONFIG_REGISTRY \
    --mount=type=secret,id=pnpm_config_registry \
    for registry_secret in \
      COREPACK_NPM_REGISTRY \
      NPM_CONFIG_REGISTRY \
      npm_config_registry \
      PNPM_CONFIG_REGISTRY \
      pnpm_config_registry; do \
      if [ -s "/run/secrets/${registry_secret}" ]; then \
        export "${registry_secret}=$(cat "/run/secrets/${registry_secret}")"; \
      fi; \
    done \
    && CI=true pnpm --dir adminui install --frozen-lockfile --config.shared-workspace-lockfile=false --config.confirmModulesPurge=false \
    && pnpm --dir adminui build

FROM --platform=${BUILDPLATFORM} ${BASE_BUILDER_IMAGE} AS builder
ARG PROJECT_ROOT=.
ARG GIT_SHA=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /go/src/app

COPY ${PROJECT_ROOT}/go.mod ${PROJECT_ROOT}/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY ${PROJECT_ROOT}/ ./
COPY --from=ui-builder /repo/pkg/adminui/assets ./pkg/adminui/assets
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags "-X github.com/openai/tunnel-client/pkg/version.GitSHA=${GIT_SHA}" \
    -o /usr/local/bin/tunnel-client ./cmd/client

FROM --platform=${BUILDPLATFORM} ${BASE_BUILDER_IMAGE} AS cloudflared-builder
ARG PROJECT_ROOT=.
ARG TARGETOS
ARG TARGETARCH
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

FROM runtime-base
EXPOSE 8080

ENTRYPOINT ["/usr/bin/tunnel-client", "run"]
