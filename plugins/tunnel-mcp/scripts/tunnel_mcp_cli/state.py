from __future__ import annotations

import dataclasses
import datetime as dt
import json
import os
import re
import shlex
from pathlib import Path
from typing import Any

_ALIAS_CHARS = re.compile(r"[^a-z0-9-]+")
_SECRET_PATTERNS = [
    re.compile(r"sk-[A-Za-z0-9_-]{12,}"),
    re.compile(r"(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}"),
    re.compile(r"(?i)\bAuthorization\s*:"),
    re.compile(
        r"(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|password|secret)=\S+"
    ),
    re.compile(r"(?i)\b(OPENAI_ADMIN_KEY|OPENAI_API_KEY|CONTROL_PLANE_API_KEY)="),
    re.compile(r"://[^/\s:@]+:[^/\s:@]+@"),
]
_SECRET_FLAG_PATTERN = re.compile(
    r"(?i)^-{1,2}[a-z0-9_-]*(api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|password|secret)[a-z0-9_-]*$"
)
_SECRET_REFERENCE_PATTERN = re.compile(
    r"^(env:[A-Za-z_][A-Za-z0-9_]*|file:.+|\$[A-Za-z_][A-Za-z0-9_]*|\$\{[A-Za-z_][A-Za-z0-9_]*\})$"
)


class StateError(RuntimeError):
    pass


@dataclasses.dataclass(frozen=True)
class AliasRecord:
    alias: str
    tunnel_id: str
    name: str
    admin_profile: str = ""
    description: str = ""
    organization_ids: tuple[str, ...] = ()
    workspace_ids: tuple[str, ...] = ()
    tenant_ids: tuple[str, ...] = ()
    config_path: str = ""
    health_url_file: str = ""
    updated_at: str = ""

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "AliasRecord":
        return cls(
            alias=str(raw.get("alias", "")),
            tunnel_id=str(raw.get("tunnel_id", "")),
            name=str(raw.get("name", "")),
            admin_profile=str(raw.get("admin_profile", "")),
            description=str(raw.get("description", "")),
            organization_ids=tuple(str(v) for v in raw.get("organization_ids", [])),
            workspace_ids=tuple(str(v) for v in raw.get("workspace_ids", [])),
            tenant_ids=tuple(str(v) for v in raw.get("tenant_ids", [])),
            config_path=str(raw.get("config_path", "")),
            health_url_file=str(raw.get("health_url_file", "")),
            updated_at=str(raw.get("updated_at", "")),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "alias": self.alias,
            "tunnel_id": self.tunnel_id,
            "name": self.name,
            "admin_profile": self.admin_profile,
            "description": self.description,
            "organization_ids": list(self.organization_ids),
            "workspace_ids": list(self.workspace_ids),
            "tenant_ids": list(self.tenant_ids),
            "config_path": self.config_path,
            "health_url_file": self.health_url_file,
            "updated_at": self.updated_at,
        }


@dataclasses.dataclass(frozen=True)
class ProcessRecord:
    alias: str
    tunnel_id: str
    config_path: str
    health_url_file: str
    target_kind: str
    target_value: str
    command: str
    started_at: str
    admin_profile: str = ""
    mode: str = "tmux"
    session_name: str = ""
    pid: int = 0
    log_path: str = ""

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "ProcessRecord":
        return cls(
            alias=str(raw.get("alias", "")),
            tunnel_id=str(raw.get("tunnel_id", "")),
            admin_profile=str(raw.get("admin_profile", "")),
            mode=str(raw.get("mode", "tmux")),
            session_name=str(raw.get("session_name", "")),
            pid=int(raw.get("pid", 0) or 0),
            config_path=str(raw.get("config_path", "")),
            health_url_file=str(raw.get("health_url_file", "")),
            target_kind=str(raw.get("target_kind", "")),
            target_value=str(raw.get("target_value", "")),
            command=str(raw.get("command", "")),
            log_path=str(raw.get("log_path", "")),
            started_at=str(raw.get("started_at", "")),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "alias": self.alias,
            "tunnel_id": self.tunnel_id,
            "admin_profile": self.admin_profile,
            "mode": self.mode,
            "session_name": self.session_name,
            "pid": self.pid,
            "config_path": self.config_path,
            "health_url_file": self.health_url_file,
            "target_kind": self.target_kind,
            "target_value": self.target_value,
            "command": self.command,
            "log_path": self.log_path,
            "started_at": self.started_at,
        }


@dataclasses.dataclass(frozen=True)
class AdminProfile:
    name: str
    control_plane_base_url: str
    admin_key: str
    updated_at: str = ""

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> "AdminProfile":
        return cls(
            name=str(raw.get("name", "")),
            control_plane_base_url=str(raw.get("control_plane_base_url", "")),
            admin_key=str(raw.get("admin_key", "")),
            updated_at=str(raw.get("updated_at", "")),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "control_plane_base_url": self.control_plane_base_url,
            "admin_key": self.admin_key,
            "updated_at": self.updated_at,
        }


def utc_now() -> str:
    return dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def normalize_alias(alias: str) -> str:
    lowered = alias.strip().lower()
    lowered = lowered.replace("_", "-")
    lowered = _ALIAS_CHARS.sub("-", lowered)
    lowered = re.sub(r"-{2,}", "-", lowered).strip("-")
    if not lowered:
        raise StateError("alias must contain at least one ASCII letter or number")
    return lowered


