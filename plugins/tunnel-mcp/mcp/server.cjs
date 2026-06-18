"use strict";

const fs = require("node:fs");
const path = require("node:path");
const readline = require("node:readline");
const { spawnSync } = require("node:child_process");

const SERVER_NAME = "tunnel-mcp";
const PLUGIN_ROOT = path.resolve(__dirname, "..");
const SERVER_VERSION = readPluginVersion();
const MAX_STDIO_COMMAND_LENGTH = 4096;
const BIN_HINT_PATH = path.join(PLUGIN_ROOT, ".tunnel-client-bin");

const NORMALIZED_KEYS = [
  "tunnel_id",
  "alias",
  "profile_path",
  "healthz",
  "readyz",
  "control_plane_poll_health",
  "session_name",
  "repair_actions",
  "selected_tunnel_client_bin",
  "live_process_command",
  "live_process_binary",
  "launch_diagnostics",
];

function readPluginVersion() {
  try {
    const manifest = JSON.parse(
      fs.readFileSync(path.join(PLUGIN_ROOT, ".codex-plugin", "plugin.json"), "utf8"),
    );
    return manifest.version || "0.1.1";
  } catch {
    return "0.1.1";
  }
}

function toolDefinitions() {
  return [
    tool(
      "install_or_select_tunnel_client",
      "Install Or Select Tunnel Client",
      [
        "Select the native tunnel-client binary for Tunnel MCP operations.",
        "This does not auto-download or clone tunnel-client;",
        "pass an explicit binary path or use an existing trusted hint.",
      ].join(" "),
      {
        type: "object",
        properties: {
          tunnel_client_bin: {
            type: "string",
            description: "Optional full path to an executable tunnel-client binary.",
          },
          persist_hint: {
            type: "boolean",
            description:
              [
                "When true, write the selected path to .tunnel-client-bin",
                "in the installed plugin. Defaults to true for explicit,",
                "environment, adjacent, and PATH selections.",
              ].join(" "),
            default: true,
          },
          allow_path_lookup: {
            type: "boolean",
            description:
              "When true, allow PATH lookup as a last-resort explicit selection. Defaults to false.",
            default: false,
          },
        },
        additionalProperties: false,
      },
      true,
    ),
    tool(
      "create_tunnel_runtime",
      "Create Tunnel Runtime",
      "Create or reuse a remote tunnel alias through native tunnel-client runtimes create.",
      runtimeLifecycleSchema({
        includeMcpCommand: false,
        includeTunnelId: false,
        requireRemoteScope: true,
      }),
      false,
    ),
    tool(
      "connect_stdio_mcp",
      "Connect Stdio MCP",
      "Connect a local stdio MCP command to a tunnel-client runtime through native tunnel-client runtimes connect.",
      runtimeLifecycleSchema({
        includeMcpCommand: true,
        includeTunnelId: true,
        requireRemoteScope: false,
      }),
      false,
    ),
    tool(
      "list_runtime_aliases",
      "List Runtime Aliases",
      "List local tunnel-client runtime aliases and optionally remote scoped tunnels.",
      listRuntimeAliasesSchema(),
      true,
    ),
    tool(
      "runtime_status",
      "Runtime Status",
      [
        "Inspect a tunnel-client runtime alias and normalize health,",
        "readiness, control-plane poll health, and repair actions.",
      ].join(" "),
      aliasSchema(),
      true,
    ),
    tool(
      "stop_runtime",
      "Stop Runtime",
      "Stop a local tunnel-client runtime alias without deleting the remote tunnel.",
      aliasSchema(),
      false,
    ),
  ];
}

function tool(name, title, description, inputSchema, readOnly) {
  return {
    name,
    title,
    description,
    inputSchema,
    annotations: {
      readOnlyHint: readOnly,
      destructiveHint: false,
      idempotentHint: name !== "create_tunnel_runtime" && name !== "connect_stdio_mcp",
      openWorldHint: name !== "install_or_select_tunnel_client",
    },
  };
}

function aliasSchema() {
  return {
    type: "object",
    properties: {
      alias: {
        type: "string",
        description: "Local tunnel-client runtime alias.",
      },
      tunnel_client_bin: {
        type: "string",
        description: "Optional full path to an executable tunnel-client binary.",
      },
    },
    required: ["alias"],
    additionalProperties: false,
  };
}

