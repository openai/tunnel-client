from __future__ import annotations

import argparse
import dataclasses
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any, Callable
from urllib.parse import urlparse

from tunnel_mcp_cli import runtime, state

Runner = Callable[..., subprocess.CompletedProcess[str]]
DEFAULT_BASE_URL = "https://api.openai.com"  # citadel-ignore: public endpoint example for external tunnel-client config
DEFAULT_ADMIN_PROFILE = "default"
DEFAULT_ADMIN_KEY_REF = "env:OPENAI_ADMIN_KEY"
DEFAULT_RUNTIME_API_KEY_REF = "env:CONTROL_PLANE_API_KEY"


class TunnelMCPError(RuntimeError):
    pass


class RemoteError(TunnelMCPError):
    def __init__(self, message: str, *, returncode: int):
        super().__init__(message)
        self.returncode = returncode


@dataclasses.dataclass(frozen=True)
class EffectiveAdminProfile:
    name: str
    control_plane_base_url: str
    admin_key: str
    path: str


def main(
    argv: list[str] | None = None,
    *,
    runner: Runner = subprocess.run,
    popen_factory: runtime.PopenFactory = subprocess.Popen,
) -> int:
    parser = _parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "create":
            payload = _create(args, runner)
        elif args.command == "connect":
            payload = _connect(args, runner, popen_factory)
        elif args.command == "list":
            payload = _list(args, runner)
        elif args.command == "status":
            payload = _status(args, runner)
        else:
            parser.print_help()
            return 2
    except state.StateError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except TunnelMCPError as exc:
        if args.command == "status" and str(exc).startswith("{"):
            print(str(exc))
            return 2
        print(f"error: {exc}", file=sys.stderr)
        return 1

    print(json.dumps(payload, indent=2, sort_keys=True))
    return 0


def _create(args: argparse.Namespace, runner: Runner) -> dict[str, Any]:
    alias = state.normalize_alias(args.alias)
    root = state.ensure_state_dirs()
    previous_alias = state.load_aliases(root).get(alias)
    admin_profile = _resolve_admin_profile(
        args,
        root,
        default_profile_name=previous_alias.admin_profile if previous_alias else "",
    )
    tunnel = _resolve_tunnel(
        alias,
        args,
        runner,
        admin_profile=admin_profile,
        root=root,
        create_if_missing=True,
    )
    aliases = state.load_aliases(root)
    aliases[alias] = state.alias_record_from_tunnel(
        alias=alias,
        tunnel=tunnel,
        admin_profile=admin_profile.name,
        description=args.description or _default_description(alias),
    )
    state.save_aliases(aliases, root)
    state.append_history(
        "create",
        alias,
        tunnel["id"],
        f"name={tunnel.get('name', alias)} admin_profile={admin_profile.name}",
        root,
    )
    return {
        "alias": alias,
        "tunnel": tunnel,
        "admin_profile": admin_profile.name,
        "admin_profile_path": admin_profile.path,
        "state_root": str(root),
    }


