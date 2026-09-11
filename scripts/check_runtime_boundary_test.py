"""Exercise the public dependency gate with controlled Go dependency listings."""

from __future__ import annotations

import os
import pathlib
import shutil
import subprocess
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
if [[ $# != 7 || "$1" != list || "$2" != -buildvcs=false ||
      "$3" != -mod=readonly || "$4" != -deps || "$5" != -f ||
      "$6" != '{{.ImportPath}}' || "${GOWORK:-}" != off ||
      "${CGO_ENABLED:-}" != 0 ]]; then
  printf 'unexpected Go invocation\n' >&2
  exit 90
fi
case "$7" in
  ./cmd/client-runtime|./cmd/client-runtime-cloudflared) ;;
  *) exit 91 ;;
esac
if [[ "$7" == "${STUB_FAIL_TARGET}" ]]; then
  printf 'controlled dependency listing failure\n' >&2
  exit 17
fi
cat "${STUB_IMPORTS}"
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
        (self.root / "tmp").mkdir()

    def run_checker(
        self,
        imports: list[str],
        *arguments: str,
        fail_target: str = "",
    ) -> tuple[subprocess.CompletedProcess[str], list[str]]:
        imports_file = self.root / "imports.txt"
        imports_file.write_text("".join(f"{value}\n" for value in imports), encoding="utf-8")
        calls_file = self.root / "calls.txt"
        calls_file.write_text("", encoding="utf-8")
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
            "STUB_FAIL_TARGET": fail_target,
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


if __name__ == "__main__":
    unittest.main()
