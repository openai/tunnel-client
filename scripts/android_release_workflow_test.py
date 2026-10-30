#!/usr/bin/env python3
"""Exercise the release workflow's actual Android manifest and artifact gates."""

from __future__ import annotations

import hashlib
import io
import json
import os
import pathlib
import subprocess
import tarfile
import tempfile
import unittest
import zipfile


PROJECT_ROOT = pathlib.Path(__file__).resolve().parent.parent
WORKFLOW = PROJECT_ROOT / ".github" / "workflows" / "release.yml"
RELEASE = "v0.0.19-dev"
SOURCE_COMMIT = "a" * 40
PUBLIC_BASE = f"https://persistent.oaistatic.com/tunnel-client/{RELEASE}"
PLATFORMS = tuple(
    f"{goos}-{goarch}"
    for goos in ("linux", "darwin", "windows")
    for goarch in ("amd64", "arm64")
)
FLAVORS = (
    ("client", "tunnel-client"),
    ("runtime", "tunnel-client-runtime"),
    ("runtime-cloudflared", "tunnel-client-runtime-cloudflared"),
)
ANDROID_STEM = f"tunnel-client-runtime-{RELEASE}-android-arm64"


def step_script(name: str) -> str:
    """Extract a named literal run block without adding a YAML dependency."""
    lines = WORKFLOW.read_text(encoding="utf-8").splitlines()
    marker = f"      - name: {name}"
    if lines.count(marker) != 1:
        raise AssertionError(f"expected one workflow step named {name!r}")
    index = lines.index(marker) + 1
    while index < len(lines) and not lines[index].startswith("      - "):
        if lines[index] == "        run: |":
            index += 1
            script = []
            while index < len(lines):
                line = lines[index]
                if line and not line.startswith("          "):
                    break
                script.append(line[10:] if line else "")
                index += 1
            return "\n".join(script).rstrip() + "\n"
        index += 1
    raise AssertionError(f"workflow step {name!r} has no literal run block")