def state_root() -> Path:
    codex_home = os.environ.get("CODEX_HOME")
    if codex_home:
        return Path(codex_home).expanduser() / "tunnel-mcp"
    return Path.home() / ".codex" / "tunnel-mcp"


def ensure_state_dirs(root: Path | None = None) -> Path:
    root = root or state_root()
    for subdir in (root, root / "configs", root / "health", root / "logs"):
        subdir.mkdir(parents=True, exist_ok=True)
    return root


def admin_profiles_path(root: Path | None = None) -> Path:
    return _path(root, "admin_profiles.yaml")


def load_aliases(root: Path | None = None) -> dict[str, AliasRecord]:
    raw = _load_json_map(_path(root, "aliases.yaml"))
    return {alias: AliasRecord.from_dict(value) for alias, value in raw.items()}


def save_aliases(records: dict[str, AliasRecord], root: Path | None = None) -> None:
    _save_json_map(_path(root, "aliases.yaml"), {k: records[k].to_dict() for k in sorted(records)})


def load_processes(root: Path | None = None) -> dict[str, ProcessRecord]:
    raw = _load_json_map(_path(root, "processes.yaml"))
    return {alias: ProcessRecord.from_dict(value) for alias, value in raw.items()}


def save_processes(records: dict[str, ProcessRecord], root: Path | None = None) -> None:
    _save_json_map(
        _path(root, "processes.yaml"), {k: records[k].to_dict() for k in sorted(records)}
    )


def load_admin_profiles(root: Path | None = None) -> dict[str, AdminProfile]:
    raw = _load_json_map(admin_profiles_path(root))
    profiles = raw.get("profiles", raw)
    if not isinstance(profiles, dict):
        raise StateError(f"state file {admin_profiles_path(root)} must contain a profiles object")
    return {
        name: AdminProfile.from_dict(value)
        for name, value in profiles.items()
        if isinstance(value, dict)
    }


def save_admin_profiles(
    records: dict[str, AdminProfile],
    root: Path | None = None,
    *,
    active_profile: str = "",
) -> None:
    _save_json_map(
        admin_profiles_path(root),
        {
            "active_profile": active_profile,
            "profiles": {k: records[k].to_dict() for k in sorted(records)},
        },
    )


def append_history(
    action: str, alias: str, tunnel_id: str | None, detail: str = "", root: Path | None = None
) -> None:
    path = _path(root, "history.md")
    path.parent.mkdir(parents=True, exist_ok=True)
    clean_detail = detail.replace("\n", " ").strip()
    line = f"- {utc_now()} action={action} alias={alias} tunnel_id={tunnel_id or '-'}"
    if clean_detail:
        line = f"{line} detail={clean_detail}"
    with path.open("a", encoding="utf-8") as f:
        f.write(line + "\n")


def reject_inline_secret_material(value: str, *, field: str) -> None:
    for pattern in _SECRET_PATTERNS:
        if pattern.search(value):
            raise StateError(
                f"{field} appears to contain inline secret material; use env or file references instead"
            )
    for token, next_token in _command_token_pairs(value):
        if not _SECRET_FLAG_PATTERN.match(token):
            continue
        if "=" in token:
            continue
        if next_token and not next_token.startswith("-") and not _is_secret_reference(next_token):
            raise StateError(
                f"{field} appears to contain inline secret material; use env or file references instead"
            )


def _command_token_pairs(value: str) -> list[tuple[str, str]]:
    try:
        tokens = shlex.split(value)
    except ValueError:
        tokens = value.split()
    return [
        (token, tokens[index + 1] if index + 1 < len(tokens) else "")
        for index, token in enumerate(tokens)
    ]


def _is_secret_reference(value: str) -> bool:
    return bool(_SECRET_REFERENCE_PATTERN.match(value))


def validate_secret_reference(value: str, *, field: str) -> None:
    if value.startswith(("env:", "file:")):
        return
    raise StateError(f"{field} must be an env:NAME or file:/path reference")


def alias_record_from_tunnel(
    *,
    alias: str,
    tunnel: dict[str, Any],
    admin_profile: str = "",
    description: str = "",
    config_path: str = "",
    health_url_file: str = "",
) -> AliasRecord:
    return AliasRecord(
        alias=alias,
        tunnel_id=str(tunnel.get("id", "")),
        name=str(tunnel.get("name", alias)),
        admin_profile=admin_profile,
        description=str(tunnel.get("description", description)),
        organization_ids=tuple(str(v) for v in tunnel.get("organization_ids", [])),
        workspace_ids=tuple(str(v) for v in tunnel.get("workspace_ids", [])),
        tenant_ids=tuple(str(v) for v in tunnel.get("tenant_ids", [])),
        config_path=config_path,
        health_url_file=health_url_file,
        updated_at=utc_now(),
    )


def _path(root: Path | None, name: str) -> Path:
    return ensure_state_dirs(root) / name


def _load_json_map(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise StateError(
            f"state file {path} is not valid JSON-compatible YAML; repair or remove it"
        ) from exc
    if not isinstance(data, dict):
        raise StateError(f"state file {path} must contain an object")
    return data


def _save_json_map(path: Path, data: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(tmp, 0o600)
    tmp.replace(path)
