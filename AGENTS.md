# Tunnel Client Agent Instructions
## Objectives & Assumptions
- `tunnel-client` is the enterprise-hosted peer to `https://api.openai.com/v1/tunnel`, long-polling `GET /v1/tunnel/{tunnel_id}/poll` and posting back to `POST /v1/tunnel/{tunnel_id}/response` so on-prem MCP servers can service ChatGPT, Responses API, and AgentKit requests.
- Treat this subtree as public `openai/tunnel-client` source. Keep all source, tests, and collateral OSS-friendly and free of internal-only dependencies.
- Maintain compatibility with the existing service contracts defined in `api/tunnel-service/tunnel_service/routers/mcp/tunnel_poll_router.py`.
- Operate as a single process per tunnel, emphasizing resiliency (auto-reconnect, retries) over horizontal scale for MVP.
- Preserve current admin HTTP server endpoints (`/healthz`, `/readyz`, `/metrics`) while expanding metrics coverage to the new workflow.
- **Resource map:** Consult `../tunnel-service/docs/blueprint.md` for the canonical list of tunnel-service/client code and Cloudflare assets before expanding the client surface.

## Public Surface Boundary
- Do **not** introduce references to internal repository layout, internal package managers, or mirror plumbing in code, docs, help topics, comments, tests, examples, logs, error messages, or user-facing prompts.
- Banned examples include `monorepo`, `oaipkg`, `copyberry`, `openai/openai`, `api/copyberry`, internal source-tree paths outside `api/tunnel-client`, and instructions such as `source .envrc && oaipkg install tunnel-client`.
- If internal install or contributor guidance is needed, put it outside `api/tunnel-client` or in a path that is explicitly excluded from the public mirror.
- Existing structural build metadata may remain only when required by repository tooling; do not copy those names into user-facing docs or runtime surfaces.
- Before finishing a change, scan for accidental leaks:
  - `rg -n '\b(monorepo|oaipkg|copyberry|openai/openai|api/copyberry)\b' api/tunnel-client --glob '!AGENTS.md' --glob '!BUILD.bazel' --glob '!pyproject.toml'`

## Codebase Layout
- `cmd/client` – FX-wired CLI entrypoint; builds `tunnel-client` binary.
- `pkg/config` – flag/env parsing. Default base URL is the PROD control plane host root (`https://api.openai.com`); the client appends `/v1/tunnel/...` internally.
- `pkg/controlplane` – HTTP client fpr `/v1/tunnel/*` endpoints, poll loop, and response poster. Internals live under `pkg/controlplane/internal`. Posts to `PolledCommandQueue`
- `pkg/dispatcher` - worker orchestration, listens `PolledCommandQueue` and dispatches commands to `mcp` and handles response processing.
- `pkg/mcpclient` - MCP client for interacting with MCP servers, handling command submissions and response retrieval.
- `pkg/metrics`, `pkg/health`, `pkg/log`, `pkg/version` – Prometheus instrumentation, metrics server, structured logging, and user agent string.
- `manage/applied_spec.py` – Applied manage spec that Bazel-tests `//api/tunnel-client/...` and validates the packaged binary during release automation.
- `plan.md` – Historical integration plan; keep it in sync when you make structural changes.

## Configuration & Runtime
- The binary can be configured through CLI flags (`--control-plane.*`, `--log.*`, `--health.*`, `--mcp.*`) or the mirrored environment variables (`CONTROL_PLANE_API_KEY`, `CONTROL_PLANE_BASE_URL`, `MCP_SERVER_URL`, etc.).
- `CONTROL_PLANE_API_KEY` is required; `OPENAI_API_KEY` only acts as a fallback. Fail fast if both are empty.
- Health endpoints (`/healthz`, `/readyz`, `/metrics`) are served from `HEALTH_LISTEN_ADDR` (default `:8080`). `/metrics` exposes Prometheus counters for readiness and liveness.
- Be cautious with `LOG_HTTP_RAW_UNSAFE=true`; it logs full HTTP payloads and can surface PII. Only enable this in tightly controlled debugging environments.

## Testing instructions
- This mirrors instructions in `api/tunnel-service/AGENTS.md` since **ANY** changes in tunnel-client should trigger full suite retest for both `tunnel-service` and `tunnel-client` 
- If your change includes API updates (request/response shapes, headers, auth, timeouts, or any other wire-level contract change), add an approval-only empty commit after your normal commit:
  - `git commit --allow-empty -m 'I approve the breaking changes in tunnel-service'`
- If you make changes under `api/tunnel-client` that do **not** update, add, or remove libraries, ensure there are no changes in `go.sum`.
- Always run (required):
  - `./api/tunnel-service/scripts/verify.sh`

- Note: `./api/tunnel-service/scripts/verify.sh` can take around 5 minutes to complete. Be patient and wait for it to finish before interrupting. If the script is making progress and does not look stuck, give it up to 10 minutes before stopping it.