def sha256(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_json(path: pathlib.Path, value: object) -> None:
    path.write_text(json.dumps(value) + "\n", encoding="utf-8")


class AndroidReleaseWorkflowTest(unittest.TestCase):
    def setUp(self) -> None:
        temp = tempfile.TemporaryDirectory(prefix="android-release-workflow.")
        self.addCleanup(temp.cleanup)
        self.root = pathlib.Path(temp.name)
        self.public = self.root / "dist" / "public"
        self.metadata = self.root / "dist" / "release-metadata"
        containers = self.root / "dist" / "container-metadata"
        for directory in (self.public, self.metadata, containers):
            directory.mkdir(parents=True)
        for flavor, prefix in FLAVORS:
            platforms = PLATFORMS + (("android-arm64",) if flavor == "runtime" else ())
            for platform in platforms:
                self.create_payload(flavor, prefix, platform)
        for flavor in ("runtime", "runtime-cloudflared"):
            write_json(
                containers / f"{flavor}.json",
                {
                    "flavor": flavor,
                    "digest": "sha256:" + "b" * 64,
                    "buildkitSbomAttestation": True,
                    "provenanceAttestation": True,
                },
            )
            self.create_source_archive(flavor)
        (self.public / "cloudflared-v2026.1.0-source.tar.gz").write_bytes(b"fixture source")

    def create_payload(self, flavor: str, prefix: str, platform: str) -> None:
        stem = f"{prefix}-{RELEASE}-{platform}"
        goos, goarch = platform.split("-")
        license_path = self.public / f"{stem}-licenses.txt"
        license_path.write_text(f"licenses for {flavor} {platform}\n", encoding="utf-8")
        sbom_path = self.public / f"{stem}.spdx.json"
        write_json(sbom_path, {"spdxVersion": "SPDX-2.3", "name": prefix})
        extension = ".exe" if goos == "windows" else ""
        members = {
            prefix + extension: b"binary fixture",
            "LICENSE": b"license fixture",
            "NOTICE": b"notice fixture",
            license_path.name: license_path.read_bytes(),
            sbom_path.name: sbom_path.read_bytes(),
        }
        if flavor != "runtime":
            members["cloudflared" + extension] = b"companion fixture"
            members["cloudflared-manifest.json"] = b"{}\n"
        with zipfile.ZipFile(self.public / f"{stem}.zip", "w") as archive:
            for name, data in members.items():
                archive.writestr(name, data)
        record = {
            "schemaVersion": 1,
            "flavor": flavor,
            "semanticVersion": RELEASE,
            "commitSHA": SOURCE_COMMIT,
            "goVersion": "go1.27.3",
            "goos": goos,
            "goarch": goarch,
            "cgoEnabled": "1" if goos == "android" else "0",
            "buildFlags": ["-trimpath", "-buildvcs=false"],
            "preSbomPayload": {"sha256": "c" * 64},
            "licenseReport": {"name": license_path.name, "sha256": sha256(license_path)},
            "releaseSbom": {"name": sbom_path.name, "sha256": sha256(sbom_path)},
        }
        if goos == "android":
            record["buildFlags"].append("-buildmode=pie")
            record["supportStatus"] = "experimental"
            record["androidToolchain"] = {"ndkRevision": "30.0.16248370", "minimumAPI": 28}
        if flavor != "runtime":
            record["cloudflaredManifest"] = {"sha256": "d" * 64}
        write_json(self.metadata / f"{flavor}-{platform}.json", record)

    def create_source_archive(self, flavor: str) -> None:
        name = f"tunnel-client-{flavor}-source-{RELEASE}"
        document = {
            "dependencyPackages": {
                platform.replace("-", "/"): ["cmd/client-runtime", "pkg/controlplane"]
                for platform in PLATFORMS
            },
            "excludedDirectoryAssertions": ["cmd/client", "pkg/adminui"],
        }
        data = json.dumps(document).encode()
        with tarfile.open(self.public / f"{name}.tar.gz", "w:gz") as archive:
            member = tarfile.TarInfo(f"{name}/SOURCE_EXPORT_METADATA/manifest.json")
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))

    def run_step(self, name: str, *, public: bool = False, success: bool = True) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            GITHUB_REF_NAME=RELEASE,
            GITHUB_SHA=SOURCE_COMMIT,
            PUBLIC_BASE_URL=PUBLIC_BASE,
            GITHUB_WORKSPACE=str(PROJECT_ROOT),
        )
        result = subprocess.run(
            ["bash", "-euo", "pipefail", "-c", step_script(name)],
            cwd=self.public if public else self.root,
            env=environment,
            text=True,
            capture_output=True,
            check=False,
        )
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, f"{name} unexpectedly accepted the fixture")
        return result

    def generate_and_inventory(self) -> None:
        self.run_step("Generate runtime scan manifests")
        self.run_step("Generate SHA256 checksums", public=True)
        self.run_step("Generate public URLs", public=True)

    def test_nineteen_payloads_pass_actual_workflow_contract(self) -> None:
        self.generate_and_inventory()
        self.run_step("Verify release artifact contract", public=True)
        runtime = json.loads((self.public / f"tunnel-client-runtime-{RELEASE}-scan-manifest.json").read_text())
        self.assertEqual(len(runtime["platformArtifacts"]), 7)
        self.assertEqual(len(runtime["releaseSidecars"]["runtime"]), 7)
        self.assertEqual(len(runtime["sourceArchivePlatforms"]), 6)
        self.assertNotIn("android/arm64", runtime["sourceArchivePlatforms"])
        android = next(record for record in runtime["platformArtifacts"] if record["goos"] == "android")
        self.assertEqual(android["sourceBuild"]["source"], "full-repository-checkout")
        self.assertEqual(android["sourceBuild"]["commitSHA"], SOURCE_COMMIT)

    def test_missing_android_metadata_is_rejected(self) -> None:
        (self.metadata / "runtime-android-arm64.json").unlink()
        result = self.run_step("Generate runtime scan manifests", success=False)
        self.assertIn("platform set differs", result.stderr)

    def test_duplicate_android_metadata_is_rejected(self) -> None:
        original = self.metadata / "runtime-android-arm64.json"
        (self.metadata / "duplicate.json").write_bytes(original.read_bytes())
        result = self.run_step("Generate runtime scan manifests", success=False)
        self.assertIn("platform set differs", result.stderr)

    def test_missing_android_archive_is_rejected(self) -> None:
        (self.public / f"{ANDROID_STEM}.zip").unlink()
        result = self.run_step("Generate runtime scan manifests", success=False)
        self.assertIn("release artifact is missing", result.stderr)

    def test_mismatched_android_sidecar_is_rejected(self) -> None:
        (self.public / f"{ANDROID_STEM}.spdx.json").write_text("{}\n", encoding="utf-8")
        result = self.run_step("Generate runtime scan manifests", success=False)
        self.assertIn("digest mismatch", result.stderr)

    def test_android_without_cgo_is_rejected_by_artifact_contract(self) -> None:
        path = self.metadata / "runtime-android-arm64.json"
        record = json.loads(path.read_text())
        record["cgoEnabled"] = "0"
        write_json(path, record)
        self.generate_and_inventory()
        self.run_step("Verify release artifact contract", public=True, success=False)

    def test_android_wrong_toolchain_is_rejected_by_artifact_contract(self) -> None:
        path = self.metadata / "runtime-android-arm64.json"
        record = json.loads(path.read_text())
        record["androidToolchain"]["minimumAPI"] = 21
        write_json(path, record)
        self.generate_and_inventory()
        self.run_step("Verify release artifact contract", public=True, success=False)

    def test_new_releases_require_android_in_evidence(self) -> None:
        for step in ("Generate release vulnerability and enterprise evidence", "Verify release evidence contract"):
            self.assertIn("--require-android-runtime", step_script(step))
        workflow = WORKFLOW.read_text(encoding="utf-8")
        release_job = workflow.split("\n  release:\n", 1)[1].split("    steps:\n", 1)[0]
        self.assertIn("      - build-android\n", release_job)


if __name__ == "__main__":
    unittest.main()
