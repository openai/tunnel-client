#!/usr/bin/env python3
"""Regression tests for release vulnerability and enterprise evidence tooling."""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

SCRIPT_DIR = pathlib.Path(__file__).resolve().parent
if (SCRIPT_DIR / "scripts" / "verify_release_evidence.sh").is_file():
    SCRIPT_DIR = SCRIPT_DIR / "scripts"
GENERATOR = SCRIPT_DIR / "generate_release_evidence.py"
VERIFIER = SCRIPT_DIR / "verify_release_evidence.sh"
RELEASE = "v0.0.0"
SOURCE_DIGEST = "0" * 40
GENERATED_AT = "2026-01-02T03:04:05Z"
PLATFORMS = (
    "darwin-amd64",
    "darwin-arm64",
    "linux-amd64",
    "linux-arm64",
    "windows-amd64",
    "windows-arm64",
)
FLAVORS = (
    ("client", "tunnel-client"),
    ("runtime", "tunnel-client-runtime"),
    ("runtime-cloudflared", "tunnel-client-runtime-cloudflared"),
)


def fail(message: str) -> None:
    raise SystemExit(f"release_evidence_test.py: {message}")


def run(
    command: list[str], *, env: dict[str, str] | None = None
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, check=False, capture_output=True, text=True, env=env)


def must_run(command: list[str], *, env: dict[str, str] | None = None) -> None:
    result = run(command, env=env)
    if result.returncode != 0:
        fail(
            f"command failed ({result.returncode}): {' '.join(command)}\n"
            f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
        )