def _connect(
    args: argparse.Namespace, runner: Runner, popen_factory: runtime.PopenFactory
) -> dict[str, Any]:
    alias = state.normalize_alias(args.alias)
    root = state.ensure_state_dirs()
    target = _target_from_args(args)
    runtime_api_key = _runtime_api_key_ref(args)
    previous_alias = state.load_aliases(root).get(alias)
    admin_profile = _resolve_admin_profile(
        args,
        root,
        default_profile_name=previous_alias.admin_profile if previous_alias else "",
    )
    if args.tunnel_id:
        tunnel = _provided_tunnel(alias, args)
    else:
        tunnel = _resolve_tunnel(
            alias,
            args,
            runner,
            admin_profile=admin_profile,
            root=root,
            create_if_missing=True,
        )
    tunnel_id = str(tunnel["id"])
    replace_existing_runtime = bool(
        previous_alias and previous_alias.tunnel_id and previous_alias.tunnel_id != tunnel_id
    )

    config_path = runtime.write_runtime_config(
        alias=alias,
        tunnel_id=tunnel_id,
        base_url=admin_profile.control_plane_base_url,
        api_key=runtime_api_key,
        target=target,
        state_root=root,
    )
    health_url_file = runtime.config_health_url_file(config_path)

    aliases = state.load_aliases(root)
    aliases[alias] = state.alias_record_from_tunnel(
        alias=alias,
        tunnel=tunnel,
        admin_profile=admin_profile.name,
        description=args.description or _default_description(alias),
        config_path=str(config_path),
        health_url_file=str(health_url_file),
    )
    state.save_aliases(aliases, root)

    processes = state.load_processes(root)
    existing_process = processes.get(alias)
    if replace_existing_runtime and existing_process:
        state.append_history(
            "stale-process",
            alias,
            existing_process.tunnel_id,
            f"replacing with tunnel_id={tunnel_id}",
            root,
        )
    try:
        launch = runtime.start_or_reuse(
            alias=alias,
            config_path=config_path,
            tunnel_client_bin=args.tunnel_client_bin,
            state_root=root,
            runner=runner,
            popen_factory=popen_factory,
            existing_pid=existing_process.pid if existing_process else 0,
            replace_existing=replace_existing_runtime,
        )
    except RuntimeError as exc:
        raise TunnelMCPError(str(exc)) from exc

    processes[alias] = state.ProcessRecord(
        alias=alias,
        tunnel_id=tunnel["id"],
        admin_profile=admin_profile.name,
        config_path=str(config_path),
        health_url_file=str(health_url_file),
        target_kind=target.kind,
        target_value=target.value,
        command=launch.command,
        started_at=state.utc_now(),
        mode=launch.mode,
        session_name=launch.session_name,
        pid=launch.pid,
        log_path=launch.log_path,
    )
    state.save_processes(processes, root)
    state.append_history(
        "connect",
        alias,
        tunnel["id"],
        f"mode={launch.mode} session={launch.session_name or '-'} pid={launch.pid or '-'} started={launch.started}",
        root,
    )

    return {
        "alias": alias,
        "tunnel": tunnel,
        "admin_profile": admin_profile.name,
        "admin_profile_path": admin_profile.path,
        "config_path": str(config_path),
        "health_url_file": str(health_url_file),
        "mode": launch.mode,
        "session_name": launch.session_name,
        "pid": launch.pid,
        "log_path": launch.log_path,
        "started": launch.started,
        "already_running": launch.already_running,
    }


def _list(args: argparse.Namespace, runner: Runner) -> dict[str, Any]:
    root = state.ensure_state_dirs()
    admin_profile = _resolve_admin_profile(args, root)
    aliases = state.load_aliases(root)
    local_aliases = [record.to_dict() for _, record in sorted(aliases.items())]

    payload: dict[str, Any] = {
        "aliases": local_aliases,
        "admin_profile": admin_profile.name,
        "admin_profile_path": admin_profile.path,
        "state_root": str(root),
    }
    if _has_remote_scope(args):
        remote = _remote_list(args, runner, admin_profile)
        by_tunnel_id = {record.tunnel_id: record.alias for record in aliases.values()}
        by_tunnel_admin_profile = {
            record.tunnel_id: record.admin_profile for record in aliases.values()
        }
        merged = []
        for tunnel in remote:
            item = dict(tunnel)
            tunnel_id = str(tunnel.get("id", ""))
            item["local_alias"] = by_tunnel_id.get(tunnel_id)
            item["local_admin_profile"] = by_tunnel_admin_profile.get(tunnel_id)
            merged.append(item)
        payload["remote_tunnels"] = merged
    return payload


def _status(args: argparse.Namespace, runner: Runner) -> dict[str, Any]:
    alias = state.normalize_alias(args.alias)
    root = state.ensure_state_dirs()
    aliases = state.load_aliases(root)
    processes = state.load_processes(root)
    record = aliases.get(alias)
    if record is None:
        raise TunnelMCPError(f"alias {alias} is not known; run create or connect first")
    admin_profile = _resolve_admin_profile(args, root, default_profile_name=record.admin_profile)

    process = processes.get(alias)
    health_url = ""
    health_url_file = process.health_url_file if process else record.health_url_file
    if health_url_file:
        path = Path(health_url_file)
        if path.exists():
            health_url = path.read_text(encoding="utf-8").strip()

    tmux_running = runtime.tmux_has_session(alias, runner)
    process_running = runtime.pid_is_running(process.pid) if process and process.pid else False
    try:
        remote = _remote_get(record.tunnel_id, args, runner, admin_profile)
        stale = False
        error = ""
        repair_command = ""
    except RemoteError as exc:
        if not _is_not_found(exc):
            raise
        remote = None
        stale = True
        error = str(exc)
        repair_command = _repair_command(alias, record, process)

    payload = {
        "alias": alias,
        "tunnel_id": record.tunnel_id,
        "admin_profile": admin_profile.name,
        "admin_profile_path": admin_profile.path,
        "remote": remote,
        "stale": stale,
        "error": error,
        "repair_command": repair_command,
        "config_path": process.config_path if process else record.config_path,
        "health_url_file": health_url_file,
        "health_url": health_url,
        "tmux": {
            "session_name": runtime.tmux_session_name(alias),
            "running": tmux_running,
        },
        "process_running": process_running,
        "process": process.to_dict() if process else None,
    }
    if stale:
        raise TunnelMCPError(json.dumps(payload, indent=2, sort_keys=True))
    return payload


