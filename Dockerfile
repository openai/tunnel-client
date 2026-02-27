# syntax=docker/dockerfile:1.6

ARG BASE_BUILDER_IMAGE=golang:1.26.0-alpine
ARG BASE_UI_BUILDER_IMAGE=node:22-alpine
ARG BASE_IMAGE=alpine:3.22
ARG GIT_SHA=dev
ARG PROJECT_ROOT=.

FROM ${BASE_UI_BUILDER_IMAGE} AS ui-builder
ARG PROJECT_ROOT=.
WORKDIR /repo
RUN corepack enable && corepack prepare pnpm@10.26.2 --activate
COPY ${PROJECT_ROOT}/adminui/package.json ./adminui/
COPY ${PROJECT_ROOT}/adminui/pnpm-lock.yaml ./adminui/
COPY ${PROJECT_ROOT}/adminui/ ./adminui/
RUN CI=true pnpm --dir adminui install --frozen-lockfile --ignore-workspace --config.shared-workspace-lockfile=false \
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
    -ldflags "-X go.openai.org/api/tunnel-client/pkg/version.GitSHA=${GIT_SHA}" \
    -o /usr/local/bin/tunnel-client ./cmd/client

FROM ${BASE_IMAGE} AS runtime-base
WORKDIR /app

COPY --from=builder /usr/local/bin/tunnel-client /usr/bin/tunnel-client

FROM runtime-base AS unittest
RUN printf '#!/bin/sh\nexec "$@"\n' > /entrypoint.sh \
    && chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]

FROM runtime-base
EXPOSE 8080

ENTRYPOINT ["/usr/bin/tunnel-client", "run"]