def sha256(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_fake_grype(path: pathlib.Path) -> None:
    path.write_text(
        """#!/usr/bin/env python3
import json
import os
import sys

if len(sys.argv) >= 2 and sys.argv[1] == "version":
    print("Application: grype")
    print("Version: 0.117.0")
    raise SystemExit(0)
if len(sys.argv) != 4 or not sys.argv[1].startswith("sbom:") or sys.argv[2:] != ["--output", "json"]:
    raise SystemExit(90)
matches = []
if os.environ.get("FAKE_GRYPE_MODE", "findings") != "zero":
    matches = [
        {
            "vulnerability": {"id": "CVE-2026-0001", "severity": "High"},
            "relatedVulnerabilities": [{"id": "GHSA-test-0001"}],
            "artifact": {
                "name": "example.org/dependency",
                "version": "v1.0.0",
                "purl": "pkg:golang/example.org/dependency@v1.0.0",
            },
        }
    ]
payload = {
    "descriptor": {
        "name": "grype",
        "version": "0.117.0",
        "db": {
            "status": {
                "schemaVersion": "v6.1.9",
                "from": "https://grype.example.test/db.tar.zst?checksum=sha256:0000000000000000000000000000000000000000000000000000000000000000",
                "built": "2026-01-01T00:00:00Z",
                "valid": True,
            }
        },
    },
    "source": {"type": "directory", "target": "fixture"},
    "distro": {"name": "", "version": "", "idLike": None},
    "matches": matches,
    "ignoredMatches": [],
}
print(json.dumps(payload))
""",
        encoding="utf-8",
    )
    path.chmod(0o755)


def create_inputs(artifact_dir: pathlib.Path) -> None:
    shutil.rmtree(artifact_dir, ignore_errors=True)
    artifact_dir.mkdir(parents=True)
    for flavor, prefix in FLAVORS:
        for platform in PLATFORMS:
            stem = f"{prefix}-{RELEASE}-{platform}"
            (artifact_dir / f"{stem}.zip").write_text(
                f"zip {flavor} {platform}\n", encoding="utf-8"
            )
            (artifact_dir / f"{stem}.spdx.json").write_text(
                json.dumps({"spdxVersion": "SPDX-2.3", "name": stem}) + "\n",
                encoding="utf-8",
            )
            (artifact_dir / f"{stem}-licenses.txt").write_text(
                f"licenses {stem}\n", encoding="utf-8"
            )


def generate(artifact_dir: pathlib.Path, grype: pathlib.Path, mode: str = "findings") -> None:
    env = os.environ.copy()
    env["FAKE_GRYPE_MODE"] = mode
    must_run(
        [
            sys.executable,
            str(GENERATOR),
            "--release",
            RELEASE,
            "--source-digest",
            SOURCE_DIGEST,
            "--artifact-dir",
            str(artifact_dir),
            "--grype",
            str(grype),
            "--generated-at",
            GENERATED_AT,
        ],
        env=env,
    )


def write_inventories(artifact_dir: pathlib.Path) -> None:
    bundle_name = f"tunnel-client-{RELEASE}-provenance.sigstore.json"
    excluded = {"SHA256SUMS.txt", "PUBLIC_URLS.txt", bundle_name}
    files = sorted(
        path
        for path in artifact_dir.iterdir()
        if path.is_file() and not path.is_symlink() and path.name not in excluded
    )
    (artifact_dir / "SHA256SUMS.txt").write_text(
        "".join(f"{sha256(path)}  {path.name}\n" for path in files),
        encoding="utf-8",
    )
    url_names = [path.name for path in files] + ["SHA256SUMS.txt", bundle_name]
    (artifact_dir / "PUBLIC_URLS.txt").write_text(
        "".join(
            f"https://persistent.oaistatic.com/tunnel-client/{RELEASE}/{name}\n"
            for name in url_names
        ),
        encoding="utf-8",
    )
    (artifact_dir / bundle_name).write_text('{"bundle":"fixture"}\n', encoding="utf-8")


def refresh_manifest_vex_digest(artifact_dir: pathlib.Path) -> None:
    vex_path = artifact_dir / f"tunnel-client-{RELEASE}.openvex.json"
    manifest_path = artifact_dir / f"tunnel-client-{RELEASE}-enterprise-evidence.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    vex_digest = sha256(vex_path)
    for artifact in manifest["artifacts"]:
        if artifact["name"] == vex_path.name:
            artifact["sha256"] = vex_digest
    manifest["vulnerabilityAssessment"]["openVex"]["sha256"] = vex_digest
    manifest_path.write_text(json.dumps(manifest) + "\n", encoding="utf-8")


def verifier_result(artifact_dir: pathlib.Path) -> subprocess.CompletedProcess[str]:
    return run(
        [
            "bash",
            str(VERIFIER),
            "--artifact-dir",
            str(artifact_dir),
            "--release",
            RELEASE,
            "--source-digest",
            SOURCE_DIGEST,
        ]
    )


def assert_rejected(artifact_dir: pathlib.Path, expected: str) -> None:
    result = verifier_result(artifact_dir)
    if result.returncode == 0:
        fail("expected invalid release evidence fixture to fail")
    output = result.stdout + result.stderr
    if expected not in output:
        fail(f"expected failure containing {expected!r}, got:\n{output}")


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="tunnel-client-release-evidence-test.") as tmp:
        root = pathlib.Path(tmp)
        artifact_dir = root / "artifacts"
        grype = root / "grype"
        write_fake_grype(grype)

        create_inputs(artifact_dir)
        generate(artifact_dir, grype)
        write_inventories(artifact_dir)
        must_run(
            [
                "bash",
                str(VERIFIER),
                "--artifact-dir",
                str(artifact_dir),
                "--release",
                RELEASE,
                "--source-digest",
                SOURCE_DIGEST,
            ]
        )

        vex_path = artifact_dir / f"tunnel-client-{RELEASE}.openvex.json"
        vex = json.loads(vex_path.read_text(encoding="utf-8"))
        vex["statements"][0]["status"] = "not_affected"
        vex_path.write_text(json.dumps(vex) + "\n", encoding="utf-8")
        refresh_manifest_vex_digest(artifact_dir)
        write_inventories(artifact_dir)
        assert_rejected(artifact_dir, "OpenVEX statements must use only under_investigation")

        create_inputs(artifact_dir)
        generate(artifact_dir, grype)
        write_inventories(artifact_dir)
        vex_path = artifact_dir / f"tunnel-client-{RELEASE}.openvex.json"
        vex = json.loads(vex_path.read_text(encoding="utf-8"))
        vex["statements"][0]["products"].pop()
        vex_path.write_text(json.dumps(vex) + "\n", encoding="utf-8")
        refresh_manifest_vex_digest(artifact_dir)
        write_inventories(artifact_dir)
        assert_rejected(artifact_dir, "OpenVEX statement products do not match scan findings")

        create_inputs(artifact_dir)
        generate(artifact_dir, grype)
        write_inventories(artifact_dir)
        checksums = artifact_dir / "SHA256SUMS.txt"
        checksums.write_text(
            "\n".join(
                line
                for line in checksums.read_text(encoding="utf-8").splitlines()
                if not line.endswith(f"tunnel-client-{RELEASE}-vulnerability-report.json")
            )
            + "\n",
            encoding="utf-8",
        )
        assert_rejected(artifact_dir, "SHA256SUMS.txt subject set differs from release artifacts")

        create_inputs(artifact_dir)
        generate(artifact_dir, grype)
        write_inventories(artifact_dir)
        (artifact_dir / "unexpected-link").symlink_to("SHA256SUMS.txt")
        assert_rejected(artifact_dir, "artifact directory must contain only regular root files")

        create_inputs(artifact_dir)
        generate(artifact_dir, grype, mode="zero")
        write_inventories(artifact_dir)
        if (artifact_dir / f"tunnel-client-{RELEASE}.openvex.json").exists():
            fail("OpenVEX must be omitted for a zero-findings report")
        must_run(
            [
                "bash",
                str(VERIFIER),
                "--artifact-dir",
                str(artifact_dir),
                "--release",
                RELEASE,
                "--source-digest",
                SOURCE_DIGEST,
            ]
        )

        create_inputs(artifact_dir)
        generate(artifact_dir, grype)
        write_inventories(artifact_dir)
        manifest_path = artifact_dir / f"tunnel-client-{RELEASE}-enterprise-evidence.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["sourceDigest"] = "1" * 40
        manifest_path.write_text(json.dumps(manifest) + "\n", encoding="utf-8")
        write_inventories(artifact_dir)
        assert_rejected(
            artifact_dir, "enterprise evidence manifest release metadata does not match"
        )

    print("release evidence tooling tests: passed")


if __name__ == "__main__":
    main()
