# Experimental Android / Termux runtime

The plain `tunnel-client-runtime` can be cross-compiled for Android ARM64 with
Android's Bionic libc and cgo. This build uses the native Android hostname
resolver for ordinary HTTP connections. It targets API 28 (Android 9) or newer;
device compatibility is experimental and must be validated on the intended
Android version and network.

The regular Linux ARM64 release is built without cgo and is not an Android
release. On Termux it can try localhost DNS when `/etc/resolv.conf` is absent,
even if Node or other Android-native programs resolve the same name.

## Versioned downloads

Each tagged release publishes the Android ARM64 plain runtime alongside the
other release downloads, including stable tags and prerelease tags. Android
support remains experimental. Choose the same version as the release:

```text
tunnel-client-runtime-vX.Y.Z-android-arm64.zip
tunnel-client-runtime-vX.Y.Z-android-arm64.spdx.json
tunnel-client-runtime-vX.Y.Z-android-arm64-licenses.txt
```

These files are included in the release's `SHA256SUMS.txt`, public URL
inventory, vulnerability scan, and signed provenance bundle. Follow the
[download verification instructions](../README.md#validate-a-downloaded-release)
with `flavor=runtime` and `platform=android-arm64`. Extract the ZIP in Termux's
private home directory and run `chmod 755 tunnel-client-runtime` if the
extraction tool does not preserve executable permissions.

The ordinary release workflow requires the Android package to build and pass
verification before publishing. Android releases follow the project's
versioned release cadence; there is no separate scheduled release or manual
Android upload step.

## Build with the Android NDK

Use Go 1.27.3, Bash, Make, and the official
[Android NDK r30](https://developer.android.com/ndk/downloads), revision
`30.0.16248370`, on Linux x86-64 or macOS. From a checkout of
[`openai/tunnel-client`](https://github.com/openai/tunnel-client):

```sh
make runtime-android ANDROID_NDK_HOME=/absolute/path/to/android-ndk-r30
go version -m bin/android_arm64/tunnel-client-runtime
```

The target selects the NDK Android ARM64 compiler, enables cgo, builds a PIE
executable, and checks the resulting Go build settings and ELF metadata,
including native resolver linkage and 16 KiB segment alignment. It does not
download tools. Its output is `bin/android_arm64/tunnel-client-runtime`.
The build also updates the ordinary `bin/tunnel-client-runtime` source-build
symlink, so use the platform-specific path when keeping host and Android
builds side by side.

Do not force the `netgo` build tag: that removes native resolver support.
Setting `CGO_ENABLED=1` for a glibc Linux build does not produce an Android
binary. Android adds one runtime package to the release matrix; the full
client and Cloudflare runtime continue to target the existing six platforms.
To rebuild Android, use a full public repository checkout at the release's
recorded commit. The narrow runtime source archives and their rebuild helper
retain their six-platform contract; the scan manifest explicitly records
their `sourceArchivePlatforms` and Android's separate `sourceBuild` checkout.

The **Experimental Android runtime** GitHub Actions workflow runs diagnostic
and build-tooling tests, cross-compiles with a checksum-pinned NDK, and uploads
the same versioned ZIP, license report, SPDX sidecar, and build metadata as
the regular release job. Its workflow artifacts are retained for 14 days;
tagged-release downloads use the normal release retention. These checks
verify compilation and packaging, not execution on an Android device.

To exercise release packaging locally, select the version in
`pkg/version/VERSION` and run:

```sh
ANDROID_NDK_HOME=/absolute/path/to/android-ndk-r30 \
  ./scripts/package_android_runtime.sh \
    --version "v$(cat pkg/version/VERSION)" \
    --source-commit "$(git rev-parse HEAD)" \
    --output-dir dist/public \
    --metadata-dir dist/release-metadata
```

Packaging derives the license report from the actual Android dependency
graph, checks it against the binary's module versions and the checked-in
license catalog, and includes Go/NDK notices. The existing pinned Syft
generator scans the exact staged payload. Release evidence scans this
Android SBOM in addition to the existing 18 platform/flavor SBOMs.

## Build directly in Termux

Use Termux's packaged Go and clang, with a Go version satisfying the checkout's
`go.mod`. From the source checkout:

```sh
pkg install golang clang make
GOTOOLCHAIN=local CC=clang make runtime GOOS=android GOARCH=arm64 CGO_ENABLED=1
go version -m bin/android_arm64/tunnel-client-runtime
```

Confirm `GOOS=android`, `GOARCH=arm64`, and `CGO_ENABLED=1`. The explicit cgo
override is required because the ordinary Make runtime target defaults to
cgo disabled. `GOTOOLCHAIN=local` keeps the installed Termux toolchain; update
that package if it is too old for the module. The module's `go` directive sets
the minimum version, while its `toolchain` directive suggests a toolchain.

## Run and validate

Copy the Android executable into Termux's private home directory, rather than
running it from shared storage. After configuring the normal runtime API key,
tunnel ID, and MCP server environment variables:

```sh
chmod 755 ./tunnel-client-runtime
./tunnel-client-runtime --version
./tunnel-client-runtime run --help
./tunnel-client-runtime run
```

For a short resolver-selection diagnostic run, use
`GODEBUG=netdns=2 ./tunnel-client-runtime run`. Go's debug output can contain
hostnames; sanitize it before sharing. Do not force `netdns=go` when testing
the native resolver. `GODEBUG=netdns=cgo` cannot add native resolver support to
an existing cgo-disabled Linux binary.

Validate metadata retrieval, polling, response delivery, and a complete MCP
request on the actual Termux device. Include Private DNS off/automatic/strict,
applicable VPN/private names, Wi-Fi/mobile handover, and IPv6-only/NAT64. Native
resolution follows the network policy applicable to Termux's UID; it does not
create VPN split-DNS configuration.

Verify HTTPS certificate validation independently. Android's certificate-store
layout can differ from Go's expectations; configure a trusted PEM bundle with
the existing `--ca-bundle` / `CA_BUNDLE` setting if required. Keep certificate
verification enabled.

## DNS failure diagnostics

Control-plane transports emit `control-plane DNS resolution failed` on their
first typed DNS failure. The warning includes bounded `dns_failure`,
`dns_server`, `build_goos`, `build_goarch`, `cgo_enabled`,
`system_resolv_conf`, `android_environment`, and `hint` fields. It omits the
queried hostname, nameserver address, request URL, headers, and credentials.
The existing poll error and health categories, including `network_error`,
retain their behavior. Resolver selection, retries, and HTTP transport policy
are unchanged.

A Linux build with cgo disabled, missing `/etc/resolv.conf`, and a loopback
DNS server on Android indicates the resolver mismatch described in
[issue #92](https://github.com/openai/tunnel-client/issues/92). Use the Android
build above. Installing or editing `$PREFIX/etc/resolv.conf` does not redirect
the stock Linux binary's Go resolver.

The cgo diagnostic describes a build setting, not proof of the currently
selected resolver. Environment settings, build tags, and caller-supplied
transports can affect resolution. A missing resolver file alone is not a
startup error: native resolution, proxies, IP literals, or Unix sockets may
work without it.

## Scope

This experiment covers the plain runtime. Go's SRV, TXT, MX, and NS lookups
still use its pure-Go query path; a successful native hostname lookup does
not establish `cloudflared` discovery compatibility. Companion processes and
MCP subprocesses require separate validation. No public-DNS fallback, resolver
override, or direct Android resolver-socket bridge is installed.

References: [Android DNS Resolver](https://source.android.com/docs/core/ota/modular-system/dns-resolver),
[Go resolver selection](https://go.dev/src/net/conf.go),
[Go Unix DNS lookups](https://go.dev/src/net/lookup_unix.go).