def _resolve_tunnel(
    alias: str,
    args: argparse.Namespace,
    runner: Runner,
    *,
    admin_profile: EffectiveAdminProfile,
    root: Path,
    create_if_missing: bool,
) -> dict[str, Any]:
    aliases = state.load_aliases(root)
    existing = aliases.get(alias)
    if existing and existing.tunnel_id:
        existing_admin_profile = _resolve_admin_profile(
            args, root, default_profile_name=existing.admin_profile
        )
        try:
            return _remote_get(existing.tunnel_id, args, runner, existing_admin_profile)
        except RemoteError as exc:
            if not _is_not_found(exc):
                raise
            state.append_history("stale-alias", alias, existing.tunnel_id, str(exc), root)
            aliases.pop(alias, None)
            state.save_aliases(aliases, root)

    scoped_remote = _find_matching_remote(alias, args, runner, admin_profile)
    if scoped_remote is not None:
        return scoped_remote

    if not create_if_missing:
        raise TunnelMCPError(f"alias {alias} is not known")
    if not args.organization_ids and not args.workspace_ids:
        raise TunnelMCPError("creating a tunnel requires --organization-id or --workspace-id")
    return _remote_create(alias, args, runner, admin_profile)


def _find_matching_remote(
    alias: str, args: argparse.Namespace, runner: Runner, admin_profile: EffectiveAdminProfile
) -> dict[str, Any] | None:
    if not _has_remote_scope(args):
        return None
    desired_name = args.name or alias
    try:
        for tunnel in _remote_list_for_lookup(args, runner, admin_profile):
            if str(tunnel.get("name", "")) == desired_name:
                return tunnel
    except RemoteError as exc:
        if not _is_not_found(exc):
            raise
    return None


def _remote_get(
    tunnel_id: str,
    args: argparse.Namespace,
    runner: Runner,
    admin_profile: EffectiveAdminProfile,
) -> dict[str, Any]:
    return _run_tunnel_client_json(args, runner, admin_profile, ["tunnels", "get", tunnel_id])


def _remote_create(
    alias: str, args: argparse.Namespace, runner: Runner, admin_profile: EffectiveAdminProfile
) -> dict[str, Any]:
    name = args.name or alias
    description = args.description or _default_description(alias)
    command = ["tunnels", "create", "--name", name, "--description", description]
    command.extend(_scope_flags_for_create(args))
    return _run_tunnel_client_json(args, runner, admin_profile, command)


def _remote_list(
    args: argparse.Namespace, runner: Runner, admin_profile: EffectiveAdminProfile
) -> list[dict[str, Any]]:
    command = ["tunnels", "list"]
    command.extend(_single_scope_filter(args))
    return _remote_list_with_command(args, runner, admin_profile, command)


def _remote_list_for_lookup(
    args: argparse.Namespace, runner: Runner, admin_profile: EffectiveAdminProfile
) -> list[dict[str, Any]]:
    command = ["tunnels", "list"]
    command.extend(_first_scope_filter(args))
    return _remote_list_with_command(args, runner, admin_profile, command)


def _remote_list_with_command(
    args: argparse.Namespace,
    runner: Runner,
    admin_profile: EffectiveAdminProfile,
    command: list[str],
) -> list[dict[str, Any]]:
    raw = _run_tunnel_client_json(args, runner, admin_profile, command)
    tunnels = raw.get("tunnels", [])
    if not isinstance(tunnels, list):
        raise TunnelMCPError("tunnel-client list returned malformed tunnels payload")
    return [t for t in tunnels if isinstance(t, dict)]


