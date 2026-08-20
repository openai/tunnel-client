import assert from "node:assert/strict";
import { mkdtemp, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

const [wrapperModulePath, fakeClientPath] = process.argv.slice(2);
if (!wrapperModulePath || !fakeClientPath) {
  throw new Error(
    "usage: mcp_tunnel_client_proxy_test.mjs <compiled-wrapper.js> <fake-client.mjs>",
  );
}
const tempDir = await mkdtemp(path.join(tmpdir(), "tunnel-client-proxy-ts-"));

const {
  mcpTunnelClientProxyCommand,
  mcpTunnelClientRunCommand,
  startHttpMCPTunnelProxy,
  startStdioMCPTunnelProxy,
} = await import(pathToFileURL(wrapperModulePath));

await testHttpWrapperStartsLocalProxyWithoutHealthListener();
await testStdioWrapperQuotesCommandArguments();
testLocalCommandBuilderSupportsUrlFileAndExtraArgs();
testRemoteRunCommandUsesAPIKeyEnvReference();

async function testHttpWrapperStartsLocalProxyWithoutHealthListener() {
  const argFile = path.join(tempDir, "http-args.json");
  const proxy = await startHttpMCPTunnelProxy(
    [process.execPath, fakeClientPath],
    "http://127.0.0.1:3000/mcp",
    { env: { MCP_TUNNEL_CLIENT_ARG_FILE: argFile } },
  );
  try {
    assert.equal(proxy.info.tunnel_id, "tunnel_typescript_example");
    assert.equal(proxy.info.control_plane_transport, "unix");
    assert.equal(proxy.info.health_url, undefined);
  } finally {
    await proxy.stop();
  }

  const args = JSON.parse(await readFile(argFile, "utf8"));
  assert.deepEqual(args, [
    "dev",
    "proxy",
    "--mcp-server-url",
    "http://127.0.0.1:3000/mcp",
    "--print-json",
  ]);
  assert.equal(args.includes("--health-listen-addr"), false);
}

async function testStdioWrapperQuotesCommandArguments() {
  const argFile = path.join(tempDir, "stdio-args.json");
  const proxy = await startStdioMCPTunnelProxy(
    [process.execPath, fakeClientPath],
    ["/usr/local/bin/example mcp", "--stdio", "--name", "Ada Lovelace"],
    { env: { MCP_TUNNEL_CLIENT_ARG_FILE: argFile } },
  );
  try {
    assert.equal(proxy.info.backend, "go-in-memory");
  } finally {
    await proxy.stop();
  }

  const args = JSON.parse(await readFile(argFile, "utf8"));
  assert.deepEqual(args.slice(0, 2), ["dev", "proxy"]);
  assert.equal(args[2], "--mcp-command");
  assert.equal(args[4], "--print-json");
  assert.match(args[3], /example mcp/);
  assert.match(args[3], /Ada Lovelace/);
}

function testLocalCommandBuilderSupportsUrlFileAndExtraArgs() {
  assert.deepEqual(
    mcpTunnelClientProxyCommand({
      command: ["tunnel-client"],
      mcpServerUrls: ["url=http://127.0.0.1:3000/mcp,channel=tools"],
      tunnelId: "tunnel_test",
      urlFile: "/tmp/local-proxy.json",
      extraArgs: ["--response-timeout", "5s"],
    }),
    [
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
    ],
  );
}

function testRemoteRunCommandUsesAPIKeyEnvReference() {
  assert.deepEqual(
    mcpTunnelClientRunCommand({
      command: ["tunnel-client"],
      controlPlaneBaseUrl: "https://control-plane.example",
      controlPlaneApiKeyEnv: "TEST_CONTROL_PLANE_API_KEY",
      tunnelId: "tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      mcpServerUrls: ["https://mcp.example/mcp"],
    }, "/tmp/tunnel-client-health.url"),
    [
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
    ],
  );
}
