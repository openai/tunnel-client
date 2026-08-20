"""Python wrapper for tests that need tunnel-client-driven MCP tunnels.

The local helper starts `tunnel-client dev proxy`, which includes a local
in-memory control plane. The remote helper starts `tunnel-client run` against a
caller-provided control plane and MCP server.
"""

from __future__ import annotations

import http.client
import json
import os
import queue
import shlex
import signal
import subprocess
import tempfile
import threading
import time
import urllib.parse
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import TextIO

Command = str | Sequence[str]


@dataclass(frozen=True)
class MCPTunnelClientProxyInfo:
    tunnel_id: str
    mcp_url: str
    control_plane_base_url: str
    control_plane_transport: str
    backend: str
    control_plane_unix_socket: str | None = None
    health_url: str | None = None

    @classmethod
    def from_json(cls, payload: Mapping[str, object]) -> "MCPTunnelClientProxyInfo":
        return cls(
            tunnel_id=_required_str(payload, "tunnel_id"),
            mcp_url=_required_str(payload, "mcp_url"),
            control_plane_base_url=_required_str(payload, "control_plane_base_url"),
            control_plane_transport=_required_str(payload, "control_plane_transport"),
            backend=_required_str(payload, "backend"),
            control_plane_unix_socket=_optional_str(payload, "control_plane_unix_socket"),
            health_url=_optional_str(payload, "health_url"),
        )


@dataclass(frozen=True)
class MCPTunnelClientProxyOptions:
    command: Command
    mcp_server_urls: Sequence[str] = ()
    mcp_commands: Sequence[Command] = ()
    tunnel_id: str | None = None
    url_file: str | None = None
    listen: str | None = None
    extra_args: Sequence[str] = ()
    env: Mapping[str, str] | None = None
    cwd: str | None = None
    startup_timeout_seconds: float = 10.0


@dataclass(frozen=True)
class MCPTunnelClientRunOptions:
    command: Command
    tunnel_id: str
    mcp_server_urls: Sequence[str] = ()
    mcp_commands: Sequence[Command] = ()
    control_plane_base_url: str | None = None
    control_plane_api_key_env: str = "CONTROL_PLANE_API_KEY"
    health_url_file: str | None = None
    health_listen_addr: str = "127.0.0.1:0"
    extra_args: Sequence[str] = ()
    env: Mapping[str, str] | None = None
    cwd: str | None = None
    startup_timeout_seconds: float = 10.0


@dataclass
class MCPTunnelClientProxyProcess:
    info: MCPTunnelClientProxyInfo
    process: subprocess.Popen[str]

    def stop(self, timeout_seconds: float = 5.0) -> None:
        _stop_process(self.process, timeout_seconds)

    def __enter__(self) -> "MCPTunnelClientProxyProcess":
        return self

    def __exit__(self, *unused: object) -> None:
        self.stop()


@dataclass
class MCPTunnelClientProcess:
    process: subprocess.Popen[str]
    health_url: str | None = None
    _temporary_health_dir: tempfile.TemporaryDirectory[str] | None = None

    def stop(self, timeout_seconds: float = 5.0) -> None:
        _stop_process(self.process, timeout_seconds)
        if self._temporary_health_dir is not None:
            self._temporary_health_dir.cleanup()
            self._temporary_health_dir = None

    def __enter__(self) -> "MCPTunnelClientProcess":
        return self

    def __exit__(self, *unused: object) -> None:
        self.stop()


def start_local_mcp_tunnel_proxy(
    options: MCPTunnelClientProxyOptions,
) -> MCPTunnelClientProxyProcess:
    args = mcp_tunnel_client_proxy_command(options)
    env = os.environ.copy()
    if options.env:
        env.update(options.env)
    process = subprocess.Popen(
        args,
        cwd=options.cwd,
        env=env,
        stdout=subprocess.PIPE,
        stderr=None,
        text=True,
    )
    try:
        if process.stdout is None:
            raise RuntimeError("tunnel-client stdout pipe was not created")
        info = MCPTunnelClientProxyInfo.from_json(
            _read_json_object(process.stdout, options.startup_timeout_seconds)
        )
        return MCPTunnelClientProxyProcess(info=info, process=process)
    except Exception:
        _terminate_failed_start(process)
        raise


def start_mcp_tunnel_client(options: MCPTunnelClientRunOptions) -> MCPTunnelClientProcess:
    health_dir: tempfile.TemporaryDirectory[str] | None = None
    health_url_file = options.health_url_file
    if health_url_file is None:
        health_dir = tempfile.TemporaryDirectory(prefix="mcp-tunnel-client-")
        health_url_file = str(Path(health_dir.name) / "health.url")

    args = mcp_tunnel_client_run_command(options, health_url_file=health_url_file)
    env = os.environ.copy()
    if options.env:
        env.update(options.env)
    process = subprocess.Popen(
        args,
        cwd=options.cwd,
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=None,
        text=True,
    )
    try:
        health_url = _wait_for_health_url(
            process,
            health_url_file,
            options.startup_timeout_seconds,
        )
        return MCPTunnelClientProcess(
            process=process,
            health_url=health_url,
            _temporary_health_dir=health_dir,
        )
    except Exception:
        if health_dir is not None:
            health_dir.cleanup()
        _terminate_failed_start(process)
        raise