- `./api/tunnel-service/scripts/verify.sh` runs the required verification set (listed explicitly here for visibility; do not skip any):
  - `applied bazel build --build_tag_filters="-noci,-manual" //api/tunnel-client/... //api/tunnel-service/...`
  - `applied bazel test --build_tag_filters="-noci,-manual" --test_tag_filters="-noci,-manual" --build_tests_only //api/tunnel-client/... //api/tunnel-service/... -- -//api/tunnel-service:image`
  - `applied tilt ci tunnel-e2e` (the script disables hot reload for this step)

- Reporting requirements (in your final response):
  - Paste the exact command(s) you ran (copy/paste), in order. At minimum this must include:
    - `./api/tunnel-service/scripts/verify.sh`
  - Include evidence that the commands actually executed:
    - Paste the `==> ...` command lines printed by `verify.sh` (those are the "receipts").
    - For each command, include a one-line outcome marker: `PASS` or `FAIL`.
  - If any required command fails, include the failure snippet that explains the root cause (for example, the Pyright error output),
    and what you changed to fix it.
  - If you did not run the full required set, say so explicitly, explain why, and stop (do not proceed as if work is complete).

- Copy/paste template (fill this out in the final response):
  - Commands:
    - `./api/tunnel-service/scripts/verify.sh`
  - Receipts (from `verify.sh` output):
    - `==> applied bazel build <targets>` - PASS/FAIL
    - `==> applied bazel test <targets>` - PASS/FAIL
    - `==> applied tilt ci tunnel-e2e` - PASS/FAIL

Notes:
- Tag filters are configured via the `EXCLUDE_TAGS` array in `scripts/verify.sh` (defaults: `noci`, `manual`).
- Package globs come from the `PACKAGES` array; specific negative test targets come from the `EXCLUDE_TARGETS` array and must appear after `--` per Bazel rules.

- Stop conditions:
  - If any required command fails, fix the failure(s) before concluding the task.
  - If you cannot run the required commands due to environment/tooling issues (for example, missing `applied`),
    stop and ask the user how to proceed rather than skipping verification.

## Implementation Guidelines
- Structure the codebase to separate concerns, ensuring clear boundaries between different components.
- Utilize interfaces to define contracts for interactions between the `tunnel-client` and `tunnel-service`.
- Implement configuration management to handle environment-specific settings securely.
- Prefer re-usable components: before writing new code, look for existing helpers to reuse or refactor to make them reusable, then build on that shared code.
- This directory is intended to be open sourced.
- Do **not** add dependencies on OpenAI-internal libraries (for example, anything under `lib/oaigo`).
- Prefer the Go standard library or third-party OSS dependencies that are acceptable for open-source release.
- For public binary-acquisition or plugin-install guidance, do **not** reference `oaipkg`, Bazel commands, monorepo-root commands, or internal checkout paths. Keep public guidance limited to the public repo, public releases, native `tunnel-client` commands, and exported bundle wrappers.
- `tunnel-client` <-> `tunnel-service` interaction described in the `api/tunnel-service/README.md`. Look for the endpoints under `/v1/tunnel/` path.
  - Ensure to handle authentication and authorization as specified in the API documentation.
  - Implement error handling for all API calls to manage potential failures gracefully.
- Use logging to track requests and responses for debugging purposes.
- Prefer the context-aware logging helpers (e.g. `logger.InfoContext(ctx, ...)`); only fall back to `context.Background()` when no request/context exists.
- Do not use `slog.Default()` as a fallback when a logger is nil; treat a nil logger as a wiring error and assert that a properly initialized logger is provided.
- Follow the established coding conventions and style guides for consistency across the codebase.
- Write unit tests for all new features and ensure existing tests pass before submitting changes.
- Document any new functionality in the README or relevant documentation files.
- Review pull requests thoroughly and provide constructive feedback to maintain code quality.
- `tunnel-client` could be deployed as standalone CLI, as well as part of a larger microservices architecture, i.e. inside Docker containers.
- Ensure that the `tunnel-client` can be easily configured via environment variables or configuration files for flexibility in different deployment scenarios.
- Ensure that the `tunnel-client` has appropriate health checks, liveness checks and metrics exposed for monitoring and observability.

## Mirroring to Open Source
- Anything under `api/tunnel-client` is treated as public mirror content.
- Keep generated artifacts (`bin/`, build outputs) out of commits. Only source, tests, docs, and reproducible configs should sync to the mirror.
- When adding new dependencies, confirm they license friendly so that the public repo stays license-clean. Avoid adding copy-left licenses such as AGPL.
- Update both `README.md` and this guide whenever the runtime surface, build workflow, or testing story changes so contributors across repos stay aligned.

## See also
- `../tunnel-service/AGENTS.md` – Tunnel Service contributor guide.
- `../tunnel-integration-tests/AGENTS.md` – Integration Tests agent notes.

## Security Considerations
- Ensure that all sensitive information, such as API keys and tokens, are managed through environment variables or secure vaults.
- Regularly update dependencies to mitigate security vulnerabilities and maintain compatibility with upstream changes.
- Conduct security audits and code reviews to identify potential security issues.

## Operational Expectations
- Review PRs for regressions in resiliency (poll loop backoff, dispatcher queue limits) and ensure unit/integration tests cover new edge cases before merging.
