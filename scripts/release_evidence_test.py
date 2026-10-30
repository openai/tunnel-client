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
    child_env = os.environ.copy() if env is None else env.copy()
    # Child scripts resolve python3 by name; keep them on this test's interpreter.
    child_env["PATH"] = os.pathsep.join(
        (str(pathlib.Path(sys.executable).parent), child_env.get("PATH", os.defpath))
    )
    return subprocess.run(command, check=False, capture_output=True, text=True, env=child_env)


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


def create_inputs(artifact_dir: pathlib.Path, *, android: bool = False) -> None:
    shutil.rmtree(artifact_dir, ignore_errors=True)
    artifact_dir.mkdir(parents=True)
    for flavor, prefix in FLAVORS:
        platforms = PLATFORMS + (
            ("android-arm64",) if android and flavor == "runtime" else ()
        )
        for platform in platforms:
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


def generator_result(
    artifact_dir: pathlib.Path,
    grype: pathlib.Path,
    mode: str = "findings",
    *,
    require_android: bool = False,
) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env["FAKE_GRYPE_MODE"] = mode
    return run(
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
            *(["--require-android-runtime"] if require_android else []),
        ],
        env=env,
    )


def generate(
    artifact_dir: pathlib.Path,
    grype: pathlib.Path,
    mode: str = "findings",
    *,
    require_android: bool = False,
) -> None:
    result = generator_result(artifact_dir, grype, mode, require_android=require_android)
    if result.returncode != 0:
        fail(f"generation failed:\n{result.stdout}\n{result.stderr}")


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


def verifier_result(
    artifact_dir: pathlib.Path, *, require_android: bool = False
) -> subprocess.CompletedProcess[str]:
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
            *(["--require-android-runtime"] if require_android else []),
        ]
    )


def assert_failure(result: subprocess.CompletedProcess[str], expected: str) -> None:
    if result.returncode == 0:
        fail("expected invalid release evidence fixture to fail")
    output = result.stdout + result.stderr
    if expected not in output:
        fail(f"expected failure containing {expected!r}, got:\n{output}")


def assert_rejected(artifact_dir: pathlib.Path, expected: str) -> None:
    assert_failure(verifier_result(artifact_dir), expected)


def test_android_releases(artifact_dir: pathlib.Path, grype: pathlib.Path) -> None:
    android_stem = f"tunnel-client-runtime-{RELEASE}-android-arm64"
    report_path = artifact_dir / f"tunnel-client-{RELEASE}-vulnerability-report.json"

    # New workflows require Android, while old 18-payload releases remain valid.
    create_inputs(artifact_dir)
    assert_failure(
        generator_result(artifact_dir, grype, require_android=True),
        "Android runtime release artifacts are incomplete",
    )
    generate(artifact_dir, grype)
    write_inventories(artifact_dir)
    assert_failure(
        verifier_result(artifact_dir, require_android=True),
        "Android runtime release artifacts are incomplete",
    )

    # Presence detection and the explicit requirement must scan the same 19
    # artifacts, including exact Android archive/SPDX/license bytes.
    for required in (False, True):
        create_inputs(artifact_dir, android=True)
        generate(artifact_dir, grype, require_android=required)
        write_inventories(artifact_dir)
        result = verifier_result(artifact_dir, require_android=required)
        if result.returncode != 0:
            fail(f"Android evidence rejected:\n{result.stdout}\n{result.stderr}")
        report = json.loads(report_path.read_text(encoding="utf-8"))
        android_scans = [
            scan["input"]
            for scan in report["scans"]
            if scan["input"]["platform"] == "android-arm64"
        ]
        if report["scope"]["scannedSbomCount"] != 19 or len(android_scans) != 1:
            fail("Android must add exactly one scan to the legacy 18")
        record = android_scans[0]
        if record["flavor"] != "runtime":
            fail("only the plain runtime has an Android release")
        for key, suffix in (
            ("zip", ".zip"),
            ("sbom", ".spdx.json"),
            ("licenseReport", "-licenses.txt"),
        ):
            path = artifact_dir / f"{android_stem}{suffix}"
            if record[key] != {"name": path.name, "sha256": sha256(path)}:
                fail(f"Android scan input {key} does not bind the actual bytes")
        vex_path = artifact_dir / f"tunnel-client-{RELEASE}.openvex.json"
        vex = json.loads(vex_path.read_text(encoding="utf-8"))
        android_url = (
            "https://github.com/openai/tunnel-client/releases/download/"
            f"{RELEASE}/{android_stem}.zip"
        )
        if {"@id": android_url} not in vex["statements"][0]["products"]:
            fail("Android findings must be included in OpenVEX")

    # A partial triplet cannot silently be treated as a legacy release, even
    # without --require-android-runtime. Check each artifact independently.
    for suffix in (".zip", ".spdx.json", "-licenses.txt"):
        create_inputs(artifact_dir, android=True)
        generate(artifact_dir, grype)
        write_inventories(artifact_dir)
        (artifact_dir / f"{android_stem}{suffix}").unlink()
        assert_failure(
            generator_result(artifact_dir, grype),
            "Android runtime release artifacts are incomplete",
        )
        assert_rejected(artifact_dir, "Android runtime release artifacts are incomplete")

    for name in (
        f"tunnel-client-{RELEASE}-android-arm64.zip",
        f"tunnel-client-runtime-cloudflared-{RELEASE}-android-arm64.spdx.json",
        f"tunnel-client-runtime-{RELEASE}-android-amd64-licenses.txt",
    ):
        create_inputs(artifact_dir)
        generate(artifact_dir, grype)
        write_inventories(artifact_dir)
        (artifact_dir / name).write_text("unsupported Android artifact\n", encoding="utf-8")
        assert_failure(
            generator_result(artifact_dir, grype), "unsupported Android release artifacts"
        )
        assert_rejected(artifact_dir, "unsupported Android release artifacts")

    create_inputs(artifact_dir, android=True)
    generate(artifact_dir, grype)
    write_inventories(artifact_dir)
    (artifact_dir / f"{android_stem}.zip").write_text(
        "changed Android bytes\n", encoding="utf-8"
    )
    write_inventories(artifact_dir)
    assert_rejected(
        artifact_dir, "vulnerability report scan scope does not match release sidecars"
    )

    create_inputs(artifact_dir, android=True)
    generate(artifact_dir, grype)
    report = json.loads(report_path.read_text(encoding="utf-8"))
    report["scans"] = [
        scan for scan in report["scans"] if scan["input"]["platform"] != "android-arm64"
    ]
    report_path.write_text(json.dumps(report) + "\n", encoding="utf-8")
    write_inventories(artifact_dir)
    assert_rejected(
        artifact_dir, "vulnerability report must contain one scan per release sidecar"
    )


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

        test_android_releases(artifact_dir, grype)

    print("release evidence tooling tests: passed")


if __name__ == "__main__":
    main()