def _run_tunnel_client_json(
    args: argparse.Namespace,
    runner: Runner,
    admin_profile: EffectiveAdminProfile,
    admin_args: list[str],
) -> dict[str, Any]:
    command = [
        args.tunnel_client_bin,
        "admin",
        "--control-plane.base-url",
        admin_profile.control_plane_base_url,
        "--json",
        "--admin-key",
        admin_profile.admin_key,
    ]
    command.extend(admin_args)

    result = runner(command, check=False, capture_output=True, text=True)
    if result.returncode != 0:
        raise RemoteError(
            _failed_command("tunnel-client admin", result), returncode=result.returncode
        )
    try:
        payload = json.loads(result.stdout or "{}")
    except json.JSONDecodeError as exc:
        raise TunnelMCPError(f"tunnel-client returned invalid JSON: {exc}") from exc
    if not isinstance(payload, dict):
        raise TunnelMCPError("tunnel-client returned non-object JSON")
    return payload


def _scope_flags_for_create(args: argparse.Namespace) -> list[str]:
    flags: list[str] = []
    for org in args.organization_ids or []:
        flags.extend(["--organization-id", org])
    for workspace in args.workspace_ids or []:
        flags.extend(["--workspace-id", workspace])
    return flags


def _single_scope_filter(args: argparse.Namespace) -> list[str]:
    provided = [
        ("--organization-id", (args.organization_ids or [None])[0]),
        ("--workspace-id", (args.workspace_ids or [None])[0]),
        ("--tenant-id", args.tenant_id),
    ]
    non_empty = [(flag, value) for flag, value in provided if value]
    if len(non_empty) != 1:
        raise TunnelMCPError(
            "remote list requires exactly one of --organization-id, --workspace-id, or --tenant-id"
        )
    flag, value = non_empty[0]
    return [flag, value]


def _first_scope_filter(args: argparse.Namespace) -> list[str]:
    if args.organization_ids:
        return ["--organization-id", args.organization_ids[0]]
    if args.workspace_ids:
        return ["--workspace-id", args.workspace_ids[0]]
    if getattr(args, "tenant_id", ""):
        return ["--tenant-id", args.tenant_id]
    raise TunnelMCPError("remote lookup requires --organization-id, --workspace-id, or --tenant-id")


def _has_remote_scope(args: argparse.Namespace) -> bool:
    return bool(
        (args.organization_ids or [])
        or (args.workspace_ids or [])
        or getattr(args, "tenant_id", "")
    )


def _target_from_args(args: argparse.Namespace) -> runtime.RuntimeTarget:
    if args.mcp_server_url:
        parsed = urlparse(args.mcp_server_url)
        if parsed.scheme not in {"http", "https"} or not parsed.netloc:
            raise TunnelMCPError("--mcp-server-url must be an http or https URL")
        state.reject_inline_secret_material(args.mcp_server_url, field="mcp server URL")
        return runtime.RuntimeTarget(kind="server_url", value=args.mcp_server_url)
    if args.mcp_command:
        state.reject_inline_secret_material(args.mcp_command, field="mcp command")
        return runtime.RuntimeTarget(kind="command", value=args.mcp_command)
    raise TunnelMCPError("connect requires --mcp-server-url or --mcp-command")


def _provided_tunnel(alias: str, args: argparse.Namespace) -> dict[str, Any]:
    tunnel_id = str(args.tunnel_id)
    _validate_tunnel_id(tunnel_id)
    return {
        "id": tunnel_id,
        "name": args.name or alias,
        "description": args.description or _default_description(alias),
        "organization_ids": args.organization_ids or [],
        "workspace_ids": args.workspace_ids or [],
        "tenant_ids": [],
    }


def _validate_tunnel_id(tunnel_id: str) -> None:
    if not tunnel_id.startswith("tunnel_") or len(tunnel_id) <= len("tunnel_"):
        raise TunnelMCPError("--tunnel-id must look like tunnel_<id>")


def _runtime_api_key_ref(args: argparse.Namespace) -> str:
    api_key = args.runtime_api_key or DEFAULT_RUNTIME_API_KEY_REF
    state.validate_secret_reference(api_key, field="runtime api_key")
    return api_key


