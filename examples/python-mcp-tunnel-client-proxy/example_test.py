from __future__ import annotations

import os
import socket
import threading
import time
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path

import uvicorn
from example import (
    EXAMPLE_TUNNEL_ID,
    build_http_mcp_tunnel_proxy_command,
    build_stdio_mcp_tunnel_proxy_command,
    call_echo_tool_through_mcp_tunnel_proxy,
)
from fastmcp import FastMCP


def test_http_example_command_has_no_health_listener() -> None:
    args = build_http_mcp_tunnel_proxy_command(
        "tunnel-client",
        "http://127.0.0.1:3000/mcp",
    )

    assert args == [
        "tunnel-client",
        "dev",
        "proxy",
        "--tunnel-id",
        EXAMPLE_TUNNEL_ID,
        "--mcp-server-url",
        "http://127.0.0.1:3000/mcp",
        "--print-json",
    ]
    assert "--health-listen-addr" not in args


def test_stdio_example_command_quotes_command_arguments() -> None:
    args = build_stdio_mcp_tunnel_proxy_command(
        "tunnel-client",
        ["/usr/local/bin/example mcp", "--stdio", "--name", "Ada Lovelace"],
    )

    assert args[0:5] == [
        "tunnel-client",
        "dev",
        "proxy",
        "--tunnel-id",
        EXAMPLE_TUNNEL_ID,
    ]
    assert args[5] == "--mcp-command"
    assert args[7] == "--print-json"
    assert "example mcp" in args[6]
    assert "Ada Lovelace" in args[6]


def test_http_example_starts_fastmcp_server_and_calls_it_through_proxy() -> None:
    calls: list[str] = []

    with _started_fastmcp_echo_server(calls) as mcp_server_url:
        response = call_echo_tool_through_mcp_tunnel_proxy(
            [_tunnel_client_binary()],
            mcp_server_url,
            "Ada",
        )

    assert calls == ["Ada"]
    assert response["result"]["structuredContent"]["greeting"] == "hello Ada"


@contextmanager
def _started_fastmcp_echo_server(calls: list[str]) -> Iterator[str]:
    port = _free_port()
    mcp = FastMCP("mcp-tunnel-client-proxy-python-example")

    @mcp.tool()
    def echo(name: str) -> dict[str, str]:
        calls.append(name)
        return {"greeting": f"hello {name}"}

    server = uvicorn.Server(
        uvicorn.Config(
            mcp.http_app(path="/mcp"),
            host="127.0.0.1",
            port=port,
            log_level="warning",
        )
    )
    thread = threading.Thread(target=server.run, daemon=True)
    thread.start()
    try:
        _wait_for_tcp("127.0.0.1", port)
        yield f"http://127.0.0.1:{port}/mcp"
    finally:
        server.should_exit = True
        thread.join(timeout=5)


def _tunnel_client_binary() -> str:
    return _resolve_runfile(os.environ["MCP_TUNNEL_CLIENT_BIN"])


def _resolve_runfile(path: str) -> str:
    candidate = Path(path)
    if candidate.is_absolute() and candidate.exists():
        return str(candidate)
    roots = [Path.cwd()]
    for name in ("RUNFILES_DIR", "TEST_SRCDIR"):
        value = os.environ.get(name)
        if value:
            roots.append(Path(value))
            roots.append(Path(value) / "_main")
            workspace = os.environ.get("TEST_WORKSPACE")
            if workspace:
                roots.append(Path(value) / workspace)
    for root in roots:
        resolved = root / path
        if resolved.exists():
            return str(resolved)
    return path


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _wait_for_tcp(host: str, port: int) -> None:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            with socket.create_connection((host, port), timeout=0.2):
                return
        except OSError:
            time.sleep(0.05)
    raise TimeoutError(f"timed out waiting for {host}:{port}")
