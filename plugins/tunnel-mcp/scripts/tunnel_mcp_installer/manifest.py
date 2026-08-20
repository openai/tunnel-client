from __future__ import annotations

import json
import re
from pathlib import Path

FALLBACK_MANIFEST_PATHS = (
    Path("cursor.json"),
    Path(".cursor-plugin/plugin.json"),
    Path("claude.json"),
)
SAFE_PLUGIN_SEGMENT = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")


def validate_plugin_segment(value: str, *, field: str) -> str:
    normalized = value.strip()
    if not normalized:
        raise ValueError(f"{field} must be a non-empty string")
    valid = value == normalized and normalized not in {".", ".."}
    if not valid or not SAFE_PLUGIN_SEGMENT.fullmatch(normalized):
        raise ValueError(f"{field} has an invalid plugin cache segment")
    return normalized


def load_plugin_name(source: Path) -> str:
    manifest_path = source / ".codex-plugin" / "plugin.json"
    if not manifest_path.is_file():
        create_minimal_codex_manifest(source, manifest_path)

    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as err:
        raise ValueError(f"invalid plugin manifest: {manifest_path}: {err}") from err

    plugin_name = manifest.get("name")
    if not isinstance(plugin_name, str) or not plugin_name.strip():
        raise ValueError(f"plugin manifest must contain a non-empty string name: {manifest_path}")
    return validate_plugin_segment(plugin_name, field=f"plugin manifest name in {manifest_path}")


def create_minimal_codex_manifest(source: Path, manifest_path: Path) -> None:
    plugin_name = None

    for relative_path in FALLBACK_MANIFEST_PATHS:
        candidate = source / relative_path
        if not candidate.is_file():
            continue

        try:
            manifest = json.loads(candidate.read_text(encoding="utf-8"))
        except json.JSONDecodeError as err:
            raise ValueError(f"invalid plugin manifest: {candidate}: {err}") from err

        raw_plugin_name = manifest.get("name")
        if isinstance(raw_plugin_name, str) and raw_plugin_name.strip():
            plugin_name = validate_plugin_segment(
                raw_plugin_name, field=f"plugin manifest name in {candidate}"
            )
            break

    if plugin_name is None:
        raise ValueError(f"missing plugin manifest: {manifest_path}")

    manifest_path.parent.mkdir(parents=True, exist_ok=True)
    manifest_path.write_text(json.dumps({"name": plugin_name}, indent=2) + "\n", encoding="utf-8")
