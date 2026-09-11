"""Exercise the public dependency gate with controlled Go dependency listings."""

from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPT_DIR = pathlib.Path(__file__).absolute().parent
MODULE = "example.test/runtime.v2"
FLAVORS = ("runtime", "runtime-cloudflared")
PLATFORMS = (
    "linux/amd64",
    "linux/arm64",
    "darwin/amd64",
    "darwin/arm64",
    "windows/amd64",
    "windows/arm64",
)

GO_STUB = r"""#!/usr/bin/env bash
set -euo pipefail
printf '%s\t%s\t%s\n' "${GOOS:-}" "${GOARCH:-}" "$*" >>"${STUB_CALLS}"
if [[ "$*" == 'list -m -f {{.Path}}' ]]; then
  printf '%s\n' 'example.test/runtime.v2'
  exit 0
fi
if [[ "$1" != list || "$2" != -buildvcs=false ||
      "$3" != -mod=readonly || "$4" != -deps || "${GOWORK:-}" != off ||
      "${CGO_ENABLED:-}" != 0 ]]; then
  printf 'unexpected Go invocation\n' >&2
  exit 90
fi
if [[ $# == 7 && "$5" == -f && "$6" == '{{.ImportPath}}' ]]; then
  target="$7"
  listing="${STUB_IMPORTS}"
elif [[ $# == 6 && "$5" == -json ]]; then
  target="$6"
  listing="${STUB_JSON}"
else
  printf 'unexpected Go invocation\n' >&2
  exit 90
fi
case "${target}" in
  ./cmd/client-runtime|./cmd/client-runtime-cloudflared) ;;
  *) exit 91 ;;
esac
if [[ "${target}" == "${STUB_FAIL_TARGET}" &&
      ( -z "${STUB_FAIL_PLATFORM}" || "${GOOS}/${GOARCH}" == "${STUB_FAIL_PLATFORM}" ) ]]; then
  if [[ "$5" == -json ]]; then
    cat "${listing}"
  fi
  printf 'controlled dependency listing failure\n' >&2
  exit 17
fi
cat "${listing}"
"""

PYTHON_STUB = r"""#!/usr/bin/env sh
printf '%s\n' "$*" >>"${STUB_PYTHON_CALLS}"
exec "${STUB_PYTHON}" "$@"
"""


