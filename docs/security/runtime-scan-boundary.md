# Runtime scan boundary

The `tunnel-client-runtime` release is the narrow client flavor for customers
that do not need the support UI, development commands, plugin surfaces, or the
bundled companion. A scanner result is in scope for this flavor only when the
affected bytes or first-party package are present in the exact release being
deployed.

## Select the release evidence

For a release `vX.Y.Z`, download
`tunnel-client-runtime-vX.Y.Z-scan-manifest.json` from the same GitHub
Release as the runtime artifact. Use the manifest that names the exact
operating system, architecture, ZIP, SPDX document, license report, and source
archive you plan to scan. Do not use a source baseline as proof for a
released digest.

Verify every downloaded file against the digest recorded in the scan manifest
before scanning it. For example:

```sh
jq -r '.artifacts[] | [.sha256, .name] | @tsv' \
  tunnel-client-runtime-vX.Y.Z-scan-manifest.json |
while IFS="$(printf '\t')" read -r digest name; do
  printf '%s  %s\n' "$digest" "$name" | sha256sum -c -
done
```

The release manifest is the authoritative map from a runtime ZIP digest to its
matching SPDX 2.3 sidecar, platform license report, and source archive. The
reviewable files under `compliance/` are useful drift baselines, but they are
not release-specific evidence.

## Included and excluded scope

The normal runtime includes the control-plane poller, MCP transports, OAuth
discovery and forwarding, runtime Harpoon, TLS, proxy, logging, metrics,
process/PID handling, and health endpoints. Its command surface is limited to
`run`, help, and version.

The release boundary excludes the full command tree, support UI and admin
routes, Codex integration, plugins, local proxy, full-client Harpoon payload
capture, and all companion packages. The scan manifest contains the exact
included and excluded first-party package lists for the release; use those
lists when classifying a finding.

If a finding names an excluded package, verify absence in all three matching
evidence sets before dispositioning it as outside this flavor:

1. the runtime binary or image digest;
2. the release-specific SPDX document and license report;
3. the runtime source archive manifest.

If any evidence is missing or does not match the deployed digest, keep the
finding in scope until the release owner supplies artifact-bound proof.

## What to scan

For binary or dependency analysis, scan the runtime ZIP or image together with
its matching SPDX document and license report. For first-party static analysis,
scan `tunnel-client-runtime-source-vX.Y.Z.tar.gz` from the same release.
Record the scan-manifest digest, artifact digest, scanner version, and finding
disposition with the results so another reviewer can reproduce the decision.