function runtimeLifecycleSchema({ includeMcpCommand, includeTunnelId, requireRemoteScope }) {
  const properties = {
    alias: {
      type: "string",
      description: "Local tunnel-client runtime alias.",
    },
    organization_id: {
      type: "string",
      description: "Organization id for remote tunnel creation or lookup.",
    },
    workspace_id: {
      type: "string",
      description: "Workspace id for remote tunnel creation or lookup.",
    },
    admin_profile: {
      type: "string",
      description: "Native tunnel-client admin profile name.",
    },
    name: {
      type: "string",
      description: "Optional remote tunnel display name.",
    },
    description: {
      type: "string",
      description: "Optional remote tunnel description.",
    },
    tunnel_client_bin: {
      type: "string",
      description: "Optional full path to an executable tunnel-client binary.",
    },
  };

  if (includeMcpCommand) {
    properties.mcp_command = {
      type: "string",
      description: "Stdio MCP command line for tunnel-client to run locally.",
    };
  }
  if (includeTunnelId) {
    properties.tunnel_id = {
      type: "string",
      description: "Existing tunnel id to attach instead of creating or resolving by scope.",
    };
  }

  return {
    type: "object",
    properties,
    required: requireRemoteScope ? ["alias"] : ["alias", "mcp_command"],
    additionalProperties: false,
  };
}

async function callTool(name, args = {}) {
  switch (name) {
    case "install_or_select_tunnel_client":
      return resultForPayload("Selected tunnel-client binary.", installOrSelect(args));
    case "create_tunnel_runtime":
      return runLifecycleTool("create_tunnel_runtime", args, buildCreateArgs);
    case "connect_stdio_mcp":
      return runLifecycleTool("connect_stdio_mcp", args, buildConnectArgs);
    case "list_runtime_aliases":
      return runLifecycleTool("list_runtime_aliases", args, buildListArgs);
    case "runtime_status":
      return runLifecycleTool("runtime_status", args, buildStatusArgs);
    case "stop_runtime":
      return runLifecycleTool("stop_runtime", args, buildStopArgs);
    default:
      throw new Error(`unknown tool: ${name}`);
  }
}

function installOrSelect(args) {
  assertNoUnknown(args, [
    "tunnel_client_bin",
    "persist_hint",
    "allow_path_lookup",
  ]);
  const selected = selectTunnelClientBin(args);
  const persistHint = args.persist_hint !== false;

  if (selected.path && persistHint && selected.source !== ".tunnel-client-bin") {
    persistTunnelClientHint(selected.path);
  }

  return normalizedPayload(
    {
      ok: Boolean(selected.path),
      operation: "install_or_select_tunnel_client",
      tunnel_client_bin: selected.path || null,
      selection_source: selected.source || null,
      discovery_attempts: selected.attempts,
      native: {},
      repair_actions: selected.path
        ? []
        : [
            repairAction(
              "select_tunnel_client_binary",
              [
                "Pass tunnel_client_bin with the full path to a trusted",
                "tunnel-client binary, or reinstall the plugin with a binary hint.",
              ].join(" "),
              "install_or_select_tunnel_client",
            ),
          ],
    },
    {},
  );
}

function runLifecycleTool(operation, args, buildArgs) {
  assertNoUnknown(args, allowedArgsForOperation(operation));
  if (args.alias !== undefined) {
    validateAlias(args.alias);
  }
  const nativeArgs = buildArgs(args);
  const selected = selectTunnelClientBin(args);
  if (!selected.path) {
    throw new Error(
      [
        "tunnel-client binary was not selected.",
        ...selected.attempts.map((attempt) => `- ${attempt}`),
      ].join("\n"),
    );
  }

  const completed = runTunnelClient(selected.path, nativeArgs);
  const native = parseJsonPayload(completed.stdout);
  const payload = normalizedPayload(
    {
      ok: completed.status === 0,
      operation,
      tunnel_client_bin: selected.path,
      selected_tunnel_client_bin: selected.path,
      selection_source: selected.source,
      command: ["tunnel-client", ...nativeArgs],
      exit_code: completed.status,
      stderr: completed.stderr.trim() || null,
      native,
    },
    native,
  );

  if (completed.status !== 0) {
    const err = new Error(
      completed.stderr.trim() ||
        completed.stdout.trim() ||
        `tunnel-client exited with status ${completed.status}`,
    );
    err.payload = payload;
    throw err;
  }

  return resultForPayload(summaryText(operation, payload), payload);
}

