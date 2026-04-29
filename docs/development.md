# Development & Testing

This document is for contributors working on `tunnel-client`.

## Build

```bash
./scripts/build_admin_ui.sh ./adminui ./pkg/adminui/assets
# or
make admin-ui
go build -o bin/tunnel-client ./cmd/client
```

Use `./bin/tunnel-client` for local source-checkout runs unless `bin/` is on
your `PATH`.

Before creating a release tag, stamp the source version so downloaded release
archives build with the tag semantic version:

```bash
make release-source-version VERSION=1.2.3
make release-tag VERSION=1.2.3 WORD=ember-orchid
```

## Unit tests

```bash
go test ./...
```

## E2E tests (in-repo harness)

The `e2e/` tests use in-repo test doubles under `testsupport/`:

- `testsupport/mocktunnelservice`: simulates the control plane poll/response endpoints.
- `testsupport/mockmcpserver`: a Streamable HTTP MCP server double.

Run:

```bash
go test ./e2e -count=1
```

## Repo structure (high level)

- `cmd/client`: CLI entrypoint
- `pkg/*`: implementation packages
- `e2e/`: end-to-end tests using in-repo mocks
- `testsupport/`: test helpers and doubles
