#!/usr/bin/env python3
"""Generate signed-release vulnerability and enterprise evidence artifacts."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
import subprocess
import urllib.parse
from collections import Counter, defaultdict
from typing import Any

RELEASE_RE = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+(?:[-.][0-9A-Za-z.-]+)?$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
SOURCE_RE = re.compile(r"^[0-9a-f]{40}$")
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
REPORT_KIND = "tunnel-client-release-vulnerability-report"
MANIFEST_KIND = "tunnel-client-enterprise-release-evidence"
OPENVEX_CONTEXT = "https://openvex.dev/ns/v0.2.0"
PUBLIC_RELEASE_BASE = "https://github.com/openai/tunnel-client/releases/download"


def fail(message: str) -> None:
    raise SystemExit(f"generate_release_evidence.py: {message}")


def digest(path: pathlib.Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def regular_file(path: pathlib.Path, label: str) -> pathlib.Path:
    if path.is_symlink() or not path.is_file():
        fail(f"{label} must be a regular file: {path}")
    return path


def file_ref(path: pathlib.Path) -> dict[str, str]:
    return {"name": path.name, "sha256": digest(path)}


def write_json(path: pathlib.Path, payload: Any) -> None:
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def normalize_db_status(raw: Any) -> dict[str, Any]:
    if not isinstance(raw, dict):
        fail("Grype result is missing descriptor.db.status")
    database_url = raw.get("from")
    if not isinstance(database_url, str) or not database_url.startswith("https://"):
        fail("Grype result has invalid database status metadata")
    checksum_values = urllib.parse.parse_qs(urllib.parse.urlparse(database_url).query).get(
        "checksum", []
    )
    if len(checksum_values) != 1 or not re.fullmatch(r"sha256:[0-9a-f]{64}", checksum_values[0]):
        fail("Grype database URL must carry one SHA256 checksum")
    normalized = {
        "schemaVersion": raw.get("schemaVersion"),
        "from": database_url,
        "sha256": checksum_values[0].removeprefix("sha256:"),
        "built": raw.get("built"),
        "valid": raw.get("valid"),
    }
    if (
        not isinstance(normalized["schemaVersion"], str)
        or not normalized["schemaVersion"]
        or not isinstance(normalized["from"], str)
        or not SHA256_RE.fullmatch(normalized["sha256"])
        or not isinstance(normalized["built"], str)
        or normalized["valid"] is not True
    ):
        fail("Grype result has invalid database status metadata")
    return normalized


def scan_with_grype(grype: pathlib.Path, sbom: pathlib.Path) -> dict[str, Any]:
    env = os.environ.copy()
    env.setdefault("GRYPE_CHECK_FOR_APP_UPDATE", "false")
    env.setdefault("GRYPE_DB_AUTO_UPDATE", "false")
    result = subprocess.run(
        [str(grype), f"sbom:{sbom}", "--output", "json"],
        check=False,
        capture_output=True,
        text=True,
        env=env,
    )
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit {result.returncode}"
        fail(f"Grype scan failed for {sbom.name}: {detail}")
    try:
        payload = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        fail(f"Grype output is not valid JSON for {sbom.name}: {exc}")
    if not isinstance(payload, dict):
        fail(f"Grype output must be an object for {sbom.name}")
    descriptor = payload.get("descriptor")
    if not isinstance(descriptor, dict):
        fail(f"Grype output is missing descriptor for {sbom.name}")
    if descriptor.get("name") != "grype" or not isinstance(descriptor.get("version"), str):
        fail(f"Grype output has invalid scanner descriptor for {sbom.name}")
    matches = payload.get("matches")
    ignored_matches = payload.get("ignoredMatches", [])
    if not isinstance(matches, list) or not isinstance(ignored_matches, list):
        fail(f"Grype output has invalid match arrays for {sbom.name}")
    return {
        "descriptor": {
            "name": descriptor["name"],
            "version": descriptor["version"],
            "database": normalize_db_status(
                ((descriptor.get("db") or {}).get("status"))
                if isinstance(descriptor.get("db"), dict)
                else None
            ),
        },
        "source": payload.get("source"),
        "distro": payload.get("distro"),
        "matches": matches,
        "ignoredMatches": ignored_matches,
    }


def vulnerability_name(match: Any) -> str:
    if not isinstance(match, dict):
        fail("Grype match must be an object")
    vulnerability = match.get("vulnerability")
    if not isinstance(vulnerability, dict) or not isinstance(vulnerability.get("id"), str):
        fail("Grype match is missing vulnerability.id")
    name = vulnerability["id"]
    if not name or any(character.isspace() for character in name):
        fail(f"Grype match has invalid vulnerability.id: {name!r}")
    return name


def related_aliases(match: dict[str, Any], name: str) -> list[str]:
    aliases: set[str] = set()
    related = match.get("relatedVulnerabilities", [])
    if isinstance(related, list):
        for item in related:
            if isinstance(item, dict) and isinstance(item.get("id"), str):
                alias = item["id"]
                if alias and alias != name:
                    aliases.add(alias)
    return sorted(aliases)


def artifact_purl(match: dict[str, Any]) -> str | None:
    artifact = match.get("artifact")
    if not isinstance(artifact, dict):
        return None
    purl = artifact.get("purl")
    if isinstance(purl, str) and purl:
        return purl
    return None


def expected_inputs(artifact_dir: pathlib.Path, release: str) -> list[dict[str, Any]]:
    inputs: list[dict[str, Any]] = []
    for flavor, prefix in FLAVORS:
        for platform in PLATFORMS:
            stem = f"{prefix}-{release}-{platform}"
            zip_path = regular_file(artifact_dir / f"{stem}.zip", "release ZIP")
            sbom_path = regular_file(artifact_dir / f"{stem}.spdx.json", "release SPDX")
            license_path = regular_file(
                artifact_dir / f"{stem}-licenses.txt", "release license report"
            )
            inputs.append(
                {
                    "flavor": flavor,
                    "platform": platform,
                    "zip": file_ref(zip_path),
                    "sbom": file_ref(sbom_path),
                    "licenseReport": file_ref(license_path),
                    "_sbom_path": sbom_path,
                }
            )
    return inputs


def generated_at(value: str | None) -> str:
    if value is None:
        return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")
    try:
        parsed = dt.datetime.fromisoformat(value)
    except ValueError as exc:
        fail(f"--generated-at must be RFC3339: {exc}")
    if parsed.tzinfo is None:
        fail("--generated-at must include a timezone")
    return value


def allowed_pre_manifest(name: str, release: str) -> bool:
    if name in {
        f"tunnel-client-{release}-vulnerability-report.json",
        f"tunnel-client-{release}.openvex.json",
    }:
        return True
    return name.endswith((".zip", ".tar.gz", ".spdx.json", "-licenses.txt", "-scan-manifest.json"))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--release", required=True)
    parser.add_argument("--source-digest", required=True)
    parser.add_argument("--artifact-dir", required=True)
    parser.add_argument("--grype", required=True)
    parser.add_argument("--generated-at")
    args = parser.parse_args()

    if not RELEASE_RE.fullmatch(args.release):
        fail("--release must be a v-prefixed semver tag")
    if not SOURCE_RE.fullmatch(args.source_digest):
        fail("--source-digest must be a 40-character lowercase commit SHA")
    artifact_dir = pathlib.Path(args.artifact_dir).resolve()
    if artifact_dir.is_symlink() or not artifact_dir.is_dir():
        fail(f"artifact directory does not exist: {args.artifact_dir}")
    grype = pathlib.Path(args.grype).resolve()
    if grype.is_symlink() or not grype.is_file() or not os.access(grype, os.X_OK):
        fail(f"--grype must be an executable regular file: {args.grype}")
    timestamp = generated_at(args.generated_at)

    inputs = expected_inputs(artifact_dir, args.release)
    scans: list[dict[str, Any]] = []
    scanner: dict[str, Any] | None = None
    findings_by_vulnerability: defaultdict[str, dict[str, Any]] = defaultdict(
        lambda: {"aliases": set(), "products": set(), "subcomponents": set()}
    )
    severity_counts: Counter[str] = Counter()
    match_count = 0

    for input_record in inputs:
        sbom_path = input_record.pop("_sbom_path")
        result = scan_with_grype(grype, sbom_path)
        if scanner is None:
            scanner = result["descriptor"]
        elif scanner != result["descriptor"]:
            fail("Grype scanner or database metadata changed between release scans")

        matches = result["matches"]
        for match in matches:
            name = vulnerability_name(match)
            vulnerability = match["vulnerability"]
            severity = vulnerability.get("severity", "Unknown")
            if not isinstance(severity, str) or not severity:
                severity = "Unknown"
            severity_counts[severity] += 1
            match_count += 1
            grouped = findings_by_vulnerability[name]
            grouped["aliases"].update(related_aliases(match, name))
            grouped["products"].add(
                f"{PUBLIC_RELEASE_BASE}/{args.release}/{input_record['zip']['name']}"
            )
            purl = artifact_purl(match)
            if purl is not None:
                grouped["subcomponents"].add(purl)

        scans.append(
            {
                "input": input_record,
                "result": {
                    "source": result["source"],
                    "distro": result["distro"],
                    "matches": matches,
                    "ignoredMatches": result["ignoredMatches"],
                },
            }
        )

    if scanner is None:
        fail("no release SBOMs were scanned")

    report_name = f"tunnel-client-{args.release}-vulnerability-report.json"
    report_path = artifact_dir / report_name
    report = {
        "schemaVersion": 1,
        "kind": REPORT_KIND,
        "release": args.release,
        "sourceDigest": args.source_digest,
        "generatedAt": timestamp,
        "scanner": scanner,
        "scope": {
            "kind": "release-spdx-sidecars",
            "scannedSbomCount": len(inputs),
            "scannedArtifacts": [
                {
                    "flavor": record["input"]["flavor"],
                    "platform": record["input"]["platform"],
                    "zip": record["input"]["zip"],
                    "sbom": record["input"]["sbom"],
                    "licenseReport": record["input"]["licenseReport"],
                }
                for record in scans
            ],
            "ociImages": {
                "status": "not_scanned",
                "reason": (
                    "This release contract records multi-architecture OCI index digests, "
                    "not exact per-platform OCI manifest scan inputs."
                ),
            },
        },
        "summary": {
            "matchCount": match_count,
            "uniqueVulnerabilityCount": len(findings_by_vulnerability),
            "bySeverity": dict(sorted(severity_counts.items())),
        },
        "scans": scans,
    }
    write_json(report_path, report)

    vex_name = f"tunnel-client-{args.release}.openvex.json"
    vex_path = artifact_dir / vex_name
    vex_ref: dict[str, str] | None = None
    if findings_by_vulnerability:
        statements = []
        for name in sorted(findings_by_vulnerability):
            grouped = findings_by_vulnerability[name]
            vulnerability: dict[str, Any] = {"name": name}
            aliases = sorted(grouped["aliases"])
            if aliases:
                vulnerability["aliases"] = aliases
            statement: dict[str, Any] = {
                "vulnerability": vulnerability,
                "products": [{"@id": item} for item in sorted(grouped["products"])],
                "status": "under_investigation",
                "status_notes": (
                    "Generated from an untriaged Grype match. This statement does not "
                    "assert exploitability, non-impact, or remediation."
                ),
            }
            subcomponents = sorted(grouped["subcomponents"])
            if subcomponents:
                statement["subcomponents"] = [{"@id": item} for item in subcomponents]
            statements.append(statement)
        vex = {
            "@context": OPENVEX_CONTEXT,
            "@id": f"{PUBLIC_RELEASE_BASE}/{args.release}/{vex_name}",
            "author": "OpenAI",
            "role": "Document Creator",
            "timestamp": timestamp,
            "version": 1,
            "statements": statements,
        }
        write_json(vex_path, vex)
        vex_ref = file_ref(vex_path)
    elif vex_path.exists():
        if vex_path.is_symlink() or not vex_path.is_file():
            fail(f"stale OpenVEX path is not a regular file: {vex_path}")
        vex_path.unlink()

    manifest_name = f"tunnel-client-{args.release}-enterprise-evidence.json"
    manifest_path = artifact_dir / manifest_name
    if manifest_path.exists():
        if manifest_path.is_symlink() or not manifest_path.is_file():
            fail(f"enterprise evidence path is not a regular file: {manifest_path}")
        manifest_path.unlink()

    artifact_refs = []
    for path in sorted(artifact_dir.iterdir(), key=lambda candidate: candidate.name):
        if path.is_symlink() or not path.is_file():
            fail(f"artifact directory must contain only regular root files: {path.name}")
        if not allowed_pre_manifest(path.name, args.release):
            fail(f"unexpected pre-manifest release artifact: {path.name}")
        artifact_refs.append(file_ref(path))

    manifest = {
        "schemaVersion": 1,
        "kind": MANIFEST_KIND,
        "release": args.release,
        "sourceDigest": args.source_digest,
        "generatedAt": timestamp,
        "artifacts": artifact_refs,
        "vulnerabilityAssessment": {
            "report": file_ref(report_path),
            "openVex": vex_ref,
            "openVexDisposition": (
                "emitted-under-investigation" if vex_ref is not None else "omitted-no-findings"
            ),
            "matchCount": match_count,
            "uniqueVulnerabilityCount": len(findings_by_vulnerability),
            "scope": report["scope"],
        },
        "provenance": {
            "bundleName": f"tunnel-client-{args.release}-provenance.sigstore.json",
            "statementCoverage": "all regular release artifacts except the bundle itself",
            "generatedAfterThisManifest": [
                "SHA256SUMS.txt",
                "PUBLIC_URLS.txt",
                f"tunnel-client-{args.release}-provenance.sigstore.json",
            ],
        },
        "selfDigestExclusions": [
            manifest_name,
            "SHA256SUMS.txt",
            "PUBLIC_URLS.txt",
            f"tunnel-client-{args.release}-provenance.sigstore.json",
        ],
    }
    write_json(manifest_path, manifest)


if __name__ == "__main__":
    main()
