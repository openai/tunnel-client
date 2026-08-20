"""Prepare staged tunnel-client source inputs for hermetic SBOM generation.

This helper is a declared Bazel Python executable. It only reads or rewrites
paths already staged below TEST_TMPDIR, so the shell driver never falls back to
an ambient Python interpreter.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import stat
import sys
from pathlib import Path


class SourcePreparationError(RuntimeError):
    pass


def fail(message: str) -> SourcePreparationError:
    return SourcePreparationError(message)


def require_test_tmpdir_child(raw_path: str, flag: str, *, directory: bool) -> Path:
    path = Path(raw_path)
    if not path.is_absolute():
        raise fail(f"{flag} must be an absolute path")
    test_tmpdir = os.environ.get("TEST_TMPDIR")
    if not os.environ.get("BAZEL_TEST") or not test_tmpdir:
        raise fail("BAZEL_TEST and TEST_TMPDIR are required")
    test_root = Path(test_tmpdir)
    if not test_root.is_absolute():
        raise fail("TEST_TMPDIR must be an absolute directory")
    try:
        test_mode = test_root.lstat().st_mode
        resolved_test_root = test_root.resolve(strict=True)
        resolved_path = path.resolve(strict=True)
    except OSError as error:
        raise fail(f"{flag} must exist below TEST_TMPDIR") from error
    if stat.S_ISLNK(test_mode) or not stat.S_ISDIR(test_mode):
        raise fail("TEST_TMPDIR must be an absolute directory")
    try:
        raw_relative = path.relative_to(test_root)
        resolved_relative = resolved_path.relative_to(resolved_test_root)
    except ValueError as error:
        raise fail(f"{flag} must stay below TEST_TMPDIR") from error
    if any(part in {".", ".."} for part in raw_relative.parts) or raw_relative != resolved_relative:
        raise fail(f"{flag} must not use non-canonical paths")
    current = test_root
    for part in raw_relative.parts:
        current /= part
        try:
            mode = current.lstat().st_mode
        except OSError as error:
            raise fail(f"{flag} must exist below TEST_TMPDIR") from error
        if stat.S_ISLNK(mode):
            raise fail(f"{flag} must not use symlink parents")
    if directory and not resolved_path.is_dir():
        raise fail(f"{flag} must be a directory")
    if not directory and not resolved_path.is_file():
        raise fail(f"{flag} must be a file")
    return resolved_path


def canonicalize_source(args: argparse.Namespace) -> None:
    root = require_test_tmpdir_child(args.source_root, "--source-root", directory=True)
    old_module_path = args.legacy_module_path.encode("utf-8")
    new_module_path = args.canonical_module_path.encode("utf-8")
    for relative_root in ("go.mod", "go.sum", "cmd", "pkg", "docs", "plugins"):
        candidate_root = root / relative_root
        if not candidate_root.exists():
            continue
        paths = [candidate_root] if candidate_root.is_file() else sorted(candidate_root.rglob("*"))
        for path in paths:
            if path.is_symlink() or not path.is_file():
                continue
            contents = path.read_bytes()
            if old_module_path in contents:
                path.write_bytes(contents.replace(old_module_path, new_module_path))


def print_cloudflared_metadata(args: argparse.Namespace) -> None:
    manifest_path = require_test_tmpdir_child(args.manifest, "--manifest", directory=False)
    source_root = require_test_tmpdir_child(args.source_root, "--source-root", directory=True)
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        metadata = {
            field: manifest[field]
            for field in (
                "version",
                "build_time",
                "release_commit",
                "module_path",
                "package_path",
                "module_version",
            )
        }
    except (KeyError, json.JSONDecodeError, OSError) as error:
        raise fail("cloudflared manifest is missing required source metadata") from error
    if any(not isinstance(value, str) for value in metadata.values()):
        raise fail("cloudflared manifest source metadata must be strings")
    if any(
        not value or any(separator in value for separator in "\t\n\r")
        for value in metadata.values()
    ):
        raise fail("cloudflared manifest source metadata must be single-line values")

    release_commit = metadata["release_commit"]
    module_path = metadata["module_path"]
    package_path = metadata["package_path"]
    module_version = metadata["module_version"]
    if not re.fullmatch(r"[0-9a-f]{40}", release_commit):
        raise fail("cloudflared manifest release_commit must be a full lowercase SHA")
    if source_root.name != f"cloudflared-{release_commit}":
        raise fail("cloudflared source directory does not match manifest release_commit")
    try:
        go_mod_lines = (source_root / "go.mod").read_text(encoding="utf-8").splitlines()
    except OSError as error:
        raise fail("cloudflared source is missing go.mod") from error
    source_module_path = next(
        (
            fields[1]
            for line in go_mod_lines
            if len(fields := line.split()) >= 2 and fields[0] == "module"
        ),
        "",
    )
    if source_module_path != module_path:
        raise fail("cloudflared source module path does not match manifest module_path")
    if package_path != f"{module_path}/cmd/cloudflared":
        raise fail("cloudflared manifest package_path does not match module_path")
    if module_version.rsplit("-", 1)[-1] != release_commit[:12]:
        raise fail("cloudflared manifest module_version does not match release_commit")

    print(
        metadata["version"],
        metadata["build_time"],
        module_path,
        module_version,
        sep="\t",
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    canonicalize = subparsers.add_parser("canonicalize")
    canonicalize.add_argument("--source-root", required=True)
    canonicalize.add_argument("--legacy-module-path", required=True)
    canonicalize.add_argument("--canonical-module-path", required=True)
    canonicalize.set_defaults(func=canonicalize_source)

    metadata = subparsers.add_parser("cloudflared-metadata")
    metadata.add_argument("--manifest", required=True)
    metadata.add_argument("--source-root", required=True)
    metadata.set_defaults(func=print_cloudflared_metadata)

    args = parser.parse_args()
    try:
        args.func(args)
    except (SourcePreparationError, OSError) as error:
        print(f"prepare_sbom_source.py: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
