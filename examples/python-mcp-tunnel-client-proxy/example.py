"""Copyable Python examples for the tunnel-client MCP proxy wrapper."""

from __future__ import annotations

import http.client
import json
import urllib.parse
from collections.abc import Mapping

from mcp_tunnel_client_proxy import (
    Command,
    MCPTunnelClientProxyOptions,
    MCPTunnelClientProxyProcess,
    mcp_tunnel_client_proxy_command,
    start_http_mcp_tunnel_proxy,
    start_stdio_mcp_tunnel_proxy,
)

EXAMPLE_TUNNEL_ID = "tunnel_11111111111111111111111111111111"


def build_http_mcp_tunnel_proxy_command(
    tunnel_client_binary: str,
    mcp_server_url: str,
) -> list[str]:
    return mcp_tunnel_client_proxy_command(
        MCPTunnelClientProxyOptions(
            command=[tunnel_client_binary],
            mcp_server_urls=[mcp_server_url],
            tunnel_id=EXAMPLE_TUNNEL_ID,
        )
    )


def build_stdio_mcp_tunnel_proxy_command(
    tunnel_client_binary: str,
    mcp_command: Command,
) -> list[str]:
    return mcp_tunnel_client_proxy_command(
        MCPTunnelClientProxyOptions(
            command=[tunnel_client_binary],
            mcp_commands=[mcp_command],
            tunnel_id=EXAMPLE_TUNNEL_ID,
        )
    )


def start_http_example(
    tunnel_client_binary_command: Command,
    mcp_server_url: str,
) -> MCPTunnelClientProxyProcess:
    return start_http_mcp_tunnel_proxy(tunnel_client_binary_command, mcp_server_url)


def start_stdio_example(
    tunnel_client_binary_command: Command,
    mcp_command: Command,
) -> MCPTunnelClientProxyProcess:
    return start_stdio_mcp_tunnel_proxy(tunnel_client_binary_command, mcp_command)


def call_echo_tool_through_mcp_tunnel_proxy(
    tunnel_client_binary_command: Command,
    mcp_server_url: str,
    name: str,
) -> Mapping[str, object]:
    with start_http_mcp_tunnel_proxy(tunnel_client_binary_command, mcp_server_url) as proxy:
        return call_echo_tool(proxy.info.mcp_url, name)


def call_echo_tool(mcp_url: str, name: str) -> Mapping[str, object]:
    _, initialize_headers = _post_jsonrpc(
        mcp_url,
        {
            "jsonrpc": "2.0",
            "id": "initialize-0",
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-06-18",
                "capabilities": {"sampling": {}, "roots": {"listChanged": True}},
                "clientInfo": {
                    "name": "mcp-tunnel-client-proxy-python-example",
                    "version": "0.0.1",
                },
            },
        },
    )
    session_id = initialize_headers.get("Mcp-Session-Id")
    if not session_id:
        raise RuntimeError("initialize response missing Mcp-Session-Id")

    session_headers = {"Mcp-Session-Id": session_id}
    _post_jsonrpc(
        mcp_url,
        {
            "jsonrpc": "2.0",
            "method": "notifications/initialized",
            "params": {},
        },
        headers=session_headers,
    )
    tool_response, _ = _post_jsonrpc(
        mcp_url,
        {
            "jsonrpc": "2.0",
            "id": "tool-1",
            "method": "tools/call",
            "params": {"name": "echo", "arguments": {"name": name}},
        },
        headers=session_headers,
    )
    return tool_response


def _post_jsonrpc(
    mcp_url: str,
    payload: Mapping[str, object],
    *,
    headers: Mapping[str, str] | None = None,
) -> tuple[Mapping[str, object], http.client.HTTPMessage]:
    request_headers = {
        "Accept": "application/json, text/event-stream",
        "Content-Type": "application/json",
    }
    request_headers.update(headers or {})
    parsed = urllib.parse.urlsplit(mcp_url)
    if not parsed.hostname:
        raise RuntimeError(f"MCP URL missing hostname: {mcp_url}")
    path = parsed.path or "/"
    if parsed.query:
        path = f"{path}?{parsed.query}"

    if parsed.scheme == "http":
        connection: http.client.HTTPConnection = http.client.HTTPConnection(
            parsed.hostname,
            parsed.port,
            timeout=15,
        )
    elif parsed.scheme == "https":
        connection = http.client.HTTPSConnection(
            parsed.hostname,
            parsed.port,
            timeout=15,
        )
    else:
        raise RuntimeError(f"MCP URL must use http or https: {mcp_url}")

    try:
        connection.request(
            "POST",
            path,
            body=json.dumps(payload).encode("utf-8"),
            headers=request_headers,
        )
        response = connection.getresponse()
        body = response.read()
        if response.status >= 400:
            raise RuntimeError(f"JSON-RPC request failed with HTTP {response.status}: {body!r}")
        if not body:
            return {}, response.headers
        if response.headers.get_content_type() == "text/event-stream":
            event_data = [
                line.removeprefix(b"data:").strip()
                for line in body.splitlines()
                if line.startswith(b"data:")
            ]
            if not event_data:
                raise RuntimeError(f"SSE JSON-RPC response missing data event: {body!r}")
            body = event_data[-1]
        decoded = json.loads(body)
        if not isinstance(decoded, Mapping):
            raise RuntimeError(f"JSON-RPC response must be an object: {decoded!r}")
        return decoded, response.headers
    finally:
        connection.close()