function allowedArgsForOperation(operation) {
  if (operation === "create_tunnel_runtime") {
    return Object.keys(runtimeLifecycleSchema({
      includeMcpCommand: false,
      includeTunnelId: false,
      requireRemoteScope: true,
    }).properties);
  }
  if (operation === "connect_stdio_mcp") {
    return Object.keys(runtimeLifecycleSchema({
      includeMcpCommand: true,
      includeTunnelId: true,
      requireRemoteScope: false,
    }).properties);
  }
  if (operation === "list_runtime_aliases") {
    return Object.keys(listRuntimeAliasesSchema().properties);
  }
  return Object.keys(aliasSchema().properties);
}

function listRuntimeAliasesSchema() {
  return {
    type: "object",
    properties: {
      organization_id: {
        type: "string",
        description: "Optional organization scope for remote listing.",
      },
      workspace_id: {
        type: "string",
        description: "Optional workspace scope for remote listing.",
      },
      tenant_id: {
        type: "string",
        description: "Optional tenant scope for remote listing.",
      },
      admin_profile: {
        type: "string",
        description: "Native tunnel-client admin profile name.",
      },
      tunnel_client_bin: {
        type: "string",
        description: "Optional full path to an executable tunnel-client binary.",
      },
    },
    additionalProperties: false,
  };
}

function buildCreateArgs(args) {
  validateRemoteScope(args, { allowTunnelId: false, required: true, command: "create_tunnel_runtime" });
  const out = ["runtimes", "create", "--alias", args.alias];
  appendRemoteScope(out, args);
  appendOptional(out, "--admin-profile", args.admin_profile);
  appendOptional(out, "--name", args.name);
  appendOptional(out, "--description", args.description);
  out.push("--json");
  return out;
}

function buildConnectArgs(args) {
  validateStdioCommand(args.mcp_command);
  validateRemoteScope(args, { allowTunnelId: true, required: true, command: "connect_stdio_mcp" });
  const out = ["runtimes", "connect", "--alias", args.alias, "--mcp-command", args.mcp_command];
  appendRemoteScope(out, args);
  appendOptional(out, "--tunnel-id", args.tunnel_id);
  appendOptional(out, "--admin-profile", args.admin_profile);
  appendOptional(out, "--name", args.name);
  appendOptional(out, "--description", args.description);
  out.push("--json");
  return out;
}

function buildListArgs(args) {
  validateListScope(args);
  const out = ["runtimes", "list"];
  appendRemoteScope(out, args);
  appendOptional(out, "--tenant-id", args.tenant_id);
  appendOptional(out, "--admin-profile", args.admin_profile);
  out.push("--json");
  return out;
}

function buildStatusArgs(args) {
  return ["runtimes", "status", args.alias, "--json"];
}

function buildStopArgs(args) {
  return ["runtimes", "stop", args.alias, "--json"];
}