class CheckRuntimeBoundaryTest(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory(dir=os.environ.get("TEST_TMPDIR"))
        self.addCleanup(temporary.cleanup)
        self.root = pathlib.Path(temporary.name)
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        for name in ("check_runtime_boundary.sh", "runtime_runfiles.sh"):
            destination = self.scripts / name
            shutil.copyfile(SCRIPT_DIR / name, destination)
            destination.chmod(0o755)
        (self.root / "go.mod").write_text(f"module {MODULE}\n", encoding="utf-8")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        go = self.bin / "go"
        go.write_text(GO_STUB, encoding="utf-8")
        go.chmod(0o755)
        python = self.bin / "python3"
        python.write_text(PYTHON_STUB, encoding="utf-8")
        python.chmod(0o755)
        (self.root / "tmp").mkdir()

    def run_checker(
        self,
        imports: list[str],
        *arguments: str,
        fail_target: str = "",
        fail_platform: str = "",
        dependency_json: str = "",
    ) -> tuple[subprocess.CompletedProcess[str], list[str]]:
        imports_file = self.root / "imports.txt"
        imports_file.write_text("".join(f"{value}\n" for value in imports), encoding="utf-8")
        calls_file = self.root / "calls.txt"
        calls_file.write_text("", encoding="utf-8")
        json_file = self.root / "dependencies.json"
        json_file.write_text(dependency_json, encoding="utf-8")
        python_calls_file = self.root / "python-calls.txt"
        python_calls_file.write_text("", encoding="utf-8")
        # Exercise the standalone public entrypoint using only the staged scripts.
        # A clean environment keeps host Go configuration and Bazel SDK selection out.
        environment = {
            "PATH": os.pathsep.join((str(self.bin), os.defpath)),
            "LANG": "C",
            "LC_ALL": "C",
            "TMPDIR": str(self.root / "tmp"),
            "GOCACHE": str(self.root / "go-cache"),
            "GOMODCACHE": str(self.root / "go-mod-cache"),
            "STUB_CALLS": str(calls_file),
            "STUB_IMPORTS": str(imports_file),
            "STUB_JSON": str(json_file),
            "STUB_FAIL_TARGET": fail_target,
            "STUB_FAIL_PLATFORM": fail_platform,
            "STUB_PYTHON": sys.executable,
            "STUB_PYTHON_CALLS": str(python_calls_file),
        }
        result = subprocess.run(
            ["bash", str(self.scripts / "check_runtime_boundary.sh"), *arguments],
            cwd=self.root,
            env=environment,
            capture_output=True,
            text=True,
            check=False,
            timeout=10,
        )
        if "--dependency-json-dir" not in arguments:
            self.assertEqual(python_calls_file.read_text(encoding="utf-8"), "")
        return result, calls_file.read_text(encoding="utf-8").splitlines()

    def assert_denied(self, flavor: str, path: str, reason: str) -> None:
        result, calls = self.run_checker(
            [MODULE, f"{MODULE}/pkg/transport", f"{MODULE}/{path}", f"{MODULE}/pkg/config"],
            "--flavor",
            flavor,
            "--platform",
            "linux/amd64",
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(result.stdout, "")
        self.assertEqual(
            result.stderr,
            "check_runtime_boundary.sh: "
            f"{flavor} dependency boundary failed for linux/amd64: {path} ({reason})\n",
        )
        self.assertEqual(len(calls), 2)

    def test_common_exclusions_exact_and_nested(self) -> None:
        exclusions = {
            "pkg/app": "full application wiring",
            "pkg/config": "full configuration",
            "pkg/health": "full health surface",
            "pkg/harpoon": "full Harpoon adapter",
            "pkg/proxyhealth": "full proxy health surface",
            "cmd/client": "full command tree",
        }
        for flavor in FLAVORS:
            for path, reason in exclusions.items():
                for suffix in ("", "/nested"):
                    with self.subTest(flavor=flavor, path=path + suffix):
                        self.assert_denied(flavor, path + suffix, reason)

    def test_support_and_codex_components(self) -> None:
        support = (
            "adminui",
            "plugins",
            "localproxy",
            "docs",
            "examples",
            "e2e",
            "tests",
            "testdata",
            "testsupport",
        )
        paths = [(f"pkg/{part}/nested", "support or development surface") for part in support]
        paths += [
            ("docs", "support or development surface"),
            ("codex", "Codex surface"),
            ("pkg/codex-agent/nested", "Codex surface"),
        ]
        for flavor in FLAVORS:
            for path, reason in paths:
                with self.subTest(flavor=flavor, path=path):
                    self.assert_denied(flavor, path, reason)

    def test_companion_policy_for_both_flavors(self) -> None:
        for flavor in FLAVORS:
            for path in (
                "pkg/cloudflared",
                "pkg/cloudflared/configgen",
                "pkg/cloudflared/runtimeextra",
                "pkg/cloudflared/runtime",
                "pkg/cloudflared/runtime/nested",
            ):
                with self.subTest(flavor=flavor, path=path):
                    if flavor == "runtime-cloudflared" and path in (
                        "pkg/cloudflared/runtime",
                        "pkg/cloudflared/runtime/nested",
                    ):
                        result, _ = self.run_checker(
                            [f"{MODULE}/{path}"],
                            "--flavor",
                            flavor,
                            "--platform",
                            "linux/amd64",
                        )
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertEqual(result.stderr, "")
                        self.assertEqual(
                            result.stdout, f"{flavor} dependency boundary: linux/amd64 passed\n"
                        )
                    else:
                        reason = (
                            "companion package"
                            if flavor == "runtime"
                            else "unapproved companion package"
                        )
                        self.assert_denied(flavor, path, reason)

    def test_allowed_near_misses_and_nonfirstparty_imports(self) -> None:
        paths = (
            "pkg/application",
            "pkg/configuration",
            "pkg/healthful",
            "pkg/harpooning",
            "pkg/proxyhealthy",
            "cmd/client-runtime",
            "pkg/cloudflaredness",
            "pkg/mydocs",
            "pkg/testsupporting",
            "pkg/xcodex",
            "pkg/Docs",
            "other/pkg/app",
        )
        imports = [MODULE, "foreign.test/pkg/app", MODULE + "-other/pkg/config"]
        imports += [f"{MODULE}/{path}" for path in paths]
        for flavor in FLAVORS:
            with self.subTest(flavor=flavor):
                result, calls = self.run_checker(
                    imports,
                    "--flavor",
                    flavor,
                    "--platform",
                    "windows/arm64",
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, "")
                self.assertEqual(
                    result.stdout, f"{flavor} dependency boundary: windows/arm64 passed\n"
                )
                self.assertEqual(len(calls), 2)
                self.assertTrue(calls[1].startswith("windows\tarm64\t"), calls)

    def test_defaults_check_both_flavors_on_every_release_platform(self) -> None:
        result, calls = self.run_checker([MODULE, f"{MODULE}/pkg/transport"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        self.assertEqual(
            result.stdout,
            "".join(
                f"{flavor} dependency boundary: {platform} passed\n"
                for flavor in FLAVORS
                for platform in PLATFORMS
            ),
        )
        expected_calls = ["\t\tlist -m -f {{.Path}}"]
        for flavor in FLAVORS:
            for platform in PLATFORMS:
                goos, goarch = platform.split("/")
                expected_calls.append(
                    f"{goos}\t{goarch}\tlist -buildvcs=false -mod=readonly "
                    f"-deps -f {{{{.ImportPath}}}} ./cmd/client-{flavor}"
                )
        self.assertEqual(calls, expected_calls)

    def test_listing_failure_is_fatal_for_each_flavor(self) -> None:
        for flavor in FLAVORS:
            with self.subTest(flavor=flavor):
                result, calls = self.run_checker(
                    [],
                    "--flavor",
                    flavor,
                    "--platform",
                    "linux/amd64",
                    fail_target=f"./cmd/client-{flavor}",
                )
                self.assertEqual(result.returncode, 1)
                self.assertEqual(result.stdout, "")
                self.assertEqual(
                    result.stderr,
                    "controlled dependency listing failure\n"
                    "check_runtime_boundary.sh: "
                    f"{flavor} dependency listing failed for linux/amd64\n",
                )
                self.assertEqual(len(calls), 2)

    def assert_json_calls(self, calls: list[str], flavor: str, platforms: tuple[str, ...]) -> None:
        expected = ["\t\tlist -m -f {{.Path}}"]
        for platform in platforms:
            goos, goarch = platform.split("/")
            expected.append(
                f"{goos}\t{goarch}\tlist -buildvcs=false -mod=readonly "
                f"-deps -json ./cmd/client-{flavor}"
            )
        self.assertEqual(calls, expected)

    def test_dependency_json_preserves_raw_listings_for_all_platforms(self) -> None:
        records = [
            {"ImportPath": "fmt", "Standard": True},
            {"ImportPath": MODULE, "Module": {"Path": "foreign.test"}},
            {"ImportPath": ""},
            {"ImportPath": f"{MODULE}/pkg/transport"},
            {"ImportPath": "foreign.test/pkg/app", "Module": {"Path": MODULE}},
        ]
        for flavor in FLAVORS:
            with self.subTest(flavor=flavor):
                selected_records = records.copy()
                if flavor == "runtime-cloudflared":
                    selected_records.append({"ImportPath": f"{MODULE}/pkg/cloudflared/runtime"})
                raw = " \r\n" + "\r\n\t".join(
                    json.dumps(record, indent=2) for record in selected_records
                )
                output = self.root / f"published {flavor}"
                self.assertFalse(output.exists())
                result, calls = self.run_checker(
                    [],
                    "--flavor",
                    flavor,
                    "--dependency-json-dir",
                    str(output),
                    dependency_json=raw,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, "")
                self.assertEqual(
                    result.stdout,
                    "".join(
                        f"{flavor} dependency boundary: {platform} passed\n"
                        for platform in PLATFORMS
                    ),
                )
                self.assert_json_calls(calls, flavor, PLATFORMS)
                self.assertEqual(
                    sorted(path.name for path in output.iterdir()),
                    sorted(platform.replace("/", "_") + ".json" for platform in PLATFORMS),
                )
                for platform in PLATFORMS:
                    self.assertEqual(
                        (output / (platform.replace("/", "_") + ".json")).read_bytes(),
                        raw.encode("utf-8"),
                    )

    def test_dependency_json_checks_import_paths_and_preserves_denial_priority(self) -> None:
        for flavor in FLAVORS:
            cases = (
                ("pkg/config", "full configuration", {"Module": {"Path": "foreign.test"}}),
                ("pkg/codex-agent/docs", "support or development surface", {}),
                (
                    "pkg/cloudflared/configgen",
                    "companion package" if flavor == "runtime" else "unapproved companion package",
                    {"Module": {"Path": "foreign.test"}},
                ),
            )
            for index, (path, reason, metadata) in enumerate(cases):
                with self.subTest(flavor=flavor, path=path):
                    # Module metadata must not hide the first denied ImportPath or
                    # turn a foreign ImportPath into a first-party dependency.
                    raw = "\n".join(
                        json.dumps(record)
                        for record in (
                            {"ImportPath": "foreign.test/pkg/app", "Module": {"Path": MODULE}},
                            {"ImportPath": f"{MODULE}/{path}", **metadata},
                            {"ImportPath": f"{MODULE}/pkg/app", "Module": {"Path": MODULE}},
                        )
                    )
                    output = self.root / f"denied-{flavor}-{index}"
                    result, calls = self.run_checker(
                        [],
                        "--flavor",
                        flavor,
                        "--platform",
                        "windows/arm64",
                        "--dependency-json-dir",
                        str(output),
                        dependency_json=raw,
                    )
                    self.assertEqual(result.returncode, 1, result.stderr)
                    self.assertEqual(result.stdout, "")
                    self.assertEqual(
                        result.stderr,
                        "check_runtime_boundary.sh: "
                        f"{flavor} dependency boundary failed for windows/arm64: "
                        f"{path} ({reason})\n",
                    )
                    self.assert_json_calls(calls, flavor, ("windows/arm64",))
                    self.assertFalse((output / "windows_arm64.json").exists())

    def test_dependency_json_rejects_invalid_records_without_publication(self) -> None:
        invalid = (
            ("{", "go list output is not valid JSON"),
            ('{"ImportPath":"fmt"}\n{', "go list output is not valid JSON"),
            ("[]", "go list output record is not an object"),
            ("null", "go list output record is not an object"),
            ("{}", "go list output record is missing ImportPath"),
            ('{"ImportPath":null}', "go list output record is missing ImportPath"),
            ('{"ImportPath":17}', "go list output record is missing ImportPath"),
        )
        for index, (raw, diagnostic) in enumerate(invalid):
            with self.subTest(raw=raw):
                output = self.root / f"invalid-{index}"
                result, calls = self.run_checker(
                    [],
                    "--flavor",
                    "runtime",
                    "--platform",
                    "linux/amd64",
                    "--dependency-json-dir",
                    str(output),
                    dependency_json=raw,
                )
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(result.stdout, "")
                self.assertEqual(
                    result.stderr,
                    f"{diagnostic}\ncheck_runtime_boundary.sh: "
                    "runtime dependency JSON is invalid for linux/amd64\n",
                )
                self.assert_json_calls(calls, "runtime", ("linux/amd64",))
                self.assertFalse((output / "linux_amd64.json").exists())

    def test_dependency_json_listing_failure_keeps_only_successful_platforms(self) -> None:
        raw = json.dumps({"ImportPath": f"{MODULE}/pkg/transport"})
        for flavor in FLAVORS:
            for fail_platform in ("linux/amd64", "linux/arm64"):
                with self.subTest(flavor=flavor, fail_platform=fail_platform):
                    output = self.root / f"failed-{flavor}-{fail_platform.replace('/', '_')}"
                    result, calls = self.run_checker(
                        [],
                        "--flavor",
                        flavor,
                        "--dependency-json-dir",
                        str(output),
                        dependency_json=raw,
                        fail_target=f"./cmd/client-{flavor}",
                        fail_platform=fail_platform,
                    )
                    self.assertEqual(result.returncode, 1, result.stderr)
                    self.assertEqual(
                        result.stderr,
                        "controlled dependency listing failure\ncheck_runtime_boundary.sh: "
                        f"{flavor} dependency listing failed for {fail_platform}\n",
                    )
                    passed = () if fail_platform == "linux/amd64" else ("linux/amd64",)
                    self.assertEqual(
                        result.stdout,
                        "".join(
                            f"{flavor} dependency boundary: {platform} passed\n"
                            for platform in passed
                        ),
                    )
                    self.assert_json_calls(calls, flavor, (*passed, fail_platform))
                    self.assertEqual(
                        [
                            platform.replace("/", "_") + ".json"
                            for platform in PLATFORMS
                            if (output / (platform.replace("/", "_") + ".json")).exists()
                        ],
                        [platform.replace("/", "_") + ".json" for platform in passed],
                    )
                    for platform in passed:
                        self.assertEqual(
                            (output / (platform.replace("/", "_") + ".json")).read_bytes(),
                            raw.encode("utf-8"),
                        )

    def test_dependency_json_rejects_invalid_destinations_and_options(self) -> None:
        existing = self.root / "existing"
        existing.mkdir()
        sentinel = existing / "unchanged"
        sentinel.write_text("retain this file", encoding="utf-8")
        link = self.root / "link"
        link.symlink_to(existing, target_is_directory=True)
        dangling = self.root / "dangling"
        dangling.symlink_to(self.root / "absent", target_is_directory=True)
        new = self.root / "new"
        cases = [
            (("--dependency-json-dir",), "--dependency-json-dir requires a directory"),
            (("--dependency-json-dir", ""), "--dependency-json-dir requires a directory"),
            (
                ("--dependency-json-dir", str(new)),
                "--dependency-json-dir requires one explicit flavor",
            ),
            (
                ("--flavor", "all", "--dependency-json-dir", str(new)),
                "--dependency-json-dir requires one explicit flavor",
            ),
            (
                ("--flavor", "runtime", "--binary", "artifact", "--dependency-json-dir", str(new)),
                "--dependency-json-dir cannot be combined with --binary",
            ),
            (
                ("--flavor", "runtime", "--dependency-json-dir", "relative"),
                "--dependency-json-dir must be absolute",
            ),
        ]
        cases.extend(
            (
                ("--flavor", "runtime", "--dependency-json-dir", str(destination)),
                "--dependency-json-dir must not already exist",
            )
            for destination in (existing, sentinel, link, dangling)
        )
        for arguments, diagnostic in cases:
            with self.subTest(arguments=arguments):
                result, calls = self.run_checker([], *arguments)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(result.stdout, "")
                self.assertEqual(result.stderr, f"check_runtime_boundary.sh: {diagnostic}\n")
                self.assertEqual(calls, [])
                self.assertFalse(new.exists())
                self.assertEqual(sentinel.read_text(encoding="utf-8"), "retain this file")
                self.assertTrue(link.is_symlink())
                self.assertTrue(dangling.is_symlink())

        missing_parent = self.root / "missing-parent" / "output"
        result, calls = self.run_checker(
            [], "--flavor", "runtime", "--dependency-json-dir", str(missing_parent)
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(result.stdout, "")
        self.assertTrue(
            result.stderr.endswith(
                "check_runtime_boundary.sh: "
                f"could not create dependency JSON directory: {missing_parent}\n"
            ),
            result.stderr,
        )
        self.assertEqual(calls, ["\t\tlist -m -f {{.Path}}"])
        self.assertFalse(missing_parent.exists())


if __name__ == "__main__":
    unittest.main()
