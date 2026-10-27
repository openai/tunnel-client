#!/usr/bin/env python3
"""Exercise an installed Homebrew service on a disposable, logged-in macOS host.

Uses only loopback fixtures and a generated file-based credential. Refuses to
replace an existing service profile or launch agent. Keeps evidence in --output.
"""

from __future__ import annotations

import argparse
import hashlib
import http.client
import json
import os
import plistlib
import re
import secrets
import shlex
import signal
import subprocess
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


class Fixture(ThreadingHTTPServer):
    def __init__(self, key: str):
        super().__init__(("127.0.0.1", 0), Handler)
        self.key = key
        self.polls = 0
        self.condition = threading.Condition()
        self.stopping = threading.Event()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args: object) -> None:
        pass  # Never log authorization headers or keys.

    def do_GET(self) -> None:
        fixture = self.server
        assert isinstance(fixture, Fixture)
        if self.path.startswith("/v1/tunnels/") and "/poll" in self.path:
            if self.headers.get("Authorization") != "Bearer " + fixture.key:
                self.send_error(401)
                return
            # Model the control plane's empty long-poll response, without
            # dispatching or replaying any tool calls.
            fixture.stopping.wait(0.25)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            try:
                self.wfile.write(b'{"commands":[]}')
            except (BrokenPipeError, ConnectionResetError):
                return
            with fixture.condition:
                fixture.polls += 1
                fixture.condition.notify_all()
            return
        self.send_error(404)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--formula", required=True, help="Installed, fully qualified formula")
    parser.add_argument(
        "--output", type=Path, required=True, help="New directory for retained evidence"
    )
    args = parser.parse_args()
    require(
        sys.platform == "darwin" and os.getuid() != 0, "Run as a logged-in macOS user, without sudo"
    )
    args.output = args.output.absolute()
    args.output.mkdir(mode=0o700, parents=True, exist_ok=False)
    commands = (args.output / "commands.log").open("w")
    env = {**os.environ, "HOMEBREW_NO_AUTO_UPDATE": "1"}

    def run(*command: str, check: bool = True) -> subprocess.CompletedProcess[str]:
        print("$ " + shlex.join(command), file=commands, flush=True)
        result = subprocess.run(
            command, env=env, capture_output=True, text=True, timeout=90, check=False
        )
        print(result.stdout + result.stderr, file=commands, flush=True)
        if check:
            require(
                result.returncode == 0,
                f"Command failed ({result.returncode}): {shlex.join(command)}",
            )
        return result

    prefix = Path(run("brew", "--prefix").stdout.strip())
    opt = Path(run("brew", "--prefix", args.formula).stdout.strip())
    client = str(opt / "bin/tunnel-client")
    profile = prefix / "etc/tunnel-client.yaml"
    info = json.loads(run("brew", "services", "info", args.formula, "--json").stdout)[0]
    label = info["service_name"]
    plist = Path.home() / "Library/LaunchAgents" / (label + ".plist")
    job = f"gui/{os.getuid()}/{label}"
    logfile = prefix / "var/log/tunnel-client.log"
    log_offset = logfile.stat().st_size if logfile.exists() else 0
    require(not os.path.lexists(profile), f"Existing profile must be preserved: {profile}")
    for name in {label, "homebrew.mxcl.tunnel-client", "sh.brew.tunnel-client"}:
        existing = Path.home() / "Library/LaunchAgents" / (name + ".plist")
        require(
            not os.path.lexists(existing), f"Existing launch agent must be preserved: {existing}"
        )
        require(
            run("launchctl", "print", f"gui/{os.getuid()}/{name}", check=False).returncode != 0,
            "Service is already loaded",
        )
    require(
        run("launchctl", "print", job, check=False).returncode != 0, "Service is already loaded"
    )
    require(
        not run("pgrep", "-x", "tunnel-client", check=False).stdout.strip(),
        "Another tunnel-client is running",
    )
    fixture = Fixture(secrets.token_hex(24))
    thread = threading.Thread(target=fixture.serve_forever, daemon=True)
    thread.start()
    keyfile = args.output / "runtime.key"
    keyfile.touch(mode=0o600, exist_ok=False)
    keyfile.write_text(fixture.key)
    healthfile = args.output / "health.url"
    pidfile = args.output / "runtime.pid"
    profile_text = f"""config_version: 1
control_plane:
  base_url: http://127.0.0.1:{fixture.server_port}
  tunnel_id: tunnel_0123456789abcdef0123456789abcdef
  api_key: file:{keyfile}
  poll_channels: [harpoon]
harpoon:
  allow_plaintext_http: true
  targets:
    - label: homebrew-service-fixture
      url: http://127.0.0.1:{fixture.server_port}/target
health:
  listen_addr: 127.0.0.1:0
  url_file: {healthfile}
process:
  pid_file: {pidfile}
admin_ui:
  open_browser: false
log:
  format: json
"""
    expected_hash = hashlib.sha256(profile_text.encode()).hexdigest()
    created_profile = False
    service_attempted = False

    def unchanged() -> None:
        require(
            hashlib.sha256(profile.read_bytes()).hexdigest() == expected_hash,
            "Service changed profile bytes",
        )
        require(profile.stat().st_mode & 0o777 == 0o600, "Service changed profile permissions")

    def service_pid() -> int:
        state = run("launchctl", "print", job, check=False)
        match = re.search(r"^\s*pid = (\d+)\s*$", state.stdout, re.MULTILINE)
        return int(match[1]) if match else 0

    def ready(previous_pid: int = 0) -> int:
        deadline = time.monotonic() + 60
        with fixture.condition:
            prior_polls = fixture.polls
        last_error = "Service has not started"
        while time.monotonic() < deadline:
            try:
                pid = service_pid()
                require(pid > 0 and pid != previous_pid, "Waiting for a new launchd PID")
                require(int(pidfile.read_text().strip()) == pid, "PID file and launchd disagree")
                address = urlsplit(healthfile.read_text().strip())
                require(address.hostname == "127.0.0.1", "Health endpoint must stay on loopback")
                for endpoint in ("/healthz", "/readyz"):
                    connection = http.client.HTTPConnection("127.0.0.1", address.port, timeout=2)
                    try:
                        connection.request("GET", endpoint)
                        response = connection.getresponse()
                        require(response.status == 200, f"Unhealthy endpoint: {endpoint}")
                        response.read()
                    finally:
                        connection.close()
                with fixture.condition:
                    require(
                        fixture.polls > prior_polls,
                        "Waiting for an authenticated control-plane poll",
                    )
                state = run("ps", "-p", str(pid), "-o", "uid=,ppid=,comm=").stdout.split()
                require(
                    state[:2] == [str(os.getuid()), "1"],
                    "Service must belong to this user and launchd",
                )
                require(
                    Path(" ".join(state[2:])).resolve()
                    == (opt / "libexec/tunnel-client").resolve(),
                    "Unexpected service executable",
                )
                unchanged()
                run(
                    client,
                    "health",
                    "--url-file",
                    str(healthfile),
                    "--pid",
                    str(pid),
                    "--require-control-plane-poll",
                    "--json",
                )
                return pid
            except (OSError, ValueError, http.client.HTTPException, RuntimeError) as error:
                last_error = str(error)
            # Wait for fixture activity, with a deadline guard for launchd
            # transitions that do not produce a poll (including failed startup).
            with fixture.condition:
                fixture.condition.wait(timeout=min(0.25, max(0, deadline - time.monotonic())))
        raise RuntimeError(f"Readiness deadline: {last_error}")

    try:
        missing = run(client, "doctor", "--profile-file", str(profile), "--json", check=False)
        require(missing.returncode != 0, "doctor unexpectedly accepted a missing profile")
        with profile.open("x") as stream:
            os.chmod(profile, 0o600)
            created_profile = True
            stream.write(profile_text)
        run(client, "doctor", "--profile-file", str(profile), "--json")
        service_attempted = True
        run("brew", "services", "start", args.formula)
        first = ready()
        run("brew", "services", "info", args.formula, "--json")
        with plist.open("rb") as stream:
            definition = plistlib.load(stream)
        require(
            definition["ProgramArguments"]
            == [str(opt / "bin/tunnel-client"), "run", "--profile-file", str(profile)],
            "launchd must use the stable opt path and foreground run",
        )
        require(definition.get("KeepAlive") is True, "launchd must restart even after clean exit")
        require(definition.get("RunAtLoad") is True, "launchd must start at login")
        require(
            definition.get("StandardOutPath") == str(logfile), "Unexpected stdout log destination"
        )
        require(
            definition.get("StandardErrorPath") == str(logfile), "Unexpected stderr log destination"
        )
        (args.output / "service.plist").write_bytes(plist.read_bytes())
        run("brew", "services", "restart", args.formula)
        second = ready(first)
        require(
            run("ps", "-p", str(first), "-o", "pid=", check=False).returncode != 0,
            "Restart left old process running",
        )
        # Recheck ownership immediately before signaling exactly this PID.
        require(service_pid() == second, "Service PID changed before SIGTERM")
        require(
            run("ps", "-p", str(second), "-o", "uid=,ppid=").stdout.split()
            == [str(os.getuid()), "1"],
            "Refusing to signal a process not owned by this service",
        )
        print(f"$ kill -TERM {second}", file=commands, flush=True)
        os.kill(second, signal.SIGTERM)
        third = ready(second)
        unchanged()
        print(f"PASS start={first}, restart={second}, SIGTERM recovery={third}", flush=True)
    finally:
        # Stop before removing anything the service needs. A failed stop leaves
        # the profile and fixture evidence intact for manual recovery.
        try:
            if service_attempted:
                run("brew", "services", "stop", args.formula)
                require(
                    run("launchctl", "print", job, check=False).returncode != 0,
                    "Job remained loaded after stop",
                )
                require(not plist.exists(), "Login job remained enabled after stop")
                require(
                    not run("pgrep", "-x", "tunnel-client", check=False).stdout.strip(),
                    "Runtime remained after stop",
                )
            if created_profile:
                unchanged()
                (args.output / "profile.yaml").write_bytes(profile.read_bytes())
                profile.unlink()
            print("PASS stop, login job removal, and profile preservation", flush=True)
        finally:
            fixture.stopping.set()
            fixture.shutdown()
            fixture.server_close()
            thread.join(timeout=5)
            if logfile.exists():
                with logfile.open("rb") as stream:
                    stream.seek(log_offset)
                    (args.output / "service.log").write_bytes(stream.read())
            commands.close()


if __name__ == "__main__":
    main()
