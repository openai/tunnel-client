#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  verify_release_evidence.sh \
    --artifact-dir <release-artifact-directory> \
    --release <vX.Y.Z> \
    --source-digest <40-character-commit-sha>

Fails closed unless the release vulnerability report, conditional OpenVEX
document, enterprise evidence manifest, checksums, and public URL inventory
describe the same regular release artifacts and exact bytes.
EOF
}

die() {
  echo "verify_release_evidence.sh: $*" >&2
  exit 1
}

artifact_dir=""
release=""
source_digest=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --artifact-dir)
      [[ $# -ge 2 && -n "${2:-}" ]] || die "--artifact-dir requires a value"
      artifact_dir="${2:-}"
      shift 2
      ;;
    --release)
      [[ $# -ge 2 && -n "${2:-}" ]] || die "--release requires a value"
      release="${2:-}"
      shift 2
      ;;
    --source-digest)
      [[ $# -ge 2 && -n "${2:-}" ]] || die "--source-digest requires a value"
      source_digest="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      die "unknown argument: $1"
      ;;
  esac
done

[[ -n "${artifact_dir}" ]] || die "--artifact-dir is required"
[[ -d "${artifact_dir}" && ! -L "${artifact_dir}" ]] ||
  die "artifact directory does not exist: ${artifact_dir}"
[[ "${release}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] ||
  die "--release must be a v-prefixed semver tag"
[[ "${source_digest}" =~ ^[0-9a-f]{40}$ ]] ||
  die "--source-digest must be a 40-character lowercase commit SHA"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

artifact_dir="$(cd "${artifact_dir}" && pwd)"
python3 - "${artifact_dir}" "${release}" "${source_digest}" <<'PY'
from __future__ import annotations

import fnmatch
import hashlib
import json
import pathlib
import re
import sys
import urllib.parse
from collections import Counter
from typing import Any


artifact_dir = pathlib.Path(sys.argv[1])
release = sys.argv[2]
source_digest = sys.argv[3]
sha256_re = re.compile(r"^[0-9a-f]{64}$")
name_re = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")
platforms = (
    "darwin-amd64",
    "darwin-arm64",
    "linux-amd64",
    "linux-arm64",
    "windows-amd64",
    "windows-arm64",
)
flavors = (
    ("client", "tunnel-client"),
    ("runtime", "tunnel-client-runtime"),
    ("runtime-cloudflared", "tunnel-client-runtime-cloudflared"),
)
report_name = f"tunnel-client-{release}-vulnerability-report.json"
vex_name = f"tunnel-client-{release}.openvex.json"
manifest_name = f"tunnel-client-{release}-enterprise-evidence.json"
bundle_name = f"tunnel-client-{release}-provenance.sigstore.json"
public_base = f"https://persistent.oaistatic.com/tunnel-client/{release}"


def fail(message: str) -> None:
    raise SystemExit(f"verify_release_evidence.sh: {message}")


def digest(path: pathlib.Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def regular(path: pathlib.Path, label: str) -> pathlib.Path:
    if path.is_symlink() or not path.is_file():
        fail(f"{label} must be a regular file: {path.name}")
    return path


def load_json(name: str) -> Any:
    path = regular(artifact_dir / name, name)
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"{name} is not valid JSON: {exc}")


def ref(name: str) -> dict[str, str]:
    path = regular(artifact_dir / name, name)
    return {"name": name, "sha256": digest(path)}


def check_ref(value: Any, expected: dict[str, str], label: str) -> None:
    if value != expected:
        fail(f"{label} does not match release bytes")


def allowed_name(name: str) -> bool:
    if name in {
        report_name,
        vex_name,
        manifest_name,
        bundle_name,
        "SHA256SUMS.txt",
        "PUBLIC_URLS.txt",
    }:
        return True
    return any(
        fnmatch.fnmatch(name, pattern)
        for pattern in (
            "*.zip",
            "*.tar.gz",
            "*.spdx.json",
            "*-licenses.txt",
            "*-scan-manifest.json",
        )
    )


root_files: dict[str, pathlib.Path] = {}
for path in sorted(artifact_dir.iterdir(), key=lambda candidate: candidate.name):
    if path.is_symlink() or not path.is_file():
        fail(f"artifact directory must contain only regular root files: {path.name}")
    if not name_re.fullmatch(path.name) or not allowed_name(path.name):
        fail(f"unexpected release artifact: {path.name}")
    root_files[path.name] = path

for required in (report_name, manifest_name, "SHA256SUMS.txt", "PUBLIC_URLS.txt"):
    if required not in root_files:
        fail(f"artifact directory is missing {required}")


def expected_inputs() -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for flavor, prefix in flavors:
        for platform in platforms:
            stem = f"{prefix}-{release}-{platform}"
            records.append(
                {
                    "flavor": flavor,
                    "platform": platform,
                    "zip": ref(f"{stem}.zip"),
                    "sbom": ref(f"{stem}.spdx.json"),
                    "licenseReport": ref(f"{stem}-licenses.txt"),
                }
            )
    return records


expected_scans = expected_inputs()
expected_scan_by_key = {
    (record["flavor"], record["platform"]): record for record in expected_scans
}


def parse_checksums() -> dict[str, str]:
    entries: dict[str, str] = {}
    for line in regular(artifact_dir / "SHA256SUMS.txt", "SHA256SUMS.txt").read_text(
        encoding="utf-8"
    ).splitlines():
        match = re.fullmatch(r"([0-9a-f]{64}) [ *]([A-Za-z0-9][A-Za-z0-9._-]*)", line)
        if match is None:
            fail("SHA256SUMS.txt has a malformed line")
        sha256, name = match.groups()
        if name in entries:
            fail(f"SHA256SUMS.txt has duplicate entry: {name}")
        if name not in root_files:
            fail(f"SHA256SUMS.txt names a missing artifact: {name}")
        if name in {"SHA256SUMS.txt", "PUBLIC_URLS.txt", bundle_name}:
            fail(f"SHA256SUMS.txt must not self-cover later artifact: {name}")
        if digest(root_files[name]) != sha256:
            fail(f"SHA256SUMS.txt digest differs from release artifact: {name}")
        entries[name] = sha256
    expected = set(root_files) - {"SHA256SUMS.txt", "PUBLIC_URLS.txt", bundle_name}
    if set(entries) != expected:
        fail(
            "SHA256SUMS.txt subject set differs from release artifacts: "
            f"missing={sorted(expected - set(entries))}, unexpected={sorted(set(entries) - expected)}"
        )
    return entries


checksums = parse_checksums()


def parse_urls() -> dict[str, str]:
    urls: dict[str, str] = {}
    for line in regular(artifact_dir / "PUBLIC_URLS.txt", "PUBLIC_URLS.txt").read_text(
        encoding="utf-8"
    ).splitlines():
        prefix = f"{public_base}/"
        if not line.startswith(prefix):
            fail(f"PUBLIC_URLS.txt has URL outside the stable release base: {line}")
        name = line.removeprefix(prefix)
        if not name_re.fullmatch(name) or name in urls:
            fail(f"PUBLIC_URLS.txt has invalid or duplicate artifact URL: {line}")
        urls[name] = line
    expected = set(checksums) | {"SHA256SUMS.txt", bundle_name}
    if set(urls) != expected:
        fail(
            "PUBLIC_URLS.txt subject set differs from release artifacts: "
            f"missing={sorted(expected - set(urls))}, unexpected={sorted(set(urls) - expected)}"
        )
    return urls


parse_urls()

report = load_json(report_name)
if not isinstance(report, dict):
    fail("vulnerability report must be an object")
if (
    report.get("schemaVersion") != 1
    or report.get("kind") != "tunnel-client-release-vulnerability-report"
    or report.get("release") != release
    or report.get("sourceDigest") != source_digest
):
    fail("vulnerability report release metadata does not match")
scanner = report.get("scanner")
if not isinstance(scanner, dict) or scanner.get("name") != "grype":
    fail("vulnerability report scanner must be Grype")
if not isinstance(scanner.get("version"), str) or not scanner["version"]:
    fail("vulnerability report scanner version is missing")
database = scanner.get("database")
database_from = database.get("from") if isinstance(database, dict) else None
database_checksums = (
    urllib.parse.parse_qs(urllib.parse.urlparse(database_from).query).get(
        "checksum", []
    )
    if isinstance(database_from, str)
    else []
)
if (
    not isinstance(database, dict)
    or database.get("valid") is not True
    or not isinstance(database.get("schemaVersion"), str)
    or not isinstance(database_from, str)
    or not database_from.startswith("https://")
    or len(database_checksums) != 1
    or not re.fullmatch(r"sha256:[0-9a-f]{64}", database_checksums[0])
    or database.get("sha256") != database_checksums[0].removeprefix("sha256:")
    or not isinstance(database.get("built"), str)
):
    fail("vulnerability report database metadata is invalid")
scope = report.get("scope")
if not isinstance(scope, dict) or scope.get("kind") != "release-spdx-sidecars":
    fail("vulnerability report scan scope is invalid")
if scope.get("scannedSbomCount") != len(expected_scans):
    fail("vulnerability report scan count is invalid")
if scope.get("scannedArtifacts") != expected_scans:
    fail("vulnerability report scan scope does not match release sidecars")
oci = scope.get("ociImages")
if not isinstance(oci, dict) or oci.get("status") != "not_scanned":
    fail("vulnerability report must not claim OCI scan coverage")

scans = report.get("scans")
if not isinstance(scans, list) or len(scans) != len(expected_scans):
    fail("vulnerability report must contain one scan per release sidecar")
finding_names: set[str] = set()
severity_counts: Counter[str] = Counter()
match_count = 0
seen_scan_keys: set[tuple[str, str]] = set()
vex_inputs: dict[str, dict[str, set[str]]] = {}
for scan in scans:
    if not isinstance(scan, dict) or not isinstance(scan.get("input"), dict):
        fail("vulnerability report contains malformed scan")
    input_record = scan["input"]
    key = (input_record.get("flavor"), input_record.get("platform"))
    if key not in expected_scan_by_key or key in seen_scan_keys:
        fail("vulnerability report contains unexpected or duplicate scan input")
    if input_record != expected_scan_by_key[key]:
        fail("vulnerability report scan input does not match release bytes")
    seen_scan_keys.add(key)
    result = scan.get("result")
    if not isinstance(result, dict) or not isinstance(result.get("matches"), list):
        fail("vulnerability report scan result is malformed")
    if not isinstance(result.get("ignoredMatches"), list):
        fail("vulnerability report ignored match list is malformed")
    for finding in result["matches"]:
        if not isinstance(finding, dict) or not isinstance(finding.get("vulnerability"), dict):
            fail("vulnerability report match is malformed")
        vulnerability = finding["vulnerability"]
        name = vulnerability.get("id")
        if not isinstance(name, str) or not name or any(character.isspace() for character in name):
            fail("vulnerability report match has invalid vulnerability.id")
        finding_names.add(name)
        vex_input = vex_inputs.setdefault(
            name, {"aliases": set(), "products": set(), "subcomponents": set()}
        )
        related = finding.get("relatedVulnerabilities", [])
        if isinstance(related, list):
            for related_vulnerability in related:
                if (
                    isinstance(related_vulnerability, dict)
                    and isinstance(related_vulnerability.get("id"), str)
                    and related_vulnerability["id"]
                    and related_vulnerability["id"] != name
                ):
                    vex_input["aliases"].add(related_vulnerability["id"])
        vex_input["products"].add(
            f"https://github.com/openai/tunnel-client/releases/download/{release}/{input_record['zip']['name']}"
        )
        artifact = finding.get("artifact")
        if (
            isinstance(artifact, dict)
            and isinstance(artifact.get("purl"), str)
            and artifact["purl"]
        ):
            vex_input["subcomponents"].add(artifact["purl"])
        severity = vulnerability.get("severity", "Unknown")
        if not isinstance(severity, str) or not severity:
            severity = "Unknown"
        severity_counts[severity] += 1
        match_count += 1
if seen_scan_keys != set(expected_scan_by_key):
    fail("vulnerability report scan inputs are incomplete")
summary = report.get("summary")
if (
    not isinstance(summary, dict)
    or summary.get("matchCount") != match_count
    or summary.get("uniqueVulnerabilityCount") != len(finding_names)
    or summary.get("bySeverity") != dict(sorted(severity_counts.items()))
):
    fail("vulnerability report summary does not match scan findings")

manifest = load_json(manifest_name)
if not isinstance(manifest, dict):
    fail("enterprise evidence manifest must be an object")
if (
    manifest.get("schemaVersion") != 1
    or manifest.get("kind") != "tunnel-client-enterprise-release-evidence"
    or manifest.get("release") != release
    or manifest.get("sourceDigest") != source_digest
):
    fail("enterprise evidence manifest release metadata does not match")
manifest_artifacts = manifest.get("artifacts")
if not isinstance(manifest_artifacts, list):
    fail("enterprise evidence manifest artifacts are missing")
manifest_by_name: dict[str, str] = {}
for item in manifest_artifacts:
    if (
        not isinstance(item, dict)
        or not isinstance(item.get("name"), str)
        or not isinstance(item.get("sha256"), str)
        or not sha256_re.fullmatch(item["sha256"])
    ):
        fail("enterprise evidence manifest contains malformed artifact")
    name = item["name"]
    if name in manifest_by_name or name not in checksums:
        fail(f"enterprise evidence manifest contains unexpected artifact: {name}")
    if checksums[name] != item["sha256"]:
        fail(f"enterprise evidence manifest digest differs from release artifact: {name}")
    manifest_by_name[name] = item["sha256"]
expected_manifest_artifacts = set(checksums) - {manifest_name}
if set(manifest_by_name) != expected_manifest_artifacts:
    fail(
        "enterprise evidence manifest artifact set differs from pre-manifest artifacts: "
        f"missing={sorted(expected_manifest_artifacts - set(manifest_by_name))}, "
        f"unexpected={sorted(set(manifest_by_name) - expected_manifest_artifacts)}"
    )
assessment = manifest.get("vulnerabilityAssessment")
if not isinstance(assessment, dict):
    fail("enterprise evidence manifest vulnerabilityAssessment is missing")
check_ref(assessment.get("report"), ref(report_name), "enterprise evidence report reference")
if (
    assessment.get("matchCount") != match_count
    or assessment.get("uniqueVulnerabilityCount") != len(finding_names)
    or assessment.get("scope") != scope
):
    fail("enterprise evidence vulnerability assessment does not match report")
provenance = manifest.get("provenance")
if (
    not isinstance(provenance, dict)
    or provenance.get("bundleName") != bundle_name
    or provenance.get("statementCoverage")
    != "all regular release artifacts except the bundle itself"
):
    fail("enterprise evidence provenance boundary is invalid")
if manifest.get("selfDigestExclusions") != [
    manifest_name,
    "SHA256SUMS.txt",
    "PUBLIC_URLS.txt",
    bundle_name,
]:
    fail("enterprise evidence self-digest exclusions are invalid")

if finding_names:
    if vex_name not in root_files:
        fail("OpenVEX document is required when findings exist")
    check_ref(assessment.get("openVex"), ref(vex_name), "enterprise evidence OpenVEX reference")
    if assessment.get("openVexDisposition") != "emitted-under-investigation":
        fail("enterprise evidence OpenVEX disposition is invalid")
    vex = load_json(vex_name)
    if not isinstance(vex, dict) or vex.get("@context") != "https://openvex.dev/ns/v0.2.0":
        fail("OpenVEX document context is invalid")
    if (
        vex.get("@id") != f"https://github.com/openai/tunnel-client/releases/download/{release}/{vex_name}"
        or vex.get("author") != "OpenAI"
        or vex.get("role") != "Document Creator"
        or vex.get("version") != 1
    ):
        fail("OpenVEX document metadata is invalid")
    statements = vex.get("statements")
    if not isinstance(statements, list) or not statements:
        fail("OpenVEX document must contain statements for findings")
    statement_names: set[str] = set()
    for statement in statements:
        if not isinstance(statement, dict) or statement.get("status") != "under_investigation":
            fail("OpenVEX statements must use only under_investigation")
        if any(key in statement for key in ("justification", "impact_statement", "action_statement")):
            fail("OpenVEX under_investigation statements must not claim a disposition")
        vulnerability = statement.get("vulnerability")
        if not isinstance(vulnerability, dict) or not isinstance(vulnerability.get("name"), str):
            fail("OpenVEX statement vulnerability is malformed")
        name = vulnerability["name"]
        if name not in finding_names or name in statement_names:
            fail("OpenVEX statement does not map one-to-one to scan findings")
        statement_names.add(name)
        expected_vulnerability = {"name": name}
        aliases = sorted(vex_inputs[name]["aliases"])
        if aliases:
            expected_vulnerability["aliases"] = aliases
        if vulnerability != expected_vulnerability:
            fail("OpenVEX statement vulnerability does not match scan findings")
        products = statement.get("products")
        expected_products = [
            {"@id": product} for product in sorted(vex_inputs[name]["products"])
        ]
        if products != expected_products:
            fail("OpenVEX statement products do not match scan findings")
        expected_subcomponents = [
            {"@id": component}
            for component in sorted(vex_inputs[name]["subcomponents"])
        ]
        if statement.get("subcomponents", []) != expected_subcomponents:
            fail("OpenVEX statement subcomponents do not match scan findings")
    if statement_names != finding_names:
        fail("OpenVEX statements do not cover every scan finding")
else:
    if vex_name in root_files:
        fail("OpenVEX document must be omitted when there are no findings")
    if assessment.get("openVex") is not None:
        fail("enterprise evidence must not reference OpenVEX without findings")
    if assessment.get("openVexDisposition") != "omitted-no-findings":
        fail("enterprise evidence no-findings disposition is invalid")

print("release evidence contract: passed")
PY
