from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

from tunnel_mcp_installer.core import DEFAULT_MARKETPLACE, install_plugin
from tunnel_mcp_installer.manifest import validate_plugin_segment


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Install this local Codex plugin into the plugin cache."
    )
    parser.add_argument(
        "--source",
        help="Path to the source plugin directory. Default: plugin containing this script.",
    )
    parser.add_argument(
        "--marketplace",
        default=DEFAULT_MARKETPLACE,
        help=f"Marketplace name used in cache paths. Default: {DEFAULT_MARKETPLACE}",
    )
    parser.add_argument(
        "--codex-home",
        help="Override CODEX_HOME. Default: $CODEX_HOME, then ~/.codex.",
    )
    parser.add_argument(
        "--tunnel-client-bin",
        help="Path to the tunnel-client binary persisted into the installed bundle.",
    )
    return parser.parse_args()


def default_source() -> Path:
    return Path(__file__).resolve().parents[2]


def resolve_codex_home(arg_value: str | None) -> Path:
    if arg_value:
        return Path(arg_value).expanduser().resolve()
    env_value = os.environ.get("CODEX_HOME")
    if env_value:
        return Path(env_value).expanduser().resolve()
    return (Path.home() / ".codex").resolve()


def main() -> int:
    args = parse_args()
    source = Path(args.source).expanduser().resolve() if args.source else default_source()
    codex_home = resolve_codex_home(args.codex_home)
    marketplace = validate_plugin_segment(
        args.marketplace.strip() or DEFAULT_MARKETPLACE,
        field="--marketplace",
    )

    try:
        plugin_name, target, config_path, binary_hint = install_plugin(
            source,
            codex_home,
            marketplace,
            tunnel_client_bin=args.tunnel_client_bin,
        )
    except ValueError as err:
        print(str(err), file=sys.stderr)
        return 1

    print(f"Installed {plugin_name}@{marketplace}")
    print(f"Target: {target}")
    print(f"Config: {config_path}")
    if binary_hint is not None:
        print(f"Tunnel client: {binary_hint}")
    return 0
