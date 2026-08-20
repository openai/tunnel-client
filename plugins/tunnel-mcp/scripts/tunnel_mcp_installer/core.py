from __future__ import annotations

import shutil
from pathlib import Path

from tunnel_mcp_installer.binary import (
    discover_tunnel_client_bin,
    tunnel_client_bin_hint_path,
)
from tunnel_mcp_installer.config import update_config
from tunnel_mcp_installer.manifest import load_plugin_name

DEFAULT_MARKETPLACE = "debug"
DEFAULT_VERSION = "local"


def install_plugin(
    source: Path,
    codex_home: Path,
    marketplace: str,
    *,
    tunnel_client_bin: str | None = None,
) -> tuple[str, Path, Path, Path | None]:
    if not source.is_dir():
        raise ValueError(f"plugin source path is not a directory: {source}")

    plugin_name = load_plugin_name(source)
    target = codex_home / "plugins" / "cache" / marketplace / plugin_name / DEFAULT_VERSION
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists():
        shutil.rmtree(target)
    shutil.copytree(source, target)

    resolved_tunnel_client_bin = discover_tunnel_client_bin(source, tunnel_client_bin)
    if resolved_tunnel_client_bin is not None:
        tunnel_client_bin_hint_path(target).write_text(
            str(resolved_tunnel_client_bin) + "\n", encoding="utf-8"
        )

    config_path = codex_home / "config.toml"
    update_config(config_path, plugin_name, marketplace)
    return plugin_name, target, config_path, resolved_tunnel_client_bin
