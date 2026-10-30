#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/android-runtime-package-test.XXXXXX")"
trap 'rm -rf "${work_dir}"' EXIT
project="${work_dir}/project"
tools_dir="${work_dir}/tools"
ndk="${work_dir}/ndk"
mkdir -p "${project}/scripts" "${project}/pkg/version" "${project}/compliance" "${tools_dir}" "${ndk}" "${work_dir}/goroot"
for script in package_android_runtime.sh release_tag.sh build_android_license_report.py verify_release_sbom.sh check_runtime_zip_contents.sh verify_release_archive.sh; do
  cp "${script_dir}/${script}" "${project}/scripts/"
done
printf '0.0.0-test\n' > "${project}/pkg/version/VERSION"
printf 'project license\n' > "${project}/LICENSE"
printf 'project notice\n' > "${project}/NOTICE"
printf 'Go license\n' > "${work_dir}/goroot/LICENSE"
printf 'NDK notice\n' > "${ndk}/NOTICE"
printf 'toolchain notice\n' > "${ndk}/NOTICE.toolchain"
cat > "${project}/compliance/oss-license-report-runtime.txt" <<'EOF'
DEPENDENCY|TYPE|VERSION|LICENSES|LICENSE_FILE|PLATFORMS
example.org/dependency|direct|v1.0.0|MIT|example.org/dependency@v1.0.0/LICENSE|linux/arm64
EOF
cat > "${tools_dir}/make" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == -C ]]
root="$2"
printf '%s\n' "$@" > "${TEST_MAKE_ARGS}"
mkdir -p "${root}/bin/android_arm64"
printf 'Android binary fixture\n' > "${root}/bin/android_arm64/tunnel-client-runtime"
chmod 755 "${root}/bin/android_arm64/tunnel-client-runtime"
EOF
cat > "${tools_dir}/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  version)
    printf 'fixture: go1.27.3\n\tmod\tgithub.com/openai/tunnel-client\t(devel)\n'
    printf '\tdep\texample.org/dependency\tv1.0.0\th1:fixture\n'
    printf '\tbuild\tGOOS=android\n\tbuild\tGOARCH=arm64\n\tbuild\tCGO_ENABLED=1\n'
    ;;
  env)
    case "$2" in
      GOROOT) printf '%s\n' "${TEST_GOROOT}" ;;
      GOVERSION) printf 'go1.27.3\n' ;;
      *) exit 1 ;;
    esac
    ;;
  list)
    [[ "${GOOS}" == android && "${GOARCH}" == arm64 && "${CGO_ENABLED}" == 1 ]]
    [[ "${CC}" == */aarch64-linux-android28-clang ]]
    printf '%s\n' \
      '{"ImportPath":"net","Standard":true}' \
      '{"ImportPath":"example.org/dependency/client","Module":{"Path":"example.org/dependency","Version":"v1.0.0"}}' \
      '{"ImportPath":"github.com/openai/tunnel-client/cmd/client-runtime","Module":{"Path":"github.com/openai/tunnel-client","Main":true}}'
    ;;
  *) exit 1 ;;
esac
EOF
cat > "${project}/scripts/generate_sbom.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == --flavor && "$2" == runtime && "$3" == --staged-dir && "$5" == --output ]]
[[ "${SBOM_SOURCE_VERSION}" == v0.0.0-test ]]
python3 - "$4" "$6" <<'PY'
import hashlib
import json
import pathlib
import sys
stage, output = map(pathlib.Path, sys.argv[1:])
files = [
    {"fileName": path.name, "SPDXID": f"SPDXRef-{index}",
     "checksums": [{"algorithm": "SHA256", "checksumValue": hashlib.sha256(path.read_bytes()).hexdigest()}]}
    for index, path in enumerate(sorted(stage.iterdir()))
]
document = {"spdxVersion": "SPDX-2.3", "name": "tunnel-client-runtime", "files": files,
            "documentDescribes": [file["SPDXID"] for file in files]}
output.write_text(json.dumps(document) + "\n", encoding="utf-8")
PY
EOF
chmod +x "${tools_dir}/make" "${tools_dir}/go" "${project}/scripts/"*.sh
source_commit=0123456789abcdef0123456789abcdef01234567
run_package() {
  env PATH="${tools_dir}:${PATH}" TEST_MAKE_ARGS="${work_dir}/make-args" \
    TEST_GOROOT="${work_dir}/goroot" ANDROID_NDK_HOME="${ndk}" \
    bash "${project}/scripts/package_android_runtime.sh" \
    --version v0.0.0-test --source-commit "${source_commit}" \
    --output-dir "${work_dir}/public" --metadata-dir "${work_dir}/metadata" "$@"
}
run_package > "${work_dir}/output"
grep -Fxq "GIT_SHA=${source_commit}" "${work_dir}/make-args"
grep -Fq "pkg/version.semanticVersion=0.0.0-test" "${work_dir}/make-args"
grep -Fxq 'ANDROID_API=28' "${work_dir}/make-args"
python3 - "${work_dir}" "${source_commit}" <<'PY'
import hashlib
import json
import pathlib
import stat
import sys
import zipfile
root, source_commit = pathlib.Path(sys.argv[1]), sys.argv[2]
stem = "tunnel-client-runtime-v0.0.0-test-android-arm64"
public = root / "public"
assert sorted(path.name for path in public.iterdir()) == sorted([stem + ".zip", stem + ".spdx.json", stem + "-licenses.txt"])
with zipfile.ZipFile(public / (stem + ".zip")) as archive:
    assert len(archive.infolist()) == 5
    assert archive.read(stem + ".spdx.json") == (public / (stem + ".spdx.json")).read_bytes()
    assert stat.S_IMODE(archive.getinfo("tunnel-client-runtime").external_attr >> 16) == 0o755
    notices = archive.read("NOTICE").decode()
    assert all(notice in notices for notice in ("project notice", "Go license", "NDK notice", "toolchain notice"))
    metadata = json.loads((root / "metadata/runtime-android-arm64.json").read_text())
    assert metadata["commitSHA"] == source_commit
    assert metadata["semanticVersion"] == "v0.0.0-test"
    assert metadata["cgoEnabled"] == "1"
    assert metadata["supportStatus"] == "experimental"
    assert metadata["androidToolchain"] == {"ndkRevision": "30.0.16248370", "minimumAPI": 28}
    assert metadata["buildFlags"] == ["-trimpath", "-buildvcs=false", "-buildmode=pie"]
    records = metadata["preSbomPayload"]["files"]
    assert len(records) == 4
    for record in records:
        assert record["sha256"] == hashlib.sha256(archive.read(record["path"])).hexdigest()
    canonical = json.dumps(records, separators=(",", ":"), sort_keys=True).encode()
    assert metadata["preSbomPayload"]["sha256"] == hashlib.sha256(canonical).hexdigest()
    assert metadata["releaseSbom"]["sha256"] == hashlib.sha256(archive.read(stem + ".spdx.json")).hexdigest()
PY
if run_package --version v9.9.9 > "${work_dir}/rejected" 2>&1; then
  echo 'expected mismatched source version to fail' >&2
  exit 1
fi
grep -Fq 'does not match release version' "${work_dir}/rejected"
if run_package --source-commit short > "${work_dir}/rejected" 2>&1; then
  echo 'expected short source commit to fail' >&2
  exit 1
fi
grep -Fq 'must be a full lowercase Git SHA' "${work_dir}/rejected"
printf 'Android runtime release packaging tests passed\n'
