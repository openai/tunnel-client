from __future__ import annotations

from pathlib import Path


def has_cmd_client_segments(path: Path) -> bool:
    return ("cmd", "client") in zip(path.parts, path.parts[1:])


def find_bazel_client_binaries(root: Path) -> list[Path]:
    bazel_bin = root / "bazel-bin"
    if not bazel_bin.is_dir():
        return []
    candidates = [*bazel_bin.rglob("client"), *bazel_bin.rglob("client.exe")]
    return [
        candidate
        for candidate in sorted(candidates)
        if has_cmd_client_segments(candidate.relative_to(bazel_bin))
    ]
