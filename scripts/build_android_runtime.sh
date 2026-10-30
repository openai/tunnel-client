#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly NDK_REVISION="30.0.16248370"

die() {
  echo "build_android_runtime.sh: $*" >&2
  exit 1
}

if [[ $# -ne 0 ]]; then
  die "usage: ANDROID_NDK_HOME=/path/to/android-ndk-r30 make runtime-android [ANDROID_API=28]"
fi

ndk="${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-}}"
[[ -n "${ndk}" ]] || die "set ANDROID_NDK_HOME (or ANDROID_NDK_ROOT) to an installed Android NDK r30 (${NDK_REVISION})"
[[ -f "${ndk}/source.properties" ]] || die "Android NDK source.properties is missing; check ANDROID_NDK_HOME"
revision="$(sed -n 's/^Pkg.Revision[[:space:]]*=[[:space:]]*//p' "${ndk}/source.properties" | tr -d '\r')"
[[ "${revision}" == "${NDK_REVISION}" ]] || die "expected Android NDK r30 (${NDK_REVISION}); found ${revision:-unknown}"
ndk="$(cd "${ndk}" && pwd)"

api="${ANDROID_API:-28}"
[[ "${api}" =~ ^[1-9][0-9]$ ]] && (( api >= 28 )) || die "ANDROID_API must be an integer of at least 28 supported by NDK r30"
case "$(uname -s)" in
  Linux) host_tag=linux-x86_64 ;;
  Darwin) host_tag=darwin-x86_64 ;;
  *) die "NDK cross-builds are supported on Linux and macOS; see docs/development.md for native Termux builds" ;;
esac
toolchain="${ndk}/toolchains/llvm/prebuilt/${host_tag}"
compiler="${toolchain}/bin/aarch64-linux-android${api}-clang"
readelf="${toolchain}/bin/llvm-readelf"
[[ -x "${compiler}" ]] || die "NDK compiler is missing for ANDROID_API=${api}: ${compiler}"
[[ -x "${readelf}" ]] || die "NDK llvm-readelf is missing: ${readelf}"
command -v go >/dev/null 2>&1 || die "go is required"

# Select the same native resolver that the build below will use. This also
# catches netgo selected through GOFLAGS or a persistent `go env -w` setting.
native_resolver="$(cd "${PROJECT_ROOT}" && GOOS=android GOARCH=arm64 CGO_ENABLED=1 CC="${compiler}" go list -f '{{if .CgoFiles}}cgo{{end}}' net)"
[[ "${native_resolver}" == "cgo" ]] || die "the Android build requires the native cgo DNS resolver; remove netgo from GOFLAGS/go env settings"

output="${PROJECT_ROOT}/bin/android_arm64/tunnel-client-runtime"
printf 'Building experimental Android/arm64 runtime: NDK %s, API %s, cgo enabled\n' "${NDK_REVISION}" "${api}"
"${MAKE:-make}" -C "${PROJECT_ROOT}" runtime \
  GOOS=android GOARCH=arm64 GOARM= CGO_ENABLED=1 CC="${compiler}" \
  RUNTIME_BIN="${output}" \
  RUNTIME_GOFLAGS='-trimpath -buildvcs=false -buildmode=pie'

# A successful compiler invocation alone does not prove that the artifact is
# usable on Android. Keep these checks in the local build and CI entry point.
build_info="$(go version -m "${output}")"
for setting in 'GOOS=android' 'GOARCH=arm64' 'CGO_ENABLED=1' '-buildmode=pie'; do
  grep -Eq "build[[:space:]]+${setting}$" <<< "${build_info}" || die "artifact is missing required Go build setting ${setting}"
done
elf_info="$("${readelf}" --wide --file-header --program-headers --dynamic --dyn-syms "${output}")"
grep -Eq 'Machine:[[:space:]]+AArch64' <<< "${elf_info}" || die "artifact is not an AArch64 ELF"
grep -Eq 'Type:[[:space:]]+DYN' <<< "${elf_info}" || die "artifact is not position-independent ELF"
grep -Fq '/system/bin/linker64' <<< "${elf_info}" || die "artifact does not use the Android dynamic linker"
grep -Eq 'NEEDED.*\[libc\.so\]' <<< "${elf_info}" || die "artifact is not linked to Android Bionic libc.so"
grep -Eq 'UND[[:space:]]+getaddrinfo(@|[[:space:]]|$)' <<< "${elf_info}" || die "artifact does not link the native getaddrinfo resolver"
alignments="$(awk '$1 == "LOAD" { print $NF }' <<< "${elf_info}")"
[[ -n "${alignments}" ]] || die "artifact has no ELF LOAD segments"
while IFS= read -r alignment; do
  [[ "${alignment}" =~ ^0x[0-9a-fA-F]+$ ]] && (( alignment >= 16384 )) || die "artifact LOAD segments must support Android 16 KiB pages"
done <<< "${alignments}"
printf 'Verified experimental Android runtime: %s\n' "${output}"