def _resolve_admin_profile(
    args: argparse.Namespace,
    root: Path,
    *,
    default_profile_name: str = "",
) -> EffectiveAdminProfile:
    requested_name = (
        args.admin_profile
        or default_profile_name
        or os.environ.get("TUNNEL_MCP_ADMIN_PROFILE")
        or DEFAULT_ADMIN_PROFILE
    )
    name = state.normalize_alias(requested_name)
    profiles = state.load_admin_profiles(root)
    existing = profiles.get(name)
    base_url = (
        args.control_plane_base_url
        or (existing.control_plane_base_url if existing else "")
        or DEFAULT_BASE_URL
    )
    admin_key = args.admin_key or (existing.admin_key if existing else "") or DEFAULT_ADMIN_KEY_REF
    state.validate_secret_reference(admin_key, field=f"admin profile {name} admin_key")

    if existing and existing.control_plane_base_url == base_url and existing.admin_key == admin_key:
        profile = existing
    else:
        profile = state.AdminProfile(
            name=name,
            control_plane_base_url=base_url,
            admin_key=admin_key,
            updated_at=state.utc_now(),
        )
        profiles[name] = profile
        state.save_admin_profiles(profiles, root, active_profile=name)
    return EffectiveAdminProfile(
        name=name,
        control_plane_base_url=base_url,
        admin_key=admin_key,
        path=str(state.admin_profiles_path(root)),
    )


def _default_description(alias: str) -> str:
    return f"MCP tunnel for {alias}"


def _failed_command(label: str, result: subprocess.CompletedProcess[str]) -> str:
    stdout = (result.stdout or "").strip()
    stderr = (result.stderr or "").strip()
    detail = stderr or stdout or f"exit {result.returncode}"
    return f"{label} failed: {detail}"


def _is_not_found(exc: RemoteError) -> bool:
    message = str(exc).lower()
    return "404" in message or "not found" in message or "no such tunnel" in message


def _repair_command(
    alias: str, record: state.AliasRecord, process: state.ProcessRecord | None
) -> str:
    command = ["scripts/tunnel_mcp", "connect", "--alias", alias]
    if record.admin_profile:
        command.extend(["--admin-profile", record.admin_profile])
    if record.organization_ids:
        command.extend(["--organization-id", record.organization_ids[0]])
    elif record.workspace_ids:
        command.extend(["--workspace-id", record.workspace_ids[0]])
    elif record.tenant_ids:
        command.extend(["--tenant-id", record.tenant_ids[0]])
    if process and process.target_kind == "server_url":
        command.extend(["--mcp-server-url", process.target_value])
    elif process and process.target_kind == "command":
        command.extend(["--mcp-command", process.target_value])
    else:
        command.append("<add --mcp-server-url or --mcp-command>")
    return " ".join(command)


def _parser() -> argparse.ArgumentParser:
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument(
        "--tunnel-client-bin", default=os.environ.get("TUNNEL_CLIENT_BIN", "tunnel-client")
    )
    common.add_argument(
        "--control-plane-base-url",
        default=os.environ.get("CONTROL_PLANE_BASE_URL"),
    )
    common.add_argument(
        "--admin-profile",
        help="Admin profile name from admin_profiles.yaml; defaults to TUNNEL_MCP_ADMIN_PROFILE or default",
    )
    common.add_argument(
        "--admin-key",
        default=os.environ.get("TUNNEL_MCP_ADMIN_KEY"),
        help="Admin key reference to store in the active admin profile, using env:NAME or file:/path",
    )
    common.add_argument(
        "--runtime-api-key",
        default=os.environ.get("TUNNEL_MCP_RUNTIME_API_KEY"),
        help="Runtime tunnel key reference for generated config control_plane.api_key, using env:NAME or file:/path",
    )
    common.add_argument("--organization-id", action="append", dest="organization_ids", default=[])
    common.add_argument("--workspace-id", action="append", dest="workspace_ids", default=[])

    parser = argparse.ArgumentParser(prog="tunnel_mcp")
    subcommands = parser.add_subparsers(dest="command", required=True)

    create = subcommands.add_parser("create", parents=[common])
    create.add_argument("--alias", required=True)
    create.add_argument("--name")
    create.add_argument("--description")
    create.set_defaults(tenant_id="")

    connect = subcommands.add_parser("connect", parents=[common])
    connect.add_argument("--alias", required=True)
    connect.add_argument("--name")
    connect.add_argument("--description")
    connect.add_argument("--tunnel-id", help="Attach to an existing tunnel id without admin CRUD")
    target = connect.add_mutually_exclusive_group(required=True)
    target.add_argument("--mcp-server-url")
    target.add_argument("--mcp-command")
    connect.set_defaults(tenant_id="")

    list_cmd = subcommands.add_parser("list", parents=[common])
    list_cmd.add_argument("--tenant-id", default="")
    list_cmd.set_defaults(alias="", name=None, description=None)

    status = subcommands.add_parser("status", parents=[common])
    status.add_argument("alias")
    status.set_defaults(tenant_id="", name=None, description=None)

    return parser
