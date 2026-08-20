import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";

const [exampleModulePath, tunnelClientPath] = process.argv.slice(2);
if (!exampleModulePath || !tunnelClientPath) {
  throw new Error("usage: example_test.mjs <compiled-example.js> <tunnel-client>");
}

const {
  EXAMPLE_TUNNEL_ID,
  buildHttpMCPTunnelProxyCommand,
  buildStdioMCPTunnelProxyCommand,
  callEchoToolThroughMCPTunnelProxy,
  startExampleMCPServer,
} = await import(pathToFileURL(exampleModulePath));

assert.deepEqual(
  buildHttpMCPTunnelProxyCommand("tunnel-client", "http://127.0.0.1:3000/mcp"),
  [
    "tunnel-client",
    "dev",
    "proxy",
    "--tunnel-id",
    EXAMPLE_TUNNEL_ID,
    "--mcp-server-url",
    "http://127.0.0.1:3000/mcp",
    "--print-json",
  ],
);

const stdioArgs = buildStdioMCPTunnelProxyCommand(
  "tunnel-client",
  ["/usr/local/bin/example mcp", "--stdio", "--name", "Ada Lovelace"],
);
assert.deepEqual(stdioArgs.slice(0, 5), [
  "tunnel-client",
  "dev",
  "proxy",
  "--tunnel-id",
  EXAMPLE_TUNNEL_ID,
]);
assert.equal(stdioArgs[5], "--mcp-command");
assert.equal(stdioArgs[7], "--print-json");
assert.match(stdioArgs[6], /example mcp/);
assert.match(stdioArgs[6], /Ada Lovelace/);

const server = await startExampleMCPServer();
try {
  const response = await callEchoToolThroughMCPTunnelProxy(
    [tunnelClientPath],
    server.url,
    "Ada",
  );
  assert.deepEqual(server.calls, ["Ada"]);
  assert.equal(response.result.structuredContent.greeting, "hello Ada");
} finally {
  await server.close();
}
