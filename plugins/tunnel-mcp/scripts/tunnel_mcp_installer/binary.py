from __future__ import annotations

import os
import shutil
from pathlib import Path

from tunnel_mcp_installer.bazel_binary import find_bazel_client_binaries

BIN_HINT_FILENAME = ".tunnel-client-bin"
BINARY_RELATIVE_PATHS = (
    Path("tunnel-client"),
    Path("tunnel-client.exe"),
    Path("bin/tunnel-client"),
    Path("bin/tunnel-client.exe"),
    Path("bazel-bin/cmd/client/client"),
    Path("bazel-bin/cmd/client/client.exe"),
    Path("bazel-bin/api/tunnel-client/cmd/client/client"),
    Path("bazel-bin/api/tunnel-client/cmd/client/client.exe"),
)


def tunnel_client_bin_hint_path(root: Path) -> Path:
    return root / BIN_HINT_FILENAME


def read_tunnel_client_bin_hint(root: Path) -> Path | None:
    hint_path = tunnel_client_bin_hint_path(root)
    if not hint_path.is_file():
        return None
    value = hint_path.read_text(encoding="utf-8").strip()
    if not value:
        return None
    return Path(value).expanduser()


def is_executable_file(path: Path) -> bool:
    return path.is_file() and os.access(path, os.X_OK)


def validate_tunnel_client_bin(path_value: str, *, field: str) -> Path:
    candidate = Path(path_value).expanduser().resolve()
    if not is_executable_file(candidate):
        raise ValueError(f"{field} is not an executable file: {candidate}")
    return candidate


def tunnel_client_bin_candidates(source: Path) -> list[Path]:
    candidates: list[Path] = []
    seen: set[str] = set()
    roots = [source, *source.parents]
    for root in roots:
        root_candidates = list(map(root.__truediv__, BINARY_RELATIVE_PATHS))
        root_candidates.extend(find_bazel_client_binaries(root))
        for candidate in root_candidates:
            key = str(candidate)
            if key not in seen:
                seen.add(key)
                candidates.append(candidate)
    return candidates


def discover_tunnel_client_bin(source: Path, explicit: str | None) -> Path | None:
    if explicit and explicit.strip():
        return validate_tunnel_client_bin(explicit.strip(), field="--tunnel-client-bin")

    env_value = os.environ.get("TUNNEL_CLIENT_BIN", "").strip()
    if env_value:
        return validate_tunnel_client_bin(env_value, field="TUNNEL_CLIENT_BIN")

    hinted = read_tunnel_client_bin_hint(source)
    if hinted is not None and is_executable_file(hinted):
        return hinted.resolve()

    for candidate in tunnel_client_bin_candidates(source):
        if is_executable_file(candidate):
            return candidate.resolve()

    for path_name in ("tunnel-client", "tunnel-client.exe"):
        discovered = shutil.which(path_name)
        if discovered:
            return Path(discovered).expanduser().resolve()
    return None