function selectTunnelClientBin(args) {
  const attempts = [];
  const explicit = trimString(args.tunnel_client_bin);
  if (explicit) {
    if (isExecutable(explicit)) {
      return { path: path.resolve(explicit), source: "explicit", attempts };
    }
    attempts.push(`tunnel_client_bin: ${explicit} is not an executable file`);
  } else {
    attempts.push("tunnel_client_bin: not provided");
  }

  const envBin = trimString(process.env.TUNNEL_CLIENT_BIN);
  if (envBin) {
    if (isExecutable(envBin)) {
      return { path: path.resolve(envBin), source: "TUNNEL_CLIENT_BIN", attempts };
    }
    attempts.push(`TUNNEL_CLIENT_BIN: ${envBin} is not an executable file`);
  } else {
    attempts.push("TUNNEL_CLIENT_BIN: not set");
  }

  if (fs.existsSync(BIN_HINT_PATH)) {
    const hinted = trimString(fs.readFileSync(BIN_HINT_PATH, "utf8"));
    if (hinted && isExecutable(hinted)) {
      return { path: path.resolve(hinted), source: ".tunnel-client-bin", attempts };
    }
    attempts.push(`.tunnel-client-bin: ${hinted || "empty"} is not an executable file`);
  } else {
    attempts.push(".tunnel-client-bin: not present");
  }

  const adjacent = findAdjacentBinary();
  if (adjacent) {
    return { path: adjacent, source: "adjacent-build-output", attempts };
  }
  attempts.push("adjacent build outputs: no executable tunnel-client binary found next to the plugin");

  if (args.allow_path_lookup === true) {
    const pathBin = commandPath("tunnel-client") || commandPath("tunnel-client.exe");
    if (pathBin && isExecutable(pathBin)) {
      return { path: path.resolve(pathBin), source: "PATH", attempts };
    }
    attempts.push("PATH: no tunnel-client executable found");
  } else {
    attempts.push("PATH: skipped unless allow_path_lookup is true");
  }

  return { path: "", source: "", attempts };
}

function findAdjacentBinary() {
  for (const root of candidateRoots()) {
    for (const rel of [
      "tunnel-client",
      "tunnel-client.exe",
      "bin/tunnel-client",
      "bin/tunnel-client.exe",
      "bazel-bin/cmd/client/client",
      "bazel-bin/cmd/client/client.exe",
      "bazel-bin/api/tunnel-client/cmd/client/client",
      "bazel-bin/api/tunnel-client/cmd/client/client.exe",
    ]) {
      const candidate = path.join(root, rel);
      if (isExecutable(candidate)) {
        return path.resolve(candidate);
      }
    }
  }
  return "";
}

function candidateRoots() {
  const roots = [PLUGIN_ROOT];
  let current = PLUGIN_ROOT;
  while (current && current !== path.dirname(current)) {
    const parent = path.dirname(current);
    if (
      fs.existsSync(path.join(parent, "cmd", "client")) ||
      fs.existsSync(path.join(parent, "api", "tunnel-client", "cmd", "client"))
    ) {
      roots.push(parent);
    }
    current = parent;
  }
  return roots;
}

function commandPath(name) {
  const completed = spawnSync("command", ["-v", name], {
    shell: true,
    encoding: "utf8",
  });
  if (completed.status === 0) {
    return completed.stdout.trim().split(/\r?\n/)[0] || "";
  }
  return "";
}

function runTunnelClient(bin, args) {
  const completed = spawnSync(bin, args, {
    cwd: PLUGIN_ROOT,
    env: process.env,
    encoding: "utf8",
    maxBuffer: 20 * 1024 * 1024,
  });
  if (
    completed.error &&
    (completed.error.code === "ENOEXEC" || completed.error.errno === -8)
  ) {
    return spawnSync("/bin/sh", [bin, ...args], {
      cwd: PLUGIN_ROOT,
      env: process.env,
      encoding: "utf8",
      maxBuffer: 20 * 1024 * 1024,
    });
  }
  if (completed.error) {
    throw completed.error;
  }
  return completed;
}

function parseJsonPayload(stdout) {
  const lines = String(stdout || "")
    .split(/\r?\n/)
    .map((line) => line.trimEnd());
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    const candidate = lines.slice(index).join("\n").trim();
    if (!candidate) {
      continue;
    }
    try {
      return JSON.parse(candidate);
    } catch {
      // Native commands may print non-JSON diagnostics before the JSON payload.
    }
  }
  return {};
}

function normalizedPayload(base, native) {
  const payload = {
    tunnel_id: tunnelIdFrom(native),
    alias: stringOrNull(native.alias || base.alias),
    profile_path: stringOrNull(native.profile_path || native.config_path),
    healthz: endpointFrom(native, "healthz"),
    readyz: endpointFrom(native, "readyz"),
    control_plane_poll_health:
      native.control_plane_poll_health ||
      nested(native, ["local", "control_plane_poll_health"]) ||
      null,
    session_name: stringOrNull(
      native.session_name || nested(native, ["process", "session_name"]),
    ),
    repair_actions: Array.isArray(native.repair_actions)
      ? native.repair_actions
      : Array.isArray(base.repair_actions)
        ? base.repair_actions
        : [],
    selected_tunnel_client_bin: stringOrNull(base.selected_tunnel_client_bin || base.tunnel_client_bin),
    live_process_command: liveProcessCommand(native),
    live_process_binary: liveProcessBinary(native),
    launch_diagnostics: launchDiagnostics(native),
    ...base,
  };

  for (const key of NORMALIZED_KEYS) {
    if (!(key in payload)) {
      payload[key] = key === "repair_actions" ? [] : null;
    }
  }
  return payload;
}

