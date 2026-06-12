# tunnel-client adminui

Svelte + TypeScript admin UI for `tunnel-client`.

## Run in dev mode (with proxy)

From repo root:

```bash
cd adminui
pnpm install --frozen-lockfile --config.shared-workspace-lockfile=false --config.confirmModulesPurge=false
pnpm dev
```

Open the URL printed by Vite (usually `http://127.0.0.1:5173`).

The dev server proxies API/health/metrics requests to:

- default: `http://127.0.0.1:8080`
- override with `ADMIN_UI_PROXY_TARGET`

Example:

```bash
ADMIN_UI_PROXY_TARGET=http://127.0.0.1:18080 pnpm dev
```

## Sync generated assets to Go embed directory

From `adminui`:

```bash
pnpm build
```

This updates:

- `pkg/adminui/assets/app.js`
- `pkg/adminui/assets/styles.css`
- `pkg/adminui/assets/index.html`

## Verify assets are up to date

From repo root:

```bash
./scripts/verify_admin_ui.sh
```

## Run unit tests (Vitest)

From `adminui`:

```bash
pnpm test
```

From the repository root:

```bash
make admin-ui-test
```

Bazel target:

```bash
bazel test //api/tunnel-client/adminui:vitest_test
```
