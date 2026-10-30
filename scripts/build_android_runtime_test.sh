#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT
project="${work_dir}/project"
tools_dir="${work_dir}/tools"
ndk="${work_dir}/ndk"
mkdir -p "${project}/scripts" "${tools_dir}" "${ndk}"
cp "${script_dir}/build_android_runtime.sh" "${project}/scripts/"
printf 'Pkg.Revision = 30.0.16248370\n' > "${ndk}/source.properties"
case "$(uname -s)" in
  Linux) host_tag=linux-x86_64 ;;
  Darwin) host_tag=darwin-x86_64 ;;
  *) echo 'Android build script tests require Linux or macOS' >&2; exit 1 ;;
esac
ndk_bin="${ndk}/toolchains/llvm/prebuilt/${host_tag}/bin"
mkdir -p "${ndk_bin}"
printf '#!/usr/bin/env bash\nexit 0\n' > "${ndk_bin}/aarch64-linux-android28-clang"
cat > "${tools_dir}/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  list)
    [[ "${GOOS}" == android && "${GOARCH}" == arm64 && "${CGO_ENABLED}" == 1 && -x "${CC}" ]]
    if [[ "${GOFLAGS:-}" != *netgo* ]]; then printf 'cgo\n'; fi
    ;;
  version)
    [[ "$2" == -m && -f "$3" ]]
    printf '\tbuild\tGOOS=%s\n' "${TEST_ARTIFACT_GOOS:-android}"
    printf '\tbuild\tGOARCH=arm64\n\tbuild\tCGO_ENABLED=1\n\tbuild\t-buildmode=pie\n'
    ;;
  *) exit 2 ;;
esac
EOF
cat > "${tools_dir}/make" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" > "${TEST_MAKE_ARGS}"
for arg in "$@"; do
  if [[ "$arg" == RUNTIME_BIN=* ]]; then
    output="${arg#RUNTIME_BIN=}"
    mkdir -p "$(dirname "${output}")"
    printf 'fixture\n' > "${output}"
  fi
done
EOF
cat > "${ndk_bin}/llvm-readelf" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '  Type: DYN (Shared object file)\n  Machine: AArch64\n'
printf '  LOAD 0x0 0x0 0x0 0x100 0x100 R E %s\n' "${TEST_ELF_ALIGNMENT:-0x4000}"
printf '      [Requesting program interpreter: %s]\n' "${TEST_ELF_LINKER:-/system/bin/linker64}"
printf '  0x1 (NEEDED) Shared library: [libc.so]\n'
if [[ "${TEST_NATIVE_RESOLVER:-yes}" == yes ]]; then
  printf '  1: 00000000 0 FUNC GLOBAL DEFAULT UND getaddrinfo@LIBC\n'
fi
EOF
chmod +x "${tools_dir}/go" "${tools_dir}/make" "${ndk_bin}/aarch64-linux-android28-clang" "${ndk_bin}/llvm-readelf"

run_build() {
  env -u ANDROID_NDK_ROOT -u GOFLAGS -u ANDROID_API \
    PATH="${tools_dir}:${PATH}" MAKE="${tools_dir}/make" \
    TEST_MAKE_ARGS="${work_dir}/make-args" ANDROID_NDK_HOME="${ndk}" \
    "$@" bash "${project}/scripts/build_android_runtime.sh"
}

expect_failure() {
  local expected="$1"
  shift
  if run_build "$@" > "${work_dir}/output" 2>&1; then
    echo "expected failure: ${expected}" >&2
    exit 1
  fi
  grep -Fq "${expected}" "${work_dir}/output" || {
    cat "${work_dir}/output" >&2
    echo "missing diagnostic: ${expected}" >&2
    exit 1
  }
}

# Caller target settings cannot accidentally turn this into a Linux/static
# build, and an explicit CC cannot replace the validated NDK compiler.
run_build GOOS=linux GOARCH=amd64 CGO_ENABLED=0 CC=false > "${work_dir}/output"
for arg in GOOS=android GOARCH=arm64 CGO_ENABLED=1 "CC=${ndk_bin}/aarch64-linux-android28-clang" 'RUNTIME_GOFLAGS=-trimpath -buildvcs=false -buildmode=pie'; do
  grep -Fxq -- "${arg}" "${work_dir}/make-args"
done
grep -Fq 'Verified experimental Android runtime:' "${work_dir}/output"
run_build ANDROID_NDK_HOME= ANDROID_NDK_ROOT="${ndk}" > "${work_dir}/output"

expect_failure 'set ANDROID_NDK_HOME' ANDROID_NDK_HOME=
expect_failure 'source.properties is missing' ANDROID_NDK_HOME="${work_dir}/missing"
printf 'Pkg.Revision = 28.2.13676358\n' > "${ndk}/source.properties"
expect_failure 'expected Android NDK r30'
printf 'Pkg.Revision = 30.0.16248370\n' > "${ndk}/source.properties"
expect_failure 'ANDROID_API must be an integer of at least 28' ANDROID_API=27
expect_failure 'ANDROID_API must be an integer of at least 28' ANDROID_API=028
expect_failure 'NDK compiler is missing for ANDROID_API=99' ANDROID_API=99
expect_failure 'requires the native cgo DNS resolver' GOFLAGS=-tags=netgo
expect_failure 'missing required Go build setting GOOS=android' TEST_ARTIFACT_GOOS=linux
expect_failure 'does not use the Android dynamic linker' TEST_ELF_LINKER=/lib/ld-linux-aarch64.so.1
expect_failure 'does not link the native getaddrinfo resolver' TEST_NATIVE_RESOLVER=no
expect_failure 'must support Android 16 KiB pages' TEST_ELF_ALIGNMENT=0x1000
printf 'Android build script tests passed\n'
