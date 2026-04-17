from __future__ import annotations

import contextlib
import io
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

PLUGIN_ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPTS_DIR = PLUGIN_ROOT / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

from tunnel_mcp_cli import commands, runtime, state  # noqa: E402


class FakeRunner:
    def __init__(self, *, tmux_installed: bool = True) -> None:
        self.calls: list[list[str]] = []
        self.remote: dict[str, dict[str, object]] = {}
        self.sessions: set[str] = set()
        self.next_id = 1
        self.tmux_installed = tmux_installed

    def __call__(self, args: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
        self.calls.append(list(args))
        if args == ["tmux", "-V"]:
            if not self.tmux_installed:
                raise FileNotFoundError("tmux")
            return _completed(args, 0, stdout="tmux 3.5\n")
        if args[:2] == ["tmux", "has-session"]:
            if not self.tmux_installed:
                raise FileNotFoundError("tmux")
            target = args[-1].removeprefix("=")
            return _completed(args, 0 if target in self.sessions else 1)
        if args[:4] == ["tmux", "new-session", "-d", "-s"]:
            if not self.tmux_installed:
                raise FileNotFoundError("tmux")
            self.sessions.add(args[4])
            return _completed(args, 0)
        if args[:3] == ["tmux", "kill-session", "-t"]:
            if not self.tmux_installed:
                raise FileNotFoundError("tmux")
            target = args[-1].removeprefix("=")
            if target not in self.sessions:
                return _completed(args, 1, stderr="can't find session")
            self.sessions.remove(target)
            return _completed(args, 0)
        if len(args) >= 7 and args[1] == "admin":
            return self._admin(args)
        return _completed(args, 99, stderr="unexpected command")

    def _admin(self, args: list[str]) -> subprocess.CompletedProcess[str]:
        idx = args.index("tunnels")
        subcommand = args[idx + 1]
        if subcommand == "get":
            tunnel_id = args[idx + 2]
            tunnel = self.remote.get(tunnel_id)
            if tunnel is None:
                return _completed(
                    args, 1, stderr="request GET /v1/tunnels/%s failed: 404 not found" % tunnel_id
                )
            return _completed(args, 0, stdout=json.dumps(tunnel))
        if subcommand == "list":
            return _completed(args, 0, stdout=json.dumps({"tunnels": list(self.remote.values())}))
        if subcommand == "create":
            tunnel_id = f"tunnel_{self.next_id:032x}"
            self.next_id += 1
            tunnel = {
                "id": tunnel_id,
                "name": _value_after(args, "--name"),
                "description": _value_after(args, "--description"),
                "organization_ids": _values_after(args, "--organization-id"),
                "workspace_ids": _values_after(args, "--workspace-id"),
                "tenant_ids": [],
            }
            self.remote[tunnel_id] = tunnel
            return _completed(args, 0, stdout=json.dumps(tunnel))
        return _completed(args, 99, stderr="unexpected admin command")


class FakePopenFactory:
    def __init__(self) -> None:
        self.calls: list[list[str]] = []

    def __call__(self, args: list[str], **kwargs: object) -> object:
        self.calls.append(list(args))

        class FakeProcess:
            pid = 43210

        return FakeProcess()


class TunnelMCPTest(unittest.TestCase):
    def setUp(self) -> None:
        self._env = os.environ.copy()
        self.temp = tempfile.TemporaryDirectory()
        os.environ["CODEX_HOME"] = self.temp.name

    def tearDown(self) -> None:
        os.environ.clear()
        os.environ.update(self._env)
        self.temp.cleanup()

    def test_plugin_entrypoint_help_executes(self) -> None:
        result = subprocess.run(
            [sys.executable, str(SCRIPTS_DIR / "tunnel_mcp"), "--help"],
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("create", result.stdout)
        self.assertIn("connect", result.stdout)

    def test_plugin_entrypoint_runs_from_standalone_copy(self) -> None:
        standalone_root = pathlib.Path(self.temp.name) / "standalone-plugin"
        shutil.copytree(
            PLUGIN_ROOT,
            standalone_root,
            ignore=shutil.ignore_patterns("__pycache__", "*.pyc"),
        )

        result = subprocess.run(
            [sys.executable, str(standalone_root / "scripts" / "tunnel_mcp"), "--help"],
            check=False,
            capture_output=True,
            text=True,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("create", result.stdout)
        self.assertIn("connect", result.stdout)

    def test_connect_writes_native_config_and_starts_tmux_with_config_flag(self) -> None:
        fake = FakeRunner()
        code, stdout, stderr = _run_main(
            [
                "connect",
                "--alias",
                "Awesome MCP",
                "--organization-id",
                "org_123",
                "--mcp-server-url",
                "http://127.0.0.1:3001/mcp",
            ],
            fake,
        )

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        config_path = pathlib.Path(payload["config_path"])
        config = json.loads(config_path.read_text(encoding="utf-8"))
        self.assertEqual(config["control_plane"]["api_key"], "env:CONTROL_PLANE_API_KEY")
        self.assertEqual(config["control_plane"]["tunnel_id"], payload["tunnel"]["id"])
        self.assertEqual(config["health"]["listen_addr"], "127.0.0.1:0")
        self.assertEqual(config["mcp"]["server_urls"][0]["channel"], "main")
        self.assertEqual(config["mcp"]["server_urls"][0]["url"], "http://127.0.0.1:3001/mcp")
        tmux_calls = [call for call in fake.calls if call[:2] == ["tmux", "new-session"]]
        self.assertEqual(len(tmux_calls), 1)
        self.assertEqual(tmux_calls[0][4], "tunnel-mcp__awesome-mcp")
        self.assertIn("tunnel-client run --config", tmux_calls[0][5])
        self.assertIn(str(config_path), tmux_calls[0][5])

    def test_connect_existing_tunnel_id_uses_runtime_key_without_admin_crud(self) -> None:
        fake = FakeRunner()
        code, stdout, stderr = _run_main(
            [
                "connect",
                "--alias",
                "Prod MCP",
                "--tunnel-id",
                "tunnel_0123456789abcdef0123456789abcdef",
                "--runtime-api-key",
                "env:TUNNEL_RUNTIME_KEY",
                "--mcp-command",
                "python server.py",
            ],
            fake,
        )

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        self.assertEqual(payload["tunnel"]["id"], "tunnel_0123456789abcdef0123456789abcdef")
        config = json.loads(pathlib.Path(payload["config_path"]).read_text(encoding="utf-8"))
        self.assertEqual(config["control_plane"]["tunnel_id"], payload["tunnel"]["id"])
        self.assertEqual(config["control_plane"]["api_key"], "env:TUNNEL_RUNTIME_KEY")
        self.assertEqual(_admin_calls(fake), [])

    def test_connect_rejects_literal_runtime_api_key(self) -> None:
        fake = FakeRunner()
        code, _stdout, stderr = _run_main(
            [
                "connect",
                "--alias",
                "bad-runtime",
                "--tunnel-id",
                "tunnel_0123456789abcdef0123456789abcdef",
                "--runtime-api-key",
                "sk-1234567890abcdef",
                "--mcp-command",
                "python server.py",
            ],
            fake,
        )

        self.assertEqual(code, 1)
        self.assertIn("runtime api_key must be", stderr)
        self.assertEqual(_admin_calls(fake), [])

    def test_connect_starts_background_process_when_tmux_missing(self) -> None:
        fake = FakeRunner(tmux_installed=False)
        fake_popen = FakePopenFactory()

        code, stdout, stderr = _run_main(
            [
                "connect",
                "--alias",
                "No Tmux MCP",
                "--organization-id",
                "org_123",
                "--mcp-command",
                "python server.py",
            ],
            fake,
            popen_factory=fake_popen,
        )

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        self.assertEqual(payload["mode"], "process")
        self.assertEqual(payload["pid"], 43210)
        self.assertTrue(payload["log_path"].endswith("no-tmux-mcp.log"))
        self.assertEqual(fake_popen.calls[0][0], "tunnel-client")
        self.assertEqual(fake_popen.calls[0][1:3], ["run", "--config"])
        process = state.load_processes()["no-tmux-mcp"]
        self.assertEqual(process.mode, "process")
        self.assertEqual(process.pid, 43210)
        self.assertEqual(process.session_name, "")

    def test_connect_rejects_inline_secret_material(self) -> None:
        fake = FakeRunner()
        code, _stdout, stderr = _run_main(
            [
                "connect",
                "--alias",
                "bad",
                "--organization-id",
                "org_123",
                "--mcp-command",
                "python server.py --api-key=sk-1234567890abcdef",
            ],
            fake,
        )

        self.assertEqual(code, 1)
        self.assertIn("inline secret material", stderr)
        self.assertFalse((pathlib.Path(self.temp.name) / "tunnel-mcp" / "aliases.yaml").exists())

    def test_connect_rejects_space_separated_secret_flag_material(self) -> None:
        fake = FakeRunner()
        code, _stdout, stderr = _run_main(
            [
                "connect",
                "--alias",
                "bad",
                "--organization-id",
                "org_123",
                "--mcp-command",
                "python server.py --api-key secret123456",
            ],
            fake,
        )

        self.assertEqual(code, 1)
        self.assertIn("inline secret material", stderr)
        self.assertFalse((pathlib.Path(self.temp.name) / "tunnel-mcp" / "aliases.yaml").exists())

    def test_connect_allows_space_separated_secret_reference(self) -> None:
        fake = FakeRunner()
        code, stdout, stderr = _run_main(
            [
                "connect",
                "--alias",
                "env-ref",
                "--organization-id",
                "org_123",
                "--mcp-command",
                "python server.py --api-key env:SERVER_API_KEY",
            ],
            fake,
        )

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        config = json.loads(pathlib.Path(payload["config_path"]).read_text(encoding="utf-8"))
        self.assertEqual(
            config["mcp"]["commands"][0]["command"],
            "python server.py --api-key env:SERVER_API_KEY",
        )

    def test_create_persists_alias(self) -> None:
        fake = FakeRunner()
        code, stdout, stderr = _run_main(
            ["create", "--alias", "Docs MCP", "--organization-id", "org_123"],
            fake,
        )

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        aliases = state.load_aliases()
        self.assertIn("docs-mcp", aliases)
        self.assertEqual(aliases["docs-mcp"].tunnel_id, payload["tunnel"]["id"])
        self.assertEqual(aliases["docs-mcp"].organization_ids, ("org_123",))

    def test_create_persists_admin_profile_and_links_alias(self) -> None:
        fake = FakeRunner()
        code, stdout, stderr = _run_main(
            [
                "create",
                "--alias",
                "Docs MCP",
                "--organization-id",
                "org_123",
                "--admin-profile",
                "Sandbox Admin",
                "--admin-key",
                "env:SANDBOX_ADMIN_KEY",
                "--control-plane-base-url",
                "https://sandbox.example.com",
            ],
            fake,
        )

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        self.assertEqual(payload["admin_profile"], "sandbox-admin")
        profiles = state.load_admin_profiles()
        self.assertEqual(profiles["sandbox-admin"].admin_key, "env:SANDBOX_ADMIN_KEY")
        self.assertEqual(
            profiles["sandbox-admin"].control_plane_base_url,
            "https://sandbox.example.com",
        )
        aliases = state.load_aliases()
        self.assertEqual(aliases["docs-mcp"].admin_profile, "sandbox-admin")
        admin_call = _admin_calls(fake)[0]
        self.assertEqual(_value_after(admin_call, "--admin-key"), "env:SANDBOX_ADMIN_KEY")
        self.assertEqual(
            _value_after(admin_call, "--control-plane.base-url"),
            "https://sandbox.example.com",
        )

    def test_create_rejects_literal_admin_key(self) -> None:
        fake = FakeRunner()
        code, _stdout, stderr = _run_main(
            [
                "create",
                "--alias",
                "bad-admin",
                "--organization-id",
                "org_123",
                "--admin-key",
                "sk-1234567890abcdef",
            ],
            fake,
        )

        self.assertEqual(code, 1)
        self.assertIn("admin profile default admin_key must be", stderr)
        self.assertEqual(_admin_calls(fake), [])

    def test_create_recovers_from_stale_alias_when_remote_get_fails(self) -> None:
        root = state.ensure_state_dirs()
        state.save_aliases(
            {
                "docs-mcp": state.AliasRecord(
                    alias="docs-mcp",
                    tunnel_id="tunnel_deadbeefdeadbeefdeadbeefdeadbeef",
                    name="docs-mcp",
                    organization_ids=("org_123",),
                )
            },
            root,
        )
        fake = FakeRunner()

        code, stdout, stderr = _run_main(
            ["create", "--alias", "docs-mcp", "--organization-id", "org_123"],
            fake,
        )

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        aliases = state.load_aliases(root)
        self.assertEqual(aliases["docs-mcp"].tunnel_id, payload["tunnel"]["id"])
        self.assertNotEqual(
            aliases["docs-mcp"].tunnel_id, "tunnel_deadbeefdeadbeefdeadbeefdeadbeef"
        )
        history = (root / "history.md").read_text(encoding="utf-8")
        self.assertIn("action=stale-alias", history)

    def test_connect_recovers_from_stale_alias_when_remote_get_fails(self) -> None:
        root = state.ensure_state_dirs()
        state.save_aliases(
            {
                "docs-mcp": state.AliasRecord(
                    alias="docs-mcp",
                    tunnel_id="tunnel_deadbeefdeadbeefdeadbeefdeadbeef",
                    name="docs-mcp",
                    organization_ids=("org_123",),
                )
            },
            root,
        )
        fake = FakeRunner()

        code, stdout, stderr = _run_main(
            [
                "connect",
                "--alias",
                "docs-mcp",
                "--organization-id",
                "org_123",
                "--mcp-command",
                "python server.py",
            ],
            fake,
        )

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        self.assertNotEqual(payload["tunnel"]["id"], "tunnel_deadbeefdeadbeefdeadbeefdeadbeef")
        config = json.loads(pathlib.Path(payload["config_path"]).read_text(encoding="utf-8"))
        self.assertEqual(config["mcp"]["commands"][0]["command"], "python server.py")

    def test_connect_restarts_running_tmux_when_stale_alias_is_replaced(self) -> None:
        root = state.ensure_state_dirs()
        stale_tunnel_id = "tunnel_deadbeefdeadbeefdeadbeefdeadbeef"
        state.save_aliases(
            {
                "docs-mcp": state.AliasRecord(
                    alias="docs-mcp",
                    tunnel_id=stale_tunnel_id,
                    name="docs-mcp",
                    organization_ids=("org_123",),
                )
            },
            root,
        )
        state.save_processes(
            {
                "docs-mcp": state.ProcessRecord(
                    alias="docs-mcp",
                    tunnel_id=stale_tunnel_id,
                    session_name="tunnel-mcp__docs-mcp",
                    config_path=str(root / "configs" / "docs-mcp.yaml"),
                    health_url_file=str(root / "health" / "docs-mcp.url"),
                    target_kind="command",
                    target_value="python old_server.py",
                    command="tunnel-client run --config docs-mcp.yaml",
                    started_at="2026-04-17T00:00:00Z",
                )
            },
            root,
        )
        fake = FakeRunner()
        fake.sessions.add("tunnel-mcp__docs-mcp")

        code, stdout, stderr = _run_main(
            [
                "connect",
                "--alias",
                "docs-mcp",
                "--organization-id",
                "org_123",
                "--mcp-command",
                "python server.py",
            ],
            fake,
        )

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        self.assertTrue(payload["started"])
        self.assertFalse(payload["already_running"])
        self.assertNotEqual(payload["tunnel"]["id"], stale_tunnel_id)
        self.assertIn("tunnel-mcp__docs-mcp", fake.sessions)
        kill_calls = [call for call in fake.calls if call[:3] == ["tmux", "kill-session", "-t"]]
        self.assertEqual(len(kill_calls), 1)
        new_session_calls = [call for call in fake.calls if call[:2] == ["tmux", "new-session"]]
        self.assertEqual(len(new_session_calls), 1)
        process = state.load_processes(root)["docs-mcp"]
        self.assertEqual(process.tunnel_id, payload["tunnel"]["id"])
        history = (root / "history.md").read_text(encoding="utf-8")
        self.assertIn("action=stale-alias", history)
        self.assertIn("action=stale-process", history)

    def test_status_reports_stale_alias_without_silent_recreate(self) -> None:
        root = state.ensure_state_dirs()
        state.save_aliases(
            {
                "docs-mcp": state.AliasRecord(
                    alias="docs-mcp",
                    tunnel_id="tunnel_deadbeefdeadbeefdeadbeefdeadbeef",
                    name="docs-mcp",
                    organization_ids=("org_123",),
                )
            },
            root,
        )
        fake = FakeRunner()

        code, stdout, stderr = _run_main(["status", "docs-mcp"], fake)

        self.assertEqual(code, 2, stderr)
        payload = json.loads(stdout)
        self.assertTrue(payload["stale"])
        self.assertEqual(payload["tunnel_id"], "tunnel_deadbeefdeadbeefdeadbeefdeadbeef")
        create_calls = [call for call in fake.calls if "create" in call]
        self.assertEqual(create_calls, [])
        self.assertIn("scripts/tunnel_mcp connect --alias docs-mcp", payload["repair_command"])

    def test_list_merges_remote_inventory_with_local_state(self) -> None:
        root = state.ensure_state_dirs()
        state.save_aliases(
            {
                "local-one": state.AliasRecord(
                    alias="local-one",
                    tunnel_id="tunnel_11111111111111111111111111111111",
                    name="Local One",
                    admin_profile="sandbox",
                    organization_ids=("org_123",),
                )
            },
            root,
        )
        fake = FakeRunner()
        fake.remote["tunnel_11111111111111111111111111111111"] = {
            "id": "tunnel_11111111111111111111111111111111",
            "name": "Local One",
            "description": "local",
            "organization_ids": ["org_123"],
            "workspace_ids": [],
            "tenant_ids": [],
        }
        fake.remote["tunnel_22222222222222222222222222222222"] = {
            "id": "tunnel_22222222222222222222222222222222",
            "name": "Remote Two",
            "description": "remote",
            "organization_ids": ["org_123"],
            "workspace_ids": [],
            "tenant_ids": [],
        }

        code, stdout, stderr = _run_main(["list", "--organization-id", "org_123"], fake)

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        local_by_id = {item["id"]: item["local_alias"] for item in payload["remote_tunnels"]}
        admin_by_id = {
            item["id"]: item["local_admin_profile"] for item in payload["remote_tunnels"]
        }
        self.assertEqual(local_by_id["tunnel_11111111111111111111111111111111"], "local-one")
        self.assertEqual(admin_by_id["tunnel_11111111111111111111111111111111"], "sandbox")
        self.assertIsNone(local_by_id["tunnel_22222222222222222222222222222222"])

    def test_status_uses_alias_admin_profile_by_default(self) -> None:
        root = state.ensure_state_dirs()
        state.save_admin_profiles(
            {
                "sandbox": state.AdminProfile(
                    name="sandbox",
                    control_plane_base_url="https://sandbox.example.com",
                    admin_key="env:SANDBOX_ADMIN_KEY",
                )
            },
            root,
            active_profile="sandbox",
        )
        state.save_aliases(
            {
                "docs-mcp": state.AliasRecord(
                    alias="docs-mcp",
                    tunnel_id="tunnel_11111111111111111111111111111111",
                    name="Docs MCP",
                    admin_profile="sandbox",
                    organization_ids=("org_123",),
                )
            },
            root,
        )
        fake = FakeRunner()
        fake.remote["tunnel_11111111111111111111111111111111"] = {
            "id": "tunnel_11111111111111111111111111111111",
            "name": "Docs MCP",
            "description": "remote",
            "organization_ids": ["org_123"],
            "workspace_ids": [],
            "tenant_ids": [],
        }
        os.environ["TUNNEL_MCP_ADMIN_PROFILE"] = "default"

        code, stdout, stderr = _run_main(["status", "docs-mcp"], fake)

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        self.assertEqual(payload["admin_profile"], "sandbox")
        admin_call = _admin_calls(fake)[0]
        self.assertEqual(_value_after(admin_call, "--admin-key"), "env:SANDBOX_ADMIN_KEY")
        self.assertEqual(
            _value_after(admin_call, "--control-plane.base-url"),
            "https://sandbox.example.com",
        )

    def test_status_merges_local_process_and_remote_metadata(self) -> None:
        root = state.ensure_state_dirs()
        health_file = root / "health" / "docs-mcp.url"
        health_file.write_text("http://127.0.0.1:4567/healthz\n", encoding="utf-8")
        state.save_aliases(
            {
                "docs-mcp": state.AliasRecord(
                    alias="docs-mcp",
                    tunnel_id="tunnel_11111111111111111111111111111111",
                    name="Docs MCP",
                    organization_ids=("org_123",),
                    config_path=str(root / "configs" / "docs-mcp.yaml"),
                    health_url_file=str(health_file),
                )
            },
            root,
        )
        state.save_processes(
            {
                "docs-mcp": state.ProcessRecord(
                    alias="docs-mcp",
                    tunnel_id="tunnel_11111111111111111111111111111111",
                    session_name="tunnel-mcp__docs-mcp",
                    config_path=str(root / "configs" / "docs-mcp.yaml"),
                    health_url_file=str(health_file),
                    target_kind="server_url",
                    target_value="http://127.0.0.1:3001/mcp",
                    command="tunnel-client run --config docs-mcp.yaml",
                    started_at="2026-04-17T00:00:00Z",
                )
            },
            root,
        )
        fake = FakeRunner()
        fake.sessions.add("tunnel-mcp__docs-mcp")
        fake.remote["tunnel_11111111111111111111111111111111"] = {
            "id": "tunnel_11111111111111111111111111111111",
            "name": "Docs MCP",
            "description": "remote",
            "organization_ids": ["org_123"],
            "workspace_ids": [],
            "tenant_ids": [],
        }

        code, stdout, stderr = _run_main(["status", "docs-mcp"], fake)

        self.assertEqual(code, 0, stderr)
        payload = json.loads(stdout)
        self.assertFalse(payload["stale"])
        self.assertTrue(payload["tmux"]["running"])
        self.assertEqual(payload["health_url"], "http://127.0.0.1:4567/healthz")
        self.assertEqual(payload["remote"]["name"], "Docs MCP")


def _run_main(
    argv: list[str],
    fake: FakeRunner,
    *,
    popen_factory: runtime.PopenFactory = subprocess.Popen,
) -> tuple[int, str, str]:
    stdout = io.StringIO()
    stderr = io.StringIO()
    with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        code = commands.main(argv, runner=fake, popen_factory=popen_factory)
    return code, stdout.getvalue(), stderr.getvalue()


def _completed(
    args: list[str],
    returncode: int,
    *,
    stdout: str = "",
    stderr: str = "",
) -> subprocess.CompletedProcess[str]:
    return subprocess.CompletedProcess(
        args=args, returncode=returncode, stdout=stdout, stderr=stderr
    )


def _value_after(args: list[str], flag: str) -> str:
    return args[args.index(flag) + 1]


def _values_after(args: list[str], flag: str) -> list[str]:
    values = []
    for idx, arg in enumerate(args):
        if arg == flag:
            values.append(args[idx + 1])
    return values


def _admin_calls(fake: FakeRunner) -> list[list[str]]:
    return [call for call in fake.calls if len(call) >= 2 and call[1] == "admin"]


if __name__ == "__main__":
    unittest.main()