function liveProcessCommand(native) {
  return stringOrNull(nested(native, ["process", "command"]) || native.process_command);
}

function liveProcessBinary(native) {
  const explicit = stringOrNull(
    nested(native, ["process", "binary"]) ||
      nested(native, ["process", "tunnel_client_bin"]) ||
      native.live_process_binary,
  );
  if (explicit) {
    return explicit;
  }
  const command = liveProcessCommand(native);
  if (!command) {
    return null;
  }
  return firstShellWord(command);
}

function launchDiagnostics(native) {
  const diagnostics = {};
  const launch = native.launch_diagnostics;
  if (launch && typeof launch === "object") {
    Object.assign(diagnostics, launch);
  }
  for (const key of ["exit_code", "stderr", "stdout"]) {
    if (native[key] !== undefined && native[key] !== null && native[key] !== "") {
      diagnostics[key] = native[key];
    }
  }
  const log = nested(native, ["local", "log"]) || native.log;
  if (log && typeof log === "object") {
    const tail = trimString(log.tail);
    if (tail) {
      diagnostics.log_path = stringOrNull(log.path) || null;
      diagnostics.log_tail = tail;
    }
  }
  return Object.keys(diagnostics).length ? diagnostics : null;
}

function firstShellWord(command) {
  const text = trimString(command);
  if (!text) {
    return null;
  }
  if (text[0] === "'") {
    const end = text.indexOf("'", 1);
    return end === -1 ? text.slice(1) : text.slice(1, end);
  }
  if (text[0] === '"') {
    let out = "";
    for (let index = 1; index < text.length; index += 1) {
      const char = text[index];
      if (char === "\\") {
        index += 1;
        if (index < text.length) {
          out += text[index];
        }
        continue;
      }
      if (char === '"') {
        return out;
      }
      out += char;
    }
    return out;
  }
  return text.split(/\s+/)[0] || null;
}

function tunnelIdFrom(native) {
  return stringOrNull(
    native.tunnel_id ||
      nested(native, ["tunnel", "id"]) ||
      nested(native, ["remote", "id"]) ||
      nested(native, ["process", "tunnel_id"]),
  );
}

function endpointFrom(native, name) {
  return (
    native[name] ||
    nested(native, ["effective_health", name]) ||
    nested(native, ["local", "effective_health", name]) ||
    nested(native, ["health", name]) ||
    nested(native, ["local", "health", name]) ||
    null
  );
}

function resultForPayload(text, payload) {
  return {
    content: [{ type: "text", text }],
    structuredContent: payload,
  };
}

function summaryText(operation, payload) {
  const alias = payload.alias ? ` alias=${payload.alias}` : "";
  const tunnel = payload.tunnel_id ? ` tunnel_id=${payload.tunnel_id}` : "";
  const health = statusSummary("healthz", payload.healthz);
  const ready = statusSummary("readyz", payload.readyz);
  return `${operation} complete.${alias}${tunnel}${health}${ready}`;
}

function statusSummary(label, endpoint) {
  if (!endpoint || typeof endpoint !== "object") {
    return "";
  }
  if ("status" in endpoint) {
    return ` ${label}=${endpoint.status}`;
  }
  if ("ok" in endpoint) {
    return ` ${label}.ok=${endpoint.ok}`;
  }
  return "";
}

function repairAction(action, reason, command) {
  return { action, reason, command };
}

function persistTunnelClientHint(selectedPath) {
  fs.writeFileSync(BIN_HINT_PATH, `${selectedPath}\n`, { mode: 0o600 });
}

