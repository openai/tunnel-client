from __future__ import annotations

import json
import sys
from pathlib import Path

from mcp_tunnel_client_proxy import (
    MCPTunnelClientProxyOptions,
    MCPTunnelClientRunOptions,
    mcp_tunnel_client_proxy_command,
    mcp_tunnel_client_run_command,
    start_http_mcp_tunnel_proxy,
    start_stdio_mcp_tunnel_proxy,
)


def test_http_wrapper_starts_local_proxy_without_health_listener(tmp_path: Path) -> None:
    arg_file = tmp_path / "args.json"
    fake_client = _fake_proxy_script(tmp_path)

    with start_http_mcp_tunnel_proxy(
        [sys.executable, str(fake_client)],
        "http://127.0.0.1:3000/mcp",
    ) as proxy:
        assert proxy.info.mcp_url.endswith("/v1/mcp/tunnel_python_example")
        assert proxy.info.control_plane_transport == "unix"
        assert proxy.info.health_url is None

    args = json.loads(arg_file.read_text())
    assert args == [
        "dev",
        "proxy",
        "--mcp-server-url",
        "http://127.0.0.1:3000/mcp",
        "--print-json",
    ]
    assert "--health-listen-addr" not in args


def test_stdio_wrapper_quotes_command_arguments(tmp_path: Path) -> None:
    arg_file = tmp_path / "args.json"
    fake_client = _fake_proxy_script(tmp_path)

    with start_stdio_mcp_tunnel_proxy(
        [sys.executable, str(fake_client)],
        ["/usr/local/bin/example mcp", "--stdio", "--name", "Ada Lovelace"],
    ) as proxy:
        assert proxy.info.backend == "go-in-memory"

    args = json.loads(arg_file.read_text())
    assert args[0:2] == ["dev", "proxy"]
    assert args[2] == "--mcp-command"
    assert args[4] == "--print-json"
    assert "example mcp" in args[3]
    assert "Ada Lovelace" in args[3]


def test_local_command_builder_supports_url_file_and_extra_args() -> None:
    args = mcp_tunnel_client_proxy_command(
        MCPTunnelClientProxyOptions(
            command=["tunnel-client"],
            mcp_server_urls=["url=http://127.0.0.1:3000/mcp,channel=tools"],
            tunnel_id="tunnel_test",
            url_file="/tmp/local-proxy.json",
            extra_args=["--response-timeout", "5s"],
        )
    )
    assert args == [
        "tunnel-client",
        "dev",
        "proxy",
        "--tunnel-id",
        "tunnel_test",
        "--mcp-server-url",
        "url=http://127.0.0.1:3000/mcp,channel=tools",
        "--url-file",
        "/tmp/local-proxy.json",
        "--response-timeout",
        "5s",
        "--print-json",
    ]


def test_remote_run_command_uses_api_key_env_reference() -> None:
    args = mcp_tunnel_client_run_command(
        MCPTunnelClientRunOptions(
            command=["tunnel-client"],
            control_plane_base_url="https://control-plane.example",
            control_plane_api_key_env="TEST_CONTROL_PLANE_API_KEY",
            tunnel_id="tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            mcp_server_urls=["https://mcp.example/mcp"],
        ),
        health_url_file="/tmp/tunnel-client-health.url",
    )
    assert args == [
        "tunnel-client",
        "run",
        "--control-plane.base-url",
        "https://control-plane.example",
        "--control-plane.tunnel-id",
        "tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "--control-plane.api-key",
        "env:TEST_CONTROL_PLANE_API_KEY",
        "--mcp-server-url",
        "https://mcp.example/mcp",
        "--health.listen-addr",
        "127.0.0.1:0",
        "--health.url-file",
        "/tmp/tunnel-client-health.url",
    ]


def _fake_proxy_script(tmp_path: Path) -> Path:
    script = tmp_path / "fake_tunnel_client.py"
    arg_file = tmp_path / "args.json"
    script.write_text(
        f"""
import json
import signal
import sys
import time

open({str(arg_file)!r}, "w", encoding="utf-8").write(json.dumps(sys.argv[1:]))
print(json.dumps({{
    "tunnel_id": "tunnel_python_example",
    "mcp_url": "http://127.0.0.1:18081/v1/mcp/tunnel_python_example",
    "control_plane_base_url": "http://tunnel-client-local-proxy",
    "control_plane_transport": "unix",
    "control_plane_unix_socket": "/tmp/local-proxy/control.sock",
    "backend": "go-in-memory",
}}, indent=2), flush=True)

running = True
def stop(signum, frame):
    global running
    running = False

signal.signal(signal.SIGTERM, stop)
while running:
    time.sleep(0.01)
"""
    )
    return script
