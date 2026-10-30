#!/usr/bin/env python3
"""Bind reviewed license records to the Android runtime's actual Go packages."""

from __future__ import annotations

import argparse
import json
import pathlib
import tempfile
from typing import Any


HEADER = "DEPENDENCY|TYPE|VERSION|LICENSES|LICENSE_FILE|PLATFORMS"
PLATFORM = "android/arm64"
MAIN_MODULE = "github.com/openai/tunnel-client"


def binary_modules(text: str) -> dict[str, str]:
    modules: dict[str, str] = {}
    settings: dict[str, str] = {}
    main_module = ""
    for line in text.splitlines():
        fields = line.strip().split("\t")
        if fields[0] == "=>":
            raise ValueError("replaced modules are not supported in release binaries")
        if fields[0] in {"mod", "dep"}:
            if len(fields) < 3 or not fields[1] or not fields[2]:
                raise ValueError("malformed binary module information")
            if fields[0] == "mod":
                if main_module:
                    raise ValueError("duplicate main module in binary information")
                main_module = fields[1]
            else:
                if fields[1] in modules:
                    raise ValueError("duplicate dependency in binary information")
                modules[fields[1]] = fields[2]
        elif fields[0] == "build" and len(fields) == 2:
            key, sep, value = fields[1].partition("=")
            if sep:
                if key in settings:
                    raise ValueError("duplicate binary build setting")
                settings[key] = value
    if main_module != MAIN_MODULE:
        raise ValueError("binary main module is not github.com/openai/tunnel-client")
    for key, expected in {"GOOS": "android", "GOARCH": "arm64", "CGO_ENABLED": "1"}.items():
        if settings.get(key) != expected:
            raise ValueError(f"binary must have {key}={expected}")
    if not modules:
        raise ValueError("binary contains no dependency module information")
    return modules


def package_graph(text: str) -> list[dict[str, Any]]:
    decoder = json.JSONDecoder()
    packages = []
    offset = 0
    while offset < len(text):
        if text[offset].isspace():
            offset += 1
            continue
        package, offset = decoder.raw_decode(text, offset)
        if not isinstance(package, dict) or not isinstance(package.get("ImportPath"), str):
            raise ValueError("malformed package graph entry")
        if package.get("Error") or package.get("DepsErrors") or package.get("Incomplete"):
            raise ValueError("package graph contains unresolved dependencies")
        if package.get("ForTest"):
            raise ValueError("package graph must not include test packages")
        packages.append(package)
    if not packages:
        raise ValueError("package graph is empty")
    return packages


def license_catalog(text: str) -> list[tuple[str, ...]]:
    rows = []
    in_table = False
    dependencies = set()
    for line in text.splitlines():
        if line == HEADER:
            if in_table:
                raise ValueError("duplicate license catalog header")
            in_table = True
            continue
        if not in_table or not line:
            continue
        fields = tuple(line.split("|"))
        if len(fields) != 6 or any(not field.strip() for field in fields):
            raise ValueError("malformed license catalog row")
        if fields[1] not in {"direct", "transitive"}:
            raise ValueError("invalid license catalog dependency type")
        if fields[0] in dependencies:
            raise ValueError(f"ambiguous license catalog dependency: {fields[0]}")
        dependencies.add(fields[0])
        rows.append(fields)
    if not rows:
        raise ValueError("license catalog has no dependency rows")
    return rows


def render_report(build_info: str, graph: str, catalog: str) -> str:
    linked_modules = binary_modules(build_info)
    packages = package_graph(graph)
    rows = license_catalog(catalog)
    graph_modules: dict[str, str] = {}
    selected: set[tuple[str, ...]] = set()
    seen_packages = set()
    saw_main = False
    for package in packages:
        import_path = package["ImportPath"]
        if import_path in seen_packages:
            raise ValueError(f"duplicate package graph entry: {import_path}")
        seen_packages.add(import_path)
        if package.get("Standard"):
            continue
        module = package.get("Module")
        if not isinstance(module, dict):
            raise ValueError(f"package has no module: {import_path}")
        if module.get("Replace") is not None:
            raise ValueError("replaced modules are not supported in package graphs")
        if module.get("Main"):
            if module.get("Path") != MAIN_MODULE:
                raise ValueError("package graph has an unexpected main module")
            saw_main = True
            continue
        module_path, version = module.get("Path"), module.get("Version")
        if not isinstance(module_path, str) or not isinstance(version, str) or not version:
            raise ValueError(f"package has incomplete module information: {import_path}")
        if not (import_path == module_path or import_path.startswith(module_path + "/")):
            raise ValueError(f"package does not belong to its module: {import_path}")
        if module_path in graph_modules and graph_modules[module_path] != version:
            raise ValueError(f"package graph contains conflicting module versions: {module_path}")
        graph_modules[module_path] = version
        if linked_modules.get(module_path) != version:
            raise ValueError(f"package graph and binary module versions differ: {module_path}")

        # Select the most specific reviewed package record before validating its
        # version. This prevents a stale special-license record from silently
        # falling back to the owning module's more general license.
        candidates = [row for row in rows if import_path == row[0] or import_path.startswith(row[0] + "/")]
        if not candidates:
            raise ValueError(f"package has no reviewed license record: {import_path}")
        row = max(candidates, key=lambda candidate: len(candidate[0]))
        if row[2] != version or not row[4].startswith(module_path + "@" + version + "/"):
            raise ValueError(f"license record does not match package module/version: {import_path}")
        selected.add((*row[:5], PLATFORM))

    if not saw_main:
        raise ValueError("package graph is missing the runtime main module")
    if graph_modules != linked_modules:
        missing = sorted(set(linked_modules) - set(graph_modules))
        raise ValueError(f"package graph does not cover binary modules: {missing}")
    if not selected:
        raise ValueError("Android runtime has no licensed dependency packages")
    licenses = sorted({license.strip() for row in selected for license in row[3].split(",")})
    lines = [
        "License report for tunnel-client release artifact flavor: runtime",
        "Platforms: " + PLATFORM,
        "Tests included: false",
        "Scope: Go dependency packages selected by the Android build; versions verified against the executable.",
        "Native toolchain and Go runtime notices are included in NOTICE.",
        "License summary:",
        *("  " + license for license in licenses),
        "",
        HEADER,
        *("|".join(row) for row in sorted(selected)),
    ]
    return "\n".join(lines) + "\n"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary-build-info", type=pathlib.Path, required=True)
    parser.add_argument("--package-graph", type=pathlib.Path, required=True)
    parser.add_argument("--base-report", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    temporary = None
    try:
        rendered = render_report(
            args.binary_build_info.read_text(encoding="utf-8"),
            args.package_graph.read_text(encoding="utf-8"),
            args.base_report.read_text(encoding="utf-8"),
        )
        args.output.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=args.output.parent, delete=False) as output:
            temporary = pathlib.Path(output.name)
            output.write(rendered)
        temporary.replace(args.output)
    except (OSError, ValueError) as error:
        parser.exit(1, f"build_android_license_report.py: {error}\n")
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
