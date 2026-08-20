# Runtime cloudflared scan boundary

The `tunnel-client-runtime-cloudflared` release is the narrow runtime plus
the pinned companion process. It is a distinct flavor from
`tunnel-client-runtime`; use evidence for this flavor when the deployed
artifact contains the companion binary.

## Select the release evidence

For a release `vX.Y.Z`, download
`tunnel-client-runtime-cloudflared-vX.Y.Z-scan-manifest.json` from the same
GitHub Release as the deployed artifact. The manifest records the exact
platform ZIP, image, SPDX 2.3 sidecar, platform license report, runtime source
archive, companion source archive, and their SHA256 digests.

Verify every file against the manifest before scanning:

```sh
jq -r '.artifacts[] | [.sha256, .name] | @tsv' \
  tunnel-client-runtime-cloudflared-vX.Y.Z-scan-manifest.json |
while IFS="$(printf '\t')" read -r digest name; do
  printf '%s  %s\n' "$digest" "$name" | sha256sum -c -
done
```

The mirrored `compliance/` SPDX files are deterministic review baselines.
They can show dependency drift, but only the release-specific scan manifest
binds evidence to the bytes being deployed.

## Included and excluded scope

This flavor includes everything in the normal runtime plus only the runtime
companion supervisor, pinned manifest, and pinned companion binary. It keeps
the same small command surface: `run`, help, and version.

It excludes the full command tree, support UI and admin routes, Codex
integration, plugins, local proxy, full-client Harpoon payload capture, and
the full companion configuration package. The release scan manifest names the
exact package delta allowed for this flavor.

Treat a finding as outside this flavor only after the matching binary or image,
SPDX document, license report, and both source manifests prove the affected
package or bytes are absent. A missing or mismatched digest keeps the finding
in scope.

## What to scan

For binary or dependency analysis, scan the runtime-cloudflared ZIP or image
together with its matching SPDX document and license report. For complete
first-party and companion source analysis, scan both:

- `tunnel-client-runtime-cloudflared-source-vX.Y.Z.tar.gz`
- `cloudflared-<pinned-module-version>-source.tar.gz`

Record the scan-manifest digest, artifact digest, scanner version, and finding
disposition with the results so the decision remains reproducible.
