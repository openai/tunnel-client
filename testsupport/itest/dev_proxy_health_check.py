from __future__ import annotations

import argparse
import http.client
import json
import socket
import urllib.parse
from pathlib import Path
from typing import Any


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
        super().__init__("localhost", timeout=timeout_seconds)
        self._socket_path = socket_path

    def connect(self) -> None:
        connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        connection.settimeout(self.timeout)
        connection.connect(self._socket_path)
        self.sock = connection


def _load_proxy_info(path: Path) -> dict[str, Any]:
    parsed = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(parsed, dict):
        raise ValueError(f"Expected JSON object in proxy info file: {path}")
    return parsed


def _request_readyz(info: dict[str, Any], *, timeout_seconds: float) -> int:
    transport = info.get("mcp_transport")
    if transport == "unix":
        socket_path = info.get("mcp_unix_socket")
        if not isinstance(socket_path, str) or not socket_path:
            raise ValueError("Unix proxy info missing mcp_unix_socket")
        connection: http.client.HTTPConnection = UnixHTTPConnection(
            socket_path,
            timeout_seconds=timeout_seconds,
        )
    elif transport == "tcp":
        mcp_url = info.get("mcp_url")
        if not isinstance(mcp_url, str) or not mcp_url:
            raise ValueError("TCP proxy info missing mcp_url")
        parsed = urllib.parse.urlsplit(mcp_url)
        if parsed.scheme != "http" or not parsed.hostname:
            raise ValueError(f"Unsupported TCP proxy mcp_url: {mcp_url}")
        connection = http.client.HTTPConnection(
            parsed.hostname,
            parsed.port,
            timeout=timeout_seconds,
        )
    else:
        raise ValueError(f"Unsupported proxy transport: {transport!r}")

    try:
        connection.request("GET", "/readyz")
        response = connection.getresponse()
        response.read()
        return response.status
    finally:
        connection.close()


def main() -> None:
    parser = argparse.ArgumentParser(description="Check tunnel-client dev proxy readiness.")
    parser.add_argument("--proxy-info-file", type=Path, required=True)
    parser.add_argument("--timeout-seconds", type=float, default=2.0)
    args = parser.parse_args()

    status = _request_readyz(
        _load_proxy_info(args.proxy_info_file),
        timeout_seconds=args.timeout_seconds,
    )
    if 200 <= status < 400:
        return
    raise RuntimeError(f"Dev proxy readiness failed with HTTP status {status}")


if __name__ == "__main__":
    main()
