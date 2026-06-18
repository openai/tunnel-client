const assert = require("node:assert/strict");
const test = require("node:test");

const { callTool, toolDefinitions } = require("./server.cjs");

const blockedCredentialArgs = [
  "control_plane_base_url",
  "control_plane_url_path",
  "runtime_api_key",
];

function toolByName(name) {
  const definition = toolDefinitions().find((item) => item.name === name);
  assert.ok(definition, `missing tool definition for ${name}`);
  return definition;
}

test("runtime lifecycle tools do not advertise credential-bearing arguments", () => {
  for (const toolName of ["create_tunnel_runtime", "connect_stdio_mcp"]) {
    const properties = toolByName(toolName).inputSchema.properties;
    for (const argName of blockedCredentialArgs) {
      assert.equal(properties[argName], undefined, `${toolName} exposes ${argName}`);
    }
  }
});

test("list tool does not advertise control-plane override arguments", () => {
  const properties = toolByName("list_runtime_aliases").inputSchema.properties;
  for (const argName of ["control_plane_base_url", "control_plane_url_path"]) {
    assert.equal(properties[argName], undefined, `list_runtime_aliases exposes ${argName}`);
  }
});

test("blocked credential arguments are rejected before native execution", async () => {
  await assert.rejects(
    callTool("create_tunnel_runtime", {
      alias: "docs",
      organization_id: "org_123",
      control_plane_base_url: "https://attacker.example",
    }),
    /unknown argument: control_plane_base_url/,
  );

  await assert.rejects(
    callTool("connect_stdio_mcp", {
      alias: "docs",
      tunnel_id: "tunnel_0123456789abcdef0123456789abcdef",
      mcp_command: "python server.py",
      runtime_api_key: "file:/tmp/secret",
    }),
    /unknown argument: runtime_api_key/,
  );
});
