from __future__ import annotations

import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name("prepare_sbom_source.py")
LEGACY = "github.com/openai/tunnel-client"
CANONICAL = "github.com/openai/tunnel-client"
CLOUDFLARED_COMMIT = "8679787525edc8575b2948a7c4a50b6292c6d426"
CLOUDFLARED_MODULE_PATH = "github.com/cloudflare/cloudflared"
CLOUDFLARED_MODULE_VERSION = "v0.0.0-20260715110107-8679787525ed"


class PrepareSbomSourceTest(unittest.TestCase):
    def run_script(
        self,
        test_tmpdir: pathlib.Path,
        *arguments: str,
    ) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update({"BAZEL_TEST": "1", "TEST_TMPDIR": str(test_tmpdir)})
        return subprocess.run(
            [sys.executable, str(SCRIPT), *arguments],
            check=False,
            capture_output=True,
            text=True,
            env=environment,
        )

    def test_canonicalizes_only_staged_source_files(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = pathlib.Path(temporary_directory)
            source_root = root / "source"
            (source_root / "cmd" / "client").mkdir(parents=True)
            (source_root / "go.mod").write_text(
                f"module {LEGACY}\n",
                encoding="utf-8",
            )
            source_file = source_root / "cmd" / "client" / "main.go"
            source_file.write_text(
                f'import "{LEGACY}/pkg/version"\n',
                encoding="utf-8",
            )

            result = self.run_script(
                root,
                "canonicalize",
                "--source-root",
                str(source_root),
                "--legacy-module-path",
                LEGACY,
                "--canonical-module-path",
                CANONICAL,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                (source_root / "go.mod").read_text(encoding="utf-8"),
                f"module {CANONICAL}\n",
            )
            self.assertIn(CANONICAL, source_file.read_text(encoding="utf-8"))
            self.assertNotIn(LEGACY, source_file.read_text(encoding="utf-8"))

    def test_prints_cloudflared_metadata(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = pathlib.Path(temporary_directory)
            source_root = root / f"cloudflared-{CLOUDFLARED_COMMIT}"
            source_root.mkdir()
            (source_root / "go.mod").write_text(
                f"module {CLOUDFLARED_MODULE_PATH}\n",
                encoding="utf-8",
            )
            manifest = root / "manifest.json"
            manifest.write_text(
                json.dumps(
                    {
                        "version": "2026.7.2",
                        "build_time": "2026-07-15T11:01:07Z",
                        "release_commit": CLOUDFLARED_COMMIT,
                        "module_path": CLOUDFLARED_MODULE_PATH,
                        "package_path": f"{CLOUDFLARED_MODULE_PATH}/cmd/cloudflared",
                        "module_version": CLOUDFLARED_MODULE_VERSION,
                    }
                ),
                encoding="utf-8",
            )

            result = self.run_script(
                root,
                "cloudflared-metadata",
                "--manifest",
                str(manifest),
                "--source-root",
                str(source_root),
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                result.stdout,
                (
                    "2026.7.2\t2026-07-15T11:01:07Z\t"
                    "github.com/cloudflare/cloudflared\t"
                    "v0.0.0-20260715110107-8679787525ed\n"
                ),
            )

    def test_rejects_cloudflared_manifest_source_identity_mismatches(self) -> None:
        cases = (
            (
                "release_commit",
                {"release_commit": "0" * 40},
                "release_commit",
            ),
            (
                "module_path",
                {"module_path": "github.com/cloudflare/not-cloudflared"},
                "module_path",
            ),
            (
                "module_version",
                {"module_version": "v0.0.0-20260715110107-000000000000"},
                "module_version",
            ),
        )
        for name, overrides, expected_error in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary_directory:
                root = pathlib.Path(temporary_directory)
                source_root = root / f"cloudflared-{CLOUDFLARED_COMMIT}"
                source_root.mkdir()
                (source_root / "go.mod").write_text(
                    f"module {CLOUDFLARED_MODULE_PATH}\n",
                    encoding="utf-8",
                )
                manifest_data = {
                    "version": "2026.7.2",
                    "build_time": "2026-07-15T11:01:07Z",
                    "release_commit": CLOUDFLARED_COMMIT,
                    "module_path": CLOUDFLARED_MODULE_PATH,
                    "package_path": f"{CLOUDFLARED_MODULE_PATH}/cmd/cloudflared",
                    "module_version": CLOUDFLARED_MODULE_VERSION,
                }
                manifest_data.update(overrides)
                manifest = root / "manifest.json"
                manifest.write_text(json.dumps(manifest_data), encoding="utf-8")

                result = self.run_script(
                    root,
                    "cloudflared-metadata",
                    "--manifest",
                    str(manifest),
                    "--source-root",
                    str(source_root),
                )

                self.assertNotEqual(result.returncode, 0)
                self.assertIn(expected_error, result.stderr)

    def test_rejects_paths_outside_test_tmpdir(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = pathlib.Path(temporary_directory)
            test_tmpdir = root / "test-tmpdir"
            test_tmpdir.mkdir()
            source_root = root / "outside"
            source_root.mkdir()

            result = self.run_script(
                test_tmpdir,
                "canonicalize",
                "--source-root",
                str(source_root),
                "--legacy-module-path",
                LEGACY,
                "--canonical-module-path",
                CANONICAL,
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn("TEST_TMPDIR", result.stderr)


if __name__ == "__main__":
    unittest.main()
