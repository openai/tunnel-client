"""Safely extract and verify the declared tunnel-client SBOM vendor tree.

The archive is a Bazel test input, not a module acquisition path. Extraction is
allowed only inside TEST_TMPDIR and the resulting bytes must match the reviewed
content digest before the SBOM payload builder can run with -mod=vendor.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import re
import shutil
import stat
import sys
import tarfile
from pathlib import Path, PurePosixPath

EXPECTED_DIGEST_PATH = Path("compliance/sbom-vendor-tree.sha256")


class VendorArchiveError(RuntimeError):
    pass


def fail(message: str) -> VendorArchiveError:
    return VendorArchiveError(message)


def require_test_tmpdir_child(raw_path: str) -> Path:
    source_root = Path(raw_path)
    if not source_root.is_absolute():
        raise fail("--source-root must be an absolute directory")
    try:
        source_mode = source_root.lstat().st_mode
    except OSError as error:
        raise fail("--source-root must be an absolute directory") from error
    if stat.S_ISLNK(source_mode) or not stat.S_ISDIR(source_mode):
        raise fail("--source-root must be an absolute directory")

    test_tmpdir = os.environ.get("TEST_TMPDIR")
    if not os.environ.get("BAZEL_TEST") or not test_tmpdir:
        raise fail("BAZEL_TEST and TEST_TMPDIR are required")
    test_root = Path(test_tmpdir)
    if not test_root.is_absolute():
        raise fail("TEST_TMPDIR must be an absolute directory")
    try:
        test_mode = test_root.lstat().st_mode
        resolved_test_root = test_root.resolve(strict=True)
        resolved_source_root = source_root.resolve(strict=True)
    except OSError as error:
        raise fail("TEST_TMPDIR and --source-root must be real directories") from error
    if stat.S_ISLNK(test_mode) or not stat.S_ISDIR(test_mode):
        raise fail("TEST_TMPDIR must be an absolute directory")
    try:
        raw_relative = source_root.relative_to(test_root)
        resolved_relative = resolved_source_root.relative_to(resolved_test_root)
    except ValueError as error:
        raise fail("--source-root must stay below TEST_TMPDIR") from error
    if any(part in {".", ".."} for part in raw_relative.parts) or raw_relative != resolved_relative:
        raise fail("--source-root must not use non-canonical paths")
    current = test_root
    for part in raw_relative.parts:
        current /= part
        try:
            if stat.S_ISLNK(current.lstat().st_mode):
                raise fail("--source-root must not use symlink parents")
        except OSError as error:
            raise fail("--source-root must be an absolute directory") from error
    return resolved_source_root


def safe_member_path(member_name: str) -> PurePosixPath:
    raw_parts = member_name.split("/")
    if member_name.startswith("/") or "." in raw_parts or ".." in raw_parts:
        raise fail(f"vendor archive has an unsafe path: {member_name}")
    path = PurePosixPath(member_name)
    if path.is_absolute() or not path.parts or path.parts[0] != "vendor":
        raise fail(f"vendor archive has an unexpected path: {member_name}")
    return path


def extract_archive(source_root: Path, archive: Path) -> None:
    if not archive.is_absolute() or not archive.is_file():
        raise fail("--vendor-archive must be an absolute file")
    vendor_root = source_root / "vendor"
    if vendor_root.exists() or vendor_root.is_symlink():
        raise fail("vendor archive destination already exists")

    with tarfile.open(archive, "r:gz") as source:
        members = source.getmembers()
        if not members:
            raise fail("vendor archive is empty")

        seen_paths: set[PurePosixPath] = set()
        file_paths: set[PurePosixPath] = set()
        for member in members:
            member_path = safe_member_path(member.name)
            if member_path in seen_paths:
                raise fail(f"vendor archive has a duplicate path: {member.name}")
            seen_paths.add(member_path)
            if member.issym() or member.islnk():
                raise fail(f"vendor archive must not contain links: {member.name}")
            if not (member.isdir() or member.isfile()):
                raise fail(f"vendor archive has an unsupported entry: {member.name}")
            if len(member_path.parts) == 1 and not member.isdir():
                raise fail("vendor archive root must be a directory")
            if member.isfile():
                file_paths.add(member_path)

        for member_path in seen_paths:
            for index in range(1, len(member_path.parts)):
                ancestor = PurePosixPath(*member_path.parts[:index])
                if ancestor in file_paths:
                    raise fail(
                        "vendor archive has a file/directory conflict: "
                        f"{ancestor} is a parent of {member_path}"
                    )

        for member in members:
            member_path = safe_member_path(member.name)
            destination = source_root.joinpath(*member_path.parts)
            if member.isdir():
                destination.mkdir(parents=True, exist_ok=True)
                continue
            destination.parent.mkdir(parents=True, exist_ok=True)
            extracted = source.extractfile(member)
            if extracted is None:
                raise fail(f"could not extract vendor archive file: {member.name}")
            with extracted, destination.open("xb") as output:
                shutil.copyfileobj(extracted, output)
            destination.chmod(member.mode & 0o777)


def tree_digest(vendor_root: Path) -> str:
    try:
        mode = vendor_root.lstat().st_mode
    except OSError as error:
        raise fail("vendor tree must contain vendor/modules.txt") from error
    if stat.S_ISLNK(mode) or not stat.S_ISDIR(mode) or not (vendor_root / "modules.txt").is_file():
        raise fail("vendor tree must contain vendor/modules.txt")
    digest = hashlib.sha256()
    for path in sorted(
        vendor_root.rglob("*"),
        key=lambda candidate: candidate.relative_to(vendor_root).as_posix(),
    ):
        if path.is_symlink():
            raise fail(f"vendor tree must not contain symlinks: {path}")
        if path.is_dir():
            continue
        if not path.is_file():
            raise fail(f"vendor tree contains an unsupported entry: {path}")
        relative_path = path.relative_to(vendor_root).as_posix().encode("utf-8")
        digest.update(b"F\0")
        digest.update(relative_path)
        digest.update(b"\0")
        digest.update(hashlib.sha256(path.read_bytes()).digest())
    return digest.hexdigest()


def expected_digest(source_root: Path) -> str:
    path = source_root / EXPECTED_DIGEST_PATH
    if not path.is_file():
        raise fail(f"source root is missing {EXPECTED_DIGEST_PATH}")
    value = path.read_text(encoding="utf-8").strip()
    if not re.fullmatch(r"[0-9a-f]{64}", value):
        raise fail(f"{EXPECTED_DIGEST_PATH} must contain one lowercase SHA256 digest")
    return value


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-root", required=True)
    parser.add_argument("--vendor-archive", required=True)
    args = parser.parse_args()
    try:
        source_root = require_test_tmpdir_child(args.source_root)
        extract_archive(source_root, Path(args.vendor_archive))
        actual = tree_digest(source_root / "vendor")
        expected = expected_digest(source_root)
        if actual != expected:
            raise fail(f"vendor tree digest mismatch: got {actual}, want {expected}")
    except (OSError, VendorArchiveError, tarfile.TarError) as error:
        print(f"extract_sbom_vendor.py: {error}", file=sys.stderr)
        return 1
    print(f"verified SBOM vendor tree {actual}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