function validateAlias(value) {
  const alias = trimString(value);
  if (!alias) {
    throw new Error("alias is required");
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(alias)) {
    throw new Error("alias must start with a letter or number and contain only letters, numbers, '.', '_', or '-'");
  }
}

function validateStdioCommand(value) {
  const command = trimString(value);
  if (!command) {
    throw new Error("mcp_command is required");
  }
  if (command.length > MAX_STDIO_COMMAND_LENGTH) {
    throw new Error(`mcp_command must be at most ${MAX_STDIO_COMMAND_LENGTH} characters`);
  }
}

function validateRemoteScope(args, { allowTunnelId, required, command }) {
  const count = [args.organization_id, args.workspace_id, allowTunnelId ? args.tunnel_id : ""]
    .map(trimString)
    .filter(Boolean).length;
  if (required && count !== 1) {
    const options = allowTunnelId
      ? "organization_id, workspace_id, or tunnel_id"
      : "organization_id or workspace_id";
    throw new Error(`${command} requires exactly one of ${options}`);
  }
  if (!required && count > 1) {
    throw new Error(`${command} accepts only one remote scope`);
  }
}

function validateListScope(args) {
  const count = [args.organization_id, args.workspace_id, args.tenant_id]
    .map(trimString)
    .filter(Boolean).length;
  if (count > 1) {
    throw new Error("list_runtime_aliases accepts at most one of organization_id, workspace_id, or tenant_id");
  }
}

function appendRemoteScope(out, args) {
  appendOptional(out, "--organization-id", args.organization_id);
  appendOptional(out, "--workspace-id", args.workspace_id);
}

function appendOptional(out, flag, value) {
  const text = trimString(value);
  if (text) {
    out.push(flag, text);
  }
}

function assertNoUnknown(args, allowed) {
  const allowedSet = new Set(allowed);
  for (const key of Object.keys(args || {})) {
    if (!allowedSet.has(key)) {
      throw new Error(`unknown argument: ${key}`);
    }
  }
}

function isExecutable(candidate) {
  try {
    const stat = fs.statSync(candidate);
    return stat.isFile() && (stat.mode & 0o111) !== 0;
  } catch {
    return false;
  }
}

function trimString(value) {
  return typeof value === "string" ? value.trim() : "";
}

function stringOrNull(value) {
  const text = trimString(value);
  return text || null;
}

function nested(value, keys) {
  let current = value;
  for (const key of keys) {
    if (!current || typeof current !== "object") {
      return undefined;
    }
    current = current[key];
  }
  return current;
}

async function handleRpc(message) {
  if (message.method === "initialize") {
    return {
      jsonrpc: "2.0",
      id: message.id,
      result: {
        protocolVersion: "2025-06-18",
        capabilities: { tools: {} },
        serverInfo: { name: SERVER_NAME, version: SERVER_VERSION },
      },
    };
  }

  if (message.method === "notifications/initialized") {
    return null;
  }

  if (message.method === "tools/list") {
    return {
      jsonrpc: "2.0",
      id: message.id,
      result: { tools: toolDefinitions() },
    };
  }

  if (message.method === "tools/call") {
    try {
      const result = await callTool(message.params.name, message.params.arguments || {});
      return { jsonrpc: "2.0", id: message.id, result };
    } catch (error) {
      return {
        jsonrpc: "2.0",
        id: message.id,
        error: {
          code: -32000,
          message: error && error.message ? error.message : String(error),
          data: error && error.payload ? error.payload : undefined,
        },
      };
    }
  }

  return {
    jsonrpc: "2.0",
    id: message.id,
    error: { code: -32601, message: `method not found: ${message.method}` },
  };
}

async function main() {
  const rl = readline.createInterface({
    input: process.stdin,
    crlfDelay: Infinity,
  });

  for await (const line of rl) {
    if (!line.trim()) {
      continue;
    }
    const response = await handleRpc(JSON.parse(line));
    if (response) {
      process.stdout.write(`${JSON.stringify(response)}\n`);
    }
  }
}

module.exports = {
  NORMALIZED_KEYS,
  callTool,
  handleRpc,
  normalizedPayload,
  toolDefinitions,
};

if (require.main === module) {
  main().catch((error) => {
    process.stderr.write(`${error && error.stack ? error.stack : error}\n`);
    process.exit(1);
  });
}