def mcp_tunnel_client_proxy_command(options: MCPTunnelClientProxyOptions) -> list[str]:
    args = _command_prefix(options.command) + ["dev", "proxy"]
    if options.listen:
        args.extend(["--listen", options.listen])
    if options.tunnel_id:
        args.extend(["--tunnel-id", options.tunnel_id])
    for mcp_server_url in options.mcp_server_urls:
        args.extend(["--mcp-server-url", mcp_server_url])
    for mcp_command in options.mcp_commands:
        args.extend(["--mcp-command", _mcp_command_arg(mcp_command)])
    if options.url_file:
        args.extend(["--url-file", options.url_file])
    args.extend(options.extra_args)
    args.append("--print-json")
    return args


def mcp_tunnel_client_run_command(
    options: MCPTunnelClientRunOptions,
    *,
    health_url_file: str | None = None,
) -> list[str]:
    args = _command_prefix(options.command) + ["run"]
    if options.control_plane_base_url:
        args.extend(["--control-plane.base-url", options.control_plane_base_url])
    args.extend(["--control-plane.tunnel-id", options.tunnel_id])
    if options.control_plane_api_key_env:
        args.extend(["--control-plane.api-key", f"env:{options.control_plane_api_key_env}"])
    for mcp_server_url in options.mcp_server_urls:
        args.extend(["--mcp-server-url", mcp_server_url])
    for mcp_command in options.mcp_commands:
        args.extend(["--mcp-command", _mcp_command_arg(mcp_command)])
    if options.health_listen_addr:
        args.extend(["--health.listen-addr", options.health_listen_addr])
    url_file = health_url_file or options.health_url_file
    if url_file:
        args.extend(["--health.url-file", url_file])
    args.extend(options.extra_args)
    return args


def start_http_mcp_tunnel_proxy(
    command: Command,
    mcp_server_url: str,
) -> MCPTunnelClientProxyProcess:
    return start_local_mcp_tunnel_proxy(
        MCPTunnelClientProxyOptions(command=command, mcp_server_urls=[mcp_server_url])
    )


def start_stdio_mcp_tunnel_proxy(
    command: Command,
    mcp_command: Command,
) -> MCPTunnelClientProxyProcess:
    return start_local_mcp_tunnel_proxy(
        MCPTunnelClientProxyOptions(command=command, mcp_commands=[mcp_command])
    )


def _command_prefix(command: Command) -> list[str]:
    if isinstance(command, str):
        return [command]
    if not command:
        raise ValueError("tunnel-client command must not be empty")
    return [str(part) for part in command]


def _mcp_command_arg(command: Command) -> str:
    if isinstance(command, str):
        return command
    if not command:
        raise ValueError("MCP command must not be empty")
    return shlex.join(str(part) for part in command)


def _read_json_object(stream: TextIO, timeout_seconds: float) -> Mapping[str, object]:
    lines: queue.Queue[str | None] = queue.Queue()

    def read_lines() -> None:
        try:
            for line in stream:
                lines.put(line)
        finally:
            lines.put(None)

    threading.Thread(target=read_lines, daemon=True).start()
    deadline = time.monotonic() + timeout_seconds
    body: list[str] = []
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("timed out waiting for tunnel-client proxy JSON")
        try:
            line = lines.get(timeout=remaining)
        except queue.Empty as exc:
            raise TimeoutError("timed out waiting for tunnel-client proxy JSON") from exc
        if line is None:
            raise RuntimeError("tunnel-client exited before printing connection JSON")
        if not body and not line.lstrip().startswith("{"):
            continue
        body.append(line)
        try:
            decoded = json.loads("".join(body))
        except json.JSONDecodeError:
            continue
        if not isinstance(decoded, Mapping):
            raise RuntimeError("tunnel-client proxy JSON must be an object")
        return decoded


def _wait_for_health_url(
    process: subprocess.Popen[str],
    health_url_file: str,
    timeout_seconds: float,
) -> str:
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("tunnel-client exited before publishing health URL")
        try:
            raw_url = Path(health_url_file).read_text(encoding="utf-8").strip()
        except FileNotFoundError:
            time.sleep(0.05)
            continue
        if raw_url and _health_ready(raw_url):
            return raw_url
        time.sleep(0.05)
    raise TimeoutError("timed out waiting for tunnel-client health URL")


def _health_ready(base_url: str) -> bool:
    parsed = urllib.parse.urlsplit(base_url.rstrip("/") + "/readyz")
    if parsed.scheme != "http" or not parsed.hostname:
        return False
    connection = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=0.5)
    try:
        path = parsed.path or "/readyz"
        if parsed.query:
            path = f"{path}?{parsed.query}"
        connection.request("GET", path)
        response = connection.getresponse()
        response.read()
        return 200 <= response.status < 300
    except OSError:
        return False
    finally:
        connection.close()


def _stop_process(process: subprocess.Popen[str], timeout_seconds: float) -> None:
    if process.poll() is not None:
        return
    if os.name == "nt":
        process.terminate()
    else:
        process.send_signal(signal.SIGTERM)
    try:
        process.wait(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=timeout_seconds)


def _terminate_failed_start(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=2)


def _required_str(payload: Mapping[str, object], key: str) -> str:
    value = payload.get(key)
    if not isinstance(value, str) or value == "":
        raise ValueError(f"tunnel-client proxy JSON missing {key!r}")
    return value


def _optional_str(payload: Mapping[str, object], key: str) -> str | None:
    value = payload.get(key)
    if value is None:
        return None
    if not isinstance(value, str):
        raise ValueError(f"tunnel-client proxy JSON field {key!r} must be a string")
    return value
