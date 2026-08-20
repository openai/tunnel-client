"use strict";

const fs = require("node:fs");
const path = require("node:path");
const readline = require("node:readline");
const { spawnSync } = require("node:child_process");

const SERVER_NAME = "tunnel-mcp";
const PLUGIN_ROOT = path.resolve(__dirname, "..");
const SERVER_VERSION = readPluginVersion();
const MAX_STDIO_COMMAND_LENGTH = 4096;
const MAX_ESCAPED_JSON_PASSES = 4;
const MAX_KNOWN_SECRETS = 256;
const MAX_KNOWN_SECRET_CHARS = 16384;
const MAX_REDACTION_DEPTH = 128;
const MAX_EMBEDDED_JSON_CANDIDATES = 64;
const MAX_EMBEDDED_JSON_CANDIDATE_CHARS = 65536;
const REDACTED = "[REDACTED]";
const BIN_HINT_PATH = path.join(PLUGIN_ROOT, ".tunnel-client-bin");
const ALLOWED_CONTROL_PLANE_ORIGINS = new Set([
  "https://api.openai.com",
  "https://mtls.api.openai.com",
]);
const CONTROL_PLANE_BASE_URL_ENV = "CONTROL_PLANE_BASE_URL";
const OPENAI_STYLE_SECRET_PATTERN = "\\bsk-[A-Za-z0-9][A-Za-z0-9_-]{11,}\\b";
const PRIVATE_KEY_PEM_PATTERN =
  "-----BEGIN (?:OPENSSH |RSA |EC |DSA |ENCRYPTED )?PRIVATE KEY-----[\\s\\S]*?" +
  "-----END (?:OPENSSH |RSA |EC |DSA |ENCRYPTED )?PRIVATE KEY-----";
const BEARER_SECRET_PATTERN = "\\b(Bearer\\s+)([^\\s,;]+)";
const URL_USERINFO_PATTERN = "(://)([^\\s/@]+)(@)";
const QUOTED_OR_BARE_VALUE_PATTERN =
  "(?:\"((?:\\\\.|[^\"\\\\])*)\"|'((?:''|\\\\.|[^'\\\\])*)'|([^\\s]+))";
const SENSITIVE_ASSIGNMENT_PATTERN =
  "(\\b([A-Za-z_][A-Za-z0-9_-]*(?:\\[[A-Za-z0-9_.-]*\\])*)\\s*=\\s*)" + QUOTED_OR_BARE_VALUE_PATTERN;
const SENSITIVE_FLAG_PATTERN =
  "((?:^|\\s)(-{1,2}[A-Za-z0-9._-]+)(?:=|\\s+))" + QUOTED_OR_BARE_VALUE_PATTERN;
const SENSITIVE_JSON_VALUE_PATTERN =
  "(?:\"((?:\\\\.|[^\"\\\\])*)\"|'((?:''|\\\\.|[^'\\\\])*)'|([^\\s{\\[]+))";
const SENSITIVE_JSON_PATTERN =
  "(\"([A-Za-z0-9_.-]+)\"\\s*:\\s*)" + SENSITIVE_JSON_VALUE_PATTERN;
const SENSITIVE_YAML_SCALAR_PATTERN =
  "(^|[^A-Za-z0-9_])([\"'])([A-Za-z0-9_][A-Za-z0-9_.-]*)\\2\\s*:\\s*" +
  SENSITIVE_JSON_VALUE_PATTERN;
const NEXT_HEADER_PATTERN =
  "(?=\\s+(?:(?:--header|-H)\\s+[\"']?)?[A-Za-z][A-Za-z0-9_.-]*\\s*:|\\r?\\n|$)";
const SENSITIVE_HEADER_PATTERN =
  "(\\b([A-Za-z][A-Za-z0-9_.-]*)\\s*:\\s*)(?:\"((?:\\\\.|[^\"\\\\])*)\"|'((?:\\\\.|[^'\\\\])*)'|([^\\r\\n]*?))" +
  NEXT_HEADER_PATTERN;
const YAML_BLOCK_PATTERN =
  "(^|[^A-Za-z0-9_])([ \\t]*)([\"']?)([A-Za-z0-9_][A-Za-z0-9_.-]*)\\3\\s*:\\s*[|>](?:[1-9][-+]?|[-+][1-9]?)?\\s*\\r?\\n" +
  "((?:(?:[ \\t]+.*(?:\\r?\\n|$))|(?:[ \\t]*\\r?\\n))+)";

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
    control_plane_base_url: {
      type: "string",
      description: "Optional control-plane base URL override stored in the admin profile.",
    },
    control_plane_url_path: {
      type: "string",
      description: "Optional URL path appended to the control-plane base URL and stored in the admin profile.",
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
    properties.runtime_api_key = {
      type: "string",
      description: "Runtime key reference such as env:CONTROL_PLANE_API_KEY or file:/path.",
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
    err.redactionSecrets = newSecretSet();
    collectSensitiveStringValues(completed.stdout, err.redactionSecrets);
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
      control_plane_base_url: {
        type: "string",
        description: "Optional control-plane base URL override stored in the admin profile.",
      },
      control_plane_url_path: {
        type: "string",
        description: "Optional URL path appended to the control-plane base URL and stored in the admin profile.",
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
  validateControlPlaneOverride(args.control_plane_base_url);
  const out = ["runtimes", "create", "--alias", args.alias];
  appendRemoteScope(out, args);
  appendOptional(out, "--admin-profile", args.admin_profile);
  appendOptional(out, "--control-plane-base-url", args.control_plane_base_url);
  appendOptional(out, "--control-plane-url-path", args.control_plane_url_path);
  appendOptional(out, "--name", args.name);
  appendOptional(out, "--description", args.description);
  out.push("--json");
  return out;
}

function buildConnectArgs(args) {
  validateStdioCommand(args.mcp_command);
  validateRemoteScope(args, { allowTunnelId: true, required: true, command: "connect_stdio_mcp" });
  validateRuntimeAPIKey(args.runtime_api_key);
  validateControlPlaneOverride(args.control_plane_base_url);
  const out = ["runtimes", "connect", "--alias", args.alias, "--mcp-command", args.mcp_command];
  appendRemoteScope(out, args);
  appendOptional(out, "--tunnel-id", args.tunnel_id);
  appendOptional(out, "--runtime-api-key", args.runtime_api_key);
  appendOptional(out, "--admin-profile", args.admin_profile);
  appendOptional(out, "--control-plane-base-url", args.control_plane_base_url);
  appendOptional(out, "--control-plane-url-path", args.control_plane_url_path);
  appendOptional(out, "--name", args.name);
  appendOptional(out, "--description", args.description);
  out.push("--json");
  return out;
}

function buildListArgs(args) {
  validateListScope(args);
  validateControlPlaneOverride(args.control_plane_base_url);
  const out = ["runtimes", "list"];
  appendRemoteScope(out, args);
  appendOptional(out, "--tenant-id", args.tenant_id);
  appendOptional(out, "--admin-profile", args.admin_profile);
  appendOptional(out, "--control-plane-base-url", args.control_plane_base_url);
  appendOptional(out, "--control-plane-url-path", args.control_plane_url_path);
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
  if (containsInlineSecret(command)) {
    throw new Error(
      "mcp_command must not contain inline secret values; use env:NAME or file:/path references",
    );
  }
}

function validateRuntimeAPIKey(value) {
  const ref = trimString(value);
  if (!ref) {
    return;
  }
  if (
    (!/^env:[A-Za-z_][A-Za-z0-9_]*$/.test(ref) && !/^file:.+$/.test(ref)) ||
    containsInlineSecret(ref)
  ) {
    throw new Error("runtime_api_key must be a secret reference such as env:NAME or file:/path");
  }
}

function validateControlPlaneOverride(value) {
  const raw = trimString(value);
  if (!raw) {
    return;
  }

  const parsed = parseCanonicalControlPlaneOrigin(raw);
  if (!parsed) {
    throw new Error("control_plane_base_url must be an HTTP or HTTPS origin in authority form");
  }

  if (parsed.protocol === "https:" && ALLOWED_CONTROL_PLANE_ORIGINS.has(parsed.origin)) {
    return;
  }

  const configuredOrigin = parseCanonicalControlPlaneOrigin(
    trimString(process.env[CONTROL_PLANE_BASE_URL_ENV]),
  );
  if (configuredOrigin?.origin === parsed.origin) {
    return;
  }

  throw new Error(
    [
      "control_plane_base_url must be https://api.openai.com or https://mtls.api.openai.com.",
      "Use the native tunnel-client CLI for custom control planes,",
      `or exactly match the trusted ${CONTROL_PLANE_BASE_URL_ENV} origin in the plugin environment.`,
    ].join(" "),
  );
}

function parseCanonicalControlPlaneOrigin(raw) {
  if (!/^https?:\/\/[^/?#]+\/?$/i.test(raw)) {
    return null;
  }

  let parsed;
  try {
    parsed = new URL(raw);
  } catch {
    return null;
  }

  if (
    (parsed.protocol !== "https:" && parsed.protocol !== "http:") ||
    parsed.username ||
    parsed.password ||
    parsed.pathname !== "/" ||
    parsed.search ||
    parsed.hash
  ) {
    return null;
  }

  return parsed;
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

function containsInlineSecret(value) {
  const secrets = newSecretSet();
  collectSensitiveStringValues(String(value || ""), secrets, true);
  return secrets.overflow || secrets.size > 0;
}

function redactSensitiveString(value, knownSecrets = new Set()) {
  const original = String(value || "");
  if (knownSecrets.overflow) {
    return REDACTED;
  }
  const redactedOriginal = redactSensitiveStringPass(original, knownSecrets);
  let candidate = redactedOriginal;

  for (let pass = 1; pass <= MAX_ESCAPED_JSON_PASSES; pass += 1) {
    const unescapedJSON = unescapeJSONQuotes(candidate);
    if (unescapedJSON === candidate) {
      break;
    }
    const redactedUnescaped = redactSensitiveStringPass(unescapedJSON, knownSecrets);
    if (redactedUnescaped !== unescapedJSON) {
      return redactedUnescaped;
    }
    candidate = unescapedJSON;
  }

  return redactedOriginal;
}

function redactSensitiveStringPass(value, knownSecrets) {
  let text = String(value || "");
  if (hasUnparseableSensitiveJSONContainer(text)) {
    return REDACTED;
  }

  for (const secret of [...knownSecrets].sort((left, right) => right.length - left.length)) {
    if (secret && secret !== REDACTED) {
      text = text.split(secret).join(REDACTED);
    }
  }

  text = text.replace(new RegExp(OPENAI_STYLE_SECRET_PATTERN, "g"), REDACTED);
  text = text.replace(new RegExp(PRIVATE_KEY_PEM_PATTERN, "gi"), REDACTED);
  text = text.replace(
    new RegExp(BEARER_SECRET_PATTERN, "gi"),
    (match, prefix, secret) => isSecretReference(secret) ? match : prefix + REDACTED,
  );
  text = redactEmbeddedJSON(text, knownSecrets);
  text = redactYAMLBlocks(text);
  text = text.replace(new RegExp(SENSITIVE_ASSIGNMENT_PATTERN, "gi"), redactSensitiveAssignmentMatch);
  text = text.replace(new RegExp(SENSITIVE_FLAG_PATTERN, "gi"), redactSensitiveFlagMatch);
  text = text.replace(new RegExp(SENSITIVE_JSON_PATTERN, "gi"), redactSensitiveJSONMatch);
  text = redactSensitiveYAMLScalars(text);
  text = redactSensitiveHeaders(text);
  text = text.replace(
    new RegExp(URL_USERINFO_PATTERN, "gi"),
    (_match, scheme, _userinfo, at) => scheme + REDACTED + at,
  );
  return text;
}

function redactSensitiveValue(value, key = "", knownSecrets, depth = 0) {
  const secrets = knownSecrets || newSecretSet();
  if (depth >= MAX_REDACTION_DEPTH) {
    return value === null || value === undefined ? value : REDACTED;
  }
  if (isCommandFieldName(key)) {
    if (Array.isArray(value)) {
      return redactCommandArgs(value, secrets, depth);
    }
    if (typeof value === "string") {
      return redactCommandString(value, key, secrets);
    }
    return value === null || value === undefined ? value : REDACTED;
  }
  if (
    isSensitiveFieldName(key) &&
    !isNonSecretConfigurationName(key, value) &&
    value !== null &&
    value !== undefined
  ) {
    if (
      typeof value === "string" &&
      isSafeSecretLocator(key, value)
    ) {
      return value;
    }
    return redactSensitiveSubtree(value, depth);
  }
  if (typeof value === "string") {
    return redactSensitiveString(value, secrets);
  }
  if (Array.isArray(value)) {
    if (isSensitiveEntryTuple(value, key)) {
      return redactSensitiveEntryTuple(value, secrets, depth);
    }
    return value.map((entry) => redactSensitiveValue(entry, "", secrets, depth + 1));
  }
  if (!value || typeof value !== "object") {
    return value;
  }

  const out = {};
  const commandTarget = value.target_kind === "command";
  const sensitiveNameValue = sensitiveSiblingName(value);
  for (const [nestedKey, nestedValue] of Object.entries(value)) {
    const redactedKey = redactSensitivePropertyName(nestedKey, secrets);
    if (commandTarget && nestedKey === "target_value") {
      out[redactedKey] = nestedValue === null || nestedValue === undefined ? nestedValue : REDACTED;
      continue;
    }
    if (sensitiveNameValue && nestedKey === "value") {
      out[redactedKey] =
        typeof nestedValue === "string" && isSafeSecretLocator(sensitiveNameValue, nestedValue)
          ? nestedValue
          : nestedValue === null || nestedValue === undefined
            ? nestedValue
            : REDACTED;
      continue;
    }
    out[redactedKey] = redactSensitiveValue(nestedValue, nestedKey, secrets, depth + 1);
  }
  return out;
}

function redactSensitivePropertyName(key, secrets) {
  const text = String(key || "");
  if (isLikelySecretPropertyValue(text) && !isSensitiveFieldName(text)) {
    return REDACTED;
  }
  return redactSensitiveString(text, secrets);
}

function isLikelySecretPropertyValue(key) {
  const text = String(key || "");
  return (
    new RegExp(OPENAI_STYLE_SECRET_PATTERN).test(text) ||
    isSensitiveName(text)
  );
}

function redactCommandArgs(value, secrets, depth = 0) {
  const out = value.map((entry) => redactSensitiveValue(entry, "", secrets, depth + 1));
  for (let index = 0; index < value.length; index += 1) {
    const token = trimString(value[index]);
    if (token === "--mcp-command") {
      if (index + 1 < value.length) {
        out[index + 1] = REDACTED;
      }
      continue;
    }
    if (token.startsWith("--mcp-command=")) {
      out[index] = "--mcp-command=" + REDACTED;
      continue;
    }
    if (
      index + 1 < value.length &&
      isSensitiveFlagMatch("", token, value[index + 1]) &&
      typeof value[index + 1] === "string" &&
      !isSecretReference(value[index + 1])
    ) {
      out[index + 1] = REDACTED;
    }
  }
  return out;
}

function redactCommandString(value, key, secrets) {
  if (String(key || "") === "mcp_command") {
    return REDACTED;
  }
  const text = redactSensitiveString(value, secrets);
  return text.replace(
    /(--mcp-command(?:=|\s+))[\s\S]*$/gi,
    "$1" + REDACTED,
  );
}

function redactSensitiveSubtree(value, depth = 0) {
  if (depth >= MAX_REDACTION_DEPTH) {
    return value === null || value === undefined ? value : REDACTED;
  }
  if (value === null || value === undefined) {
    return value;
  }
  if (typeof value === "string") {
    return isSecretReference(value) ? value : REDACTED;
  }
  if (Array.isArray(value)) {
    return value.map((entry) => redactSensitiveSubtree(entry, depth + 1));
  }
  if (typeof value !== "object") {
    return REDACTED;
  }
  const out = {};
  for (const [nestedKey, nestedValue] of Object.entries(value)) {
    const outKey = isLikelySecretPropertyValue(nestedKey) ? REDACTED : nestedKey;
    out[outKey] = redactSensitiveSubtree(nestedValue, depth + 1);
  }
  return out;
}

function collectSensitiveValues(
  value,
  key = "",
  secrets = newSecretSet(),
  depth = 0,
  includeShort = false,
) {
  if (depth >= MAX_REDACTION_DEPTH) {
    secrets.overflow = true;
    return secrets;
  }
  if (isCommandFieldName(key)) {
    collectCommandValues(value, key, secrets, depth, includeShort);
    return secrets;
  }
  if (isSensitiveFieldName(key) && !isNonSecretConfigurationName(key, value)) {
    if (
      typeof value === "string" &&
      isSafeSecretLocator(key, value)
    ) {
      return secrets;
    }
    collectStringLeaves(value, secrets, depth, includeShort, true);
    return secrets;
  }
  if (typeof value === "string") {
    collectSensitiveStringValues(value, secrets, includeShort);
    return secrets;
  }
  if (Array.isArray(value)) {
    if (isSensitiveEntryTuple(value, key)) {
      collectSensitiveEntryTuple(value, secrets, depth, includeShort);
      return secrets;
    }
    for (const entry of value) {
      collectSensitiveValues(entry, key, secrets, depth + 1, includeShort);
    }
    return secrets;
  }
  if (!value || typeof value !== "object") {
    return secrets;
  }

  const commandTarget = value.target_kind === "command";
  const sensitiveNameValue = sensitiveSiblingName(value);
  for (const [nestedKey, nestedValue] of Object.entries(value)) {
    if (
      isSensitiveFieldName(key) ||
      (isLikelySecretPropertyValue(nestedKey) && !isSensitiveFieldName(nestedKey))
    ) {
      addSensitiveValue(secrets, nestedKey, includeShort);
    }
    if (commandTarget && nestedKey === "target_value") {
      collectStringLeaves(nestedValue, secrets, depth + 1, true);
      continue;
    }
    if (sensitiveNameValue && nestedKey === "value") {
      if (
        typeof nestedValue !== "string" ||
        !isSafeSecretLocator(sensitiveNameValue, nestedValue)
      ) {
        collectStringLeaves(nestedValue, secrets, depth + 1, true);
      }
      continue;
    }
    collectSensitiveValues(nestedValue, nestedKey, secrets, depth + 1, includeShort);
  }
  return secrets;
}

function collectCommandValues(value, key, secrets, depth = 0, includeShort = false) {
  if (depth >= MAX_REDACTION_DEPTH) {
    secrets.overflow = true;
    return;
  }
  if (Array.isArray(value)) {
    for (let index = 0; index < value.length; index += 1) {
      const entry = value[index];
      collectSensitiveValues(entry, "", secrets, depth + 1, includeShort);
      const token = trimString(entry);
      if (token === "--mcp-command" && typeof value[index + 1] === "string") {
        addSensitiveValue(secrets, value[index + 1], true);
        continue;
      }
      if (token.startsWith("--mcp-command=")) {
        addSensitiveValue(secrets, token.slice("--mcp-command=".length), true);
        continue;
      }
      if (
        index + 1 < value.length &&
        typeof value[index + 1] === "string" &&
        isSensitiveFlagMatch("", token, value[index + 1]) &&
        !isSecretReference(value[index + 1])
      ) {
        addSensitiveValue(secrets, value[index + 1], includeShort);
      }
    }
    return;
  }
  if (typeof value === "string") {
    if (String(key || "") === "mcp_command") {
      addSensitiveValue(secrets, value, true);
      return;
    }
    collectSensitiveStringValues(value, secrets, includeShort);
    for (const match of value.matchAll(/--mcp-command(?:=|\s+)([\s\S]*)$/gi)) {
      addSensitiveValue(secrets, match[1], true);
    }
    return;
  }
  if (value !== null && value !== undefined) {
    collectStringLeaves(value, secrets, depth + 1, includeShort);
  }
}

function collectStringLeaves(value, secrets, depth = 0, includeShort = false, includeKeys = false) {
  if (depth >= MAX_REDACTION_DEPTH) {
    secrets.overflow = true;
    return;
  }
  if (typeof value === "string") {
    addSensitiveValue(secrets, value, includeShort);
    return;
  }
  if (Array.isArray(value)) {
    for (const entry of value) {
      collectStringLeaves(entry, secrets, depth + 1, includeShort, includeKeys);
    }
    return;
  }
  if (value === null || value === undefined) {
    return;
  }
  if (typeof value !== "object") {
    addSensitiveValue(secrets, value, includeShort);
    return;
  }
  for (const [nestedKey, nestedValue] of Object.entries(value)) {
    if (includeKeys && isLikelySecretPropertyValue(nestedKey)) {
      addSensitiveValue(secrets, nestedKey, includeShort);
    }
    collectStringLeaves(nestedValue, secrets, depth + 1, includeShort, includeKeys);
  }
}

function unescapeJSONQuotes(value) {
  return String(value || "").replace(/\\+"/g, '"');
}

function newSecretSet() {
  const secrets = new Set();
  secrets.overflow = false;
  secrets.totalChars = 0;
  return secrets;
}

function copySecretSet(values) {
  const secrets = newSecretSet();
  if (!values) {
    return secrets;
  }
  for (const value of values) {
    addSensitiveValue(secrets, value, true);
  }
  secrets.overflow = Boolean(secrets.overflow || values.overflow);
  return secrets;
}

function hasUnparseableSensitiveJSONContainer(value) {
  const text = String(value || "");
  if (text.length > MAX_EMBEDDED_JSON_CANDIDATE_CHARS) {
    for (const match of text.matchAll(/"([A-Za-z0-9_.-]+)"\s*:\s*[\[{]/gi)) {
      if (isSensitiveName(match[1])) {
        return true;
      }
    }
    return false;
  }
  const pattern = /("([A-Za-z0-9_.-]+)"\s*:\s*)([\[{])/gi;
  let scannedContainers = 0;
  for (const match of text.matchAll(pattern)) {
    if (!isSensitiveName(match[2])) {
      continue;
    }
    const containerStart = match.index + match[1].length;
    scannedContainers += 1;
    if (scannedContainers > MAX_EMBEDDED_JSON_CANDIDATES) {
      return true;
    }
    if (!isParseableJSONContainerAt(text, containerStart)) {
      return true;
    }
  }
  return false;
}

function isParseableJSONContainerAt(text, start) {
  const first = text[start];
  if (first !== "{" && first !== "[") {
    return false;
  }
  const stack = [first];
  let quoted = false;
  let escaped = false;
  for (let index = start + 1; index < text.length; index += 1) {
    const char = text[index];
    if (quoted) {
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === '"') {
        quoted = false;
      }
      continue;
    }
    if (char === '"') {
      quoted = true;
      continue;
    }
    if (char === "{" || char === "[") {
      if (stack.length >= MAX_REDACTION_DEPTH) {
        return false;
      }
      stack.push(char);
      continue;
    }
    if (char !== "}" && char !== "]") {
      continue;
    }
    const expected = char === "}" ? "{" : "[";
    if (stack.pop() !== expected) {
      return false;
    }
    if (!stack.length) {
      try {
        JSON.parse(text.slice(start, index + 1));
        return true;
      } catch {
        return false;
      }
    }
  }
  return false;
}

function forEachEmbeddedJSON(value, visitor) {
  const text = String(value || "");
  let start = -1;
  let stack = [];
  let nestedCandidates = [];
  let nestedCandidateChars = 0;
  let overflow = false;
  let quoted = false;
  let escaped = false;

  const visit = (candidateStart, candidateEnd) => {
    try {
      visitor(JSON.parse(text.slice(candidateStart, candidateEnd)), candidateStart, candidateEnd);
      return true;
    } catch {
      return false;
    }
  };
  const visitNestedCandidates = () => {
    const selected = [];
    for (const candidate of nestedCandidates.sort(
      (left, right) => left.start - right.start || right.end - left.end,
    )) {
      if (
        selected.some(
          (range) => candidate.start >= range.start && candidate.end <= range.end,
        )
      ) {
        continue;
      }
      if (visit(candidate.start, candidate.end)) {
        selected.push(candidate);
      }
    }
  };
  const reset = () => {
    start = -1;
    stack = [];
    nestedCandidates = [];
    nestedCandidateChars = 0;
    quoted = false;
    escaped = false;
  };

  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (start === -1) {
      if (char === "{" || char === "[") {
        start = index;
        stack = [{ char, index }];
      }
      continue;
    }
    if (quoted) {
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === '"') {
        quoted = false;
      }
      continue;
    }
    if (char === '"') {
      quoted = true;
      continue;
    }
    if (char === "{" || char === "[") {
      if (stack.length >= MAX_REDACTION_DEPTH) {
        overflow = true;
        reset();
        continue;
      }
      stack.push({ char, index });
      continue;
    }
    if (char !== "}" && char !== "]") {
      continue;
    }
    const expected = char === "}" ? "{" : "[";
    const opener = stack.pop();
    if (!opener || opener.char !== expected) {
      visitNestedCandidates();
      reset();
      continue;
    }
    if (stack.length) {
      const candidateChars = index + 1 - opener.index;
      if (
        nestedCandidates.length >= MAX_EMBEDDED_JSON_CANDIDATES ||
        nestedCandidateChars + candidateChars > MAX_EMBEDDED_JSON_CANDIDATE_CHARS
      ) {
        overflow = true;
      } else {
        nestedCandidates.push({ start: opener.index, end: index + 1 });
        nestedCandidateChars += candidateChars;
      }
      continue;
    }
    if (!visit(start, index + 1)) {
      visitNestedCandidates();
    }
    reset();
  }
  if (start !== -1) {
    visitNestedCandidates();
  }
  return overflow;
}

function redactEmbeddedJSON(value, knownSecrets = newSecretSet()) {
  const text = String(value || "");
  let cursor = 0;
  let out = "";
  const overflow = forEachEmbeddedJSON(text, (parsed, start, end) => {
    const secrets = copySecretSet(knownSecrets);
    collectSensitiveValues(parsed, "", secrets);
    const sanitized = redactSensitiveValue(parsed, "", secrets);
    if (JSON.stringify(parsed) !== JSON.stringify(sanitized)) {
      out += text.slice(cursor, start) + JSON.stringify(sanitized);
      cursor = end;
    }
  });
  if (overflow) {
    return REDACTED;
  }
  return cursor ? out + text.slice(cursor) : text;
}

function redactYAMLBlocks(value) {
  return String(value || "").replace(
    new RegExp(YAML_BLOCK_PATTERN, "gmi"),
    (match, lead, indent, quote, key, body) =>
      isSensitiveName(key) && !isNonSecretConfigurationName(key, body.trim())
        ? lead + indent + quote + key + quote + ": " + REDACTED + "\n"
        : match,
  );
}

function redactSensitiveYAMLScalars(value) {
  const text = String(value || "");
  if (hasSensitiveYAMLMappingParent(text) || hasSensitiveYAMLFlowMapping(text)) {
    return REDACTED;
  }
  return text.replace(
    new RegExp(SENSITIVE_YAML_SCALAR_PATTERN, "gmi"),
    (match, lead, quote, key, quotedDouble, quotedSingle, bare) => {
      const secret = quotedDouble ?? quotedSingle ?? bare;
      if (isYAMLBlockIndicator(secret)) {
        return match;
      }
      if (!isSensitiveJSONMatch("", key, secret)) {
        return match;
      }
      return redactSensitiveMatch(
        match,
        lead + quote + key + quote + ": ",
        key,
        quotedDouble,
        quotedSingle,
        bare,
      );
    },
  );
}

function collectSensitiveStringValues(value, secrets, includeShort = false) {
  let text = String(value || "");
  for (let pass = 0; pass <= MAX_ESCAPED_JSON_PASSES; pass += 1) {
    collectSensitiveStringValuesPass(text, secrets, includeShort);
    const unescapedJSON = unescapeJSONQuotes(text);
    if (unescapedJSON === text) {
      break;
    }
    text = unescapedJSON;
  }
}

function collectSensitiveStringValuesPass(text, secrets, includeShort) {
  if (hasUnparseableSensitiveJSONContainer(text)) {
    secrets.overflow = true;
    return;
  }
  if (hasSensitiveYAMLMappingParent(text)) {
    collectSensitiveYAMLMappingValues(text, secrets, includeShort);
  }
  collectSensitiveYAMLFlowValues(text, secrets, includeShort);
  collectEmbeddedJSONValues(text, secrets, includeShort);
  collectYAMLBlockValues(text, secrets, includeShort);
  for (const match of text.matchAll(new RegExp(OPENAI_STYLE_SECRET_PATTERN, "g"))) {
    addSensitiveValue(secrets, match[0], includeShort);
  }
  for (const match of text.matchAll(new RegExp(PRIVATE_KEY_PEM_PATTERN, "gi"))) {
    addSensitiveValue(secrets, match[0], true);
  }
  for (const match of text.matchAll(new RegExp(BEARER_SECRET_PATTERN, "gi"))) {
    addSensitiveValue(secrets, match[2], includeShort);
  }
  collectSensitivePatternValues(text, SENSITIVE_ASSIGNMENT_PATTERN, secrets, includeShort, isSensitiveAssignmentMatch);
  collectSensitivePatternValues(text, SENSITIVE_FLAG_PATTERN, secrets, includeShort, isSensitiveFlagMatch);
  collectSensitivePatternValues(text, SENSITIVE_JSON_PATTERN, secrets, includeShort, isSensitiveJSONMatch);
  collectSensitiveYAMLScalarValues(text, secrets, includeShort);
  collectSensitiveHeaderValues(text, secrets, includeShort);
  for (const match of text.matchAll(new RegExp(URL_USERINFO_PATTERN, "gi"))) {
    addSensitiveValue(secrets, match[2], includeShort, true);
  }
}

function collectEmbeddedJSONValues(value, secrets, includeShort = false) {
  if (
    forEachEmbeddedJSON(
      value,
      (parsed) => collectSensitiveValues(parsed, "", secrets, 0, includeShort),
    )
  ) {
    secrets.overflow = true;
  }
}

function collectYAMLBlockValues(value, secrets, includeShort) {
  for (const match of String(value || "").matchAll(
    new RegExp(YAML_BLOCK_PATTERN, "gmi"),
  )) {
    if (!isSensitiveName(match[4]) || isNonSecretConfigurationName(match[4], match[5].trim())) {
      continue;
    }
    const body = match[5].trim();
    addSensitiveValue(secrets, body, includeShort);
    for (const line of body.split(/\r?\n/)) {
      addSensitiveValue(secrets, line.trim(), includeShort);
    }
  }
}

function addSensitiveValue(secrets, value, includeShort = false, allowReference = false) {
  const text = String(value || "");
  if (
    text === "" ||
    (!includeShort && text.length <= 4) ||
    (!allowReference && isSecretReferenceOrBearerReference(text)) ||
    text === REDACTED ||
    secrets.has(text)
  ) {
    return;
  }
  const nextChars = (secrets.totalChars || 0) + text.length;
  if (secrets.size >= MAX_KNOWN_SECRETS || nextChars > MAX_KNOWN_SECRET_CHARS) {
    secrets.overflow = true;
    return;
  }
  secrets.add(text);
  secrets.totalChars = nextChars;
}

function redactSensitiveMatch(match, prefix, _key, quotedDouble, quotedSingle, bare) {
  const secret = quotedDouble ?? quotedSingle ?? bare;
  if (isSecretReferenceOrBearerReference(secret)) {
    return match;
  }
  if (quotedDouble !== undefined) {
    return prefix + '"' + REDACTED + '"';
  }
  if (quotedSingle !== undefined) {
    return prefix + "'" + REDACTED + "'";
  }
  return prefix + REDACTED;
}

function redactSensitiveAssignmentMatch(match, prefix, key, quotedDouble, quotedSingle, bare) {
  const secret = quotedDouble ?? quotedSingle ?? bare;
  if (!isSensitiveAssignmentMatch(prefix, key, secret)) {
    return match;
  }
  return redactSensitiveMatch(match, prefix, key, quotedDouble, quotedSingle, bare);
}

function redactSensitiveFlagMatch(match, prefix, key, quotedDouble, quotedSingle, bare) {
  const secret = quotedDouble ?? quotedSingle ?? bare;
  if (!isSensitiveFlagMatch(prefix, key, secret)) {
    return match;
  }
  return redactSensitiveMatch(match, prefix, key, quotedDouble, quotedSingle, bare);
}

function redactSensitiveJSONMatch(match, prefix, key, quotedDouble, quotedSingle, bare) {
  const secret = quotedDouble ?? quotedSingle ?? bare;
  if (!isSensitiveJSONMatch(prefix, key, secret)) {
    return match;
  }
  return redactSensitiveMatch(match, prefix, key, quotedDouble, quotedSingle, bare);
}

function collectSensitiveYAMLScalarValues(text, secrets, includeShort) {
  for (const match of text.matchAll(new RegExp(SENSITIVE_YAML_SCALAR_PATTERN, "gmi"))) {
    const secret = decodeYAMLScalarCapture(match[4], match[5], match[6]);
    if (isYAMLBlockIndicator(secret)) {
      continue;
    }
    if (isSensitiveJSONMatch("", match[3], secret)) {
      addSensitiveValue(secrets, secret, includeShort);
    }
  }
}

function isYAMLBlockIndicator(value) {
  return /^(?:[|>](?:[1-9][-+]?|[-+][1-9]?)?)$/.test(String(value || ""));
}

function hasSensitiveYAMLMappingParent(value) {
  const lines = String(value || "").split(/\r?\n/);
  for (let index = 0; index < lines.length - 1; index += 1) {
    if (sensitiveYAMLMappingEnd(lines, index) !== -1) {
      return true;
    }
  }
  return false;
}

function hasSensitiveYAMLFlowMapping(value) {
  let found = false;
  forEachSensitiveYAMLFlowMapping(value, () => {
    found = true;
  });
  return found;
}

function collectSensitiveYAMLFlowValues(value, secrets, includeShort) {
  forEachSensitiveYAMLFlowMapping(value, (body, complete, opener) => {
    if (!complete) {
      secrets.overflow = true;
      return;
    }
    if (opener === "[") {
      collectYAMLFlowSequenceValues("[" + body + "]", secrets, includeShort);
    } else {
      collectYAMLFlowScalarValues(body, secrets, includeShort);
    }
  });
}

function forEachSensitiveYAMLFlowMapping(value, visit) {
  const text = String(value || "");
  const embeddedJSONRanges = [];
  forEachEmbeddedJSON(text, (_parsed, start, end) => {
    embeddedJSONRanges.push([start, end]);
  });
  const pattern =
    /(?:^|[^A-Za-z0-9_])(?:(["'])([^"'\r\n]+)\1|([A-Za-z0-9_][A-Za-z0-9_.-]*))\s*:\s*(?:(?:&[A-Za-z0-9_.-]+|![A-Za-z0-9_.!:/-]+)\s+)*([{\[])/g;
  for (let match = pattern.exec(text); match; match = pattern.exec(text)) {
    const key = match[2] ?? match[3];
    if (!isSensitiveName(key) || isNonSecretConfigurationName(key)) {
      continue;
    }
    const opener = match[4];
    const containerStart = pattern.lastIndex - 1;
    const containerEnd = matchingYAMLFlowContainerEnd(text, containerStart);
    if (containerEnd === -1) {
      if (
        embeddedJSONRanges.some(
          ([start, end]) => containerStart >= start && containerStart < end,
        )
      ) {
        continue;
      }
      visit(text.slice(containerStart + 1), false, opener);
      return;
    }
    if (
      embeddedJSONRanges.some(
        ([start, end]) => containerStart >= start && containerEnd < end,
      )
    ) {
      pattern.lastIndex = containerEnd + 1;
      continue;
    }
    visit(text.slice(containerStart + 1, containerEnd), true, opener);
    pattern.lastIndex = containerEnd + 1;
  }
}

function matchingYAMLFlowContainerEnd(value, containerStart) {
  const text = String(value || "");
  const stack = [];
  let quote = "";
  let escaped = false;
  for (let index = containerStart; index < text.length; index += 1) {
    const char = text[index];
    if (quote === '"') {
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === quote) {
        quote = "";
      }
      continue;
    }
    if (quote === "'") {
      if (char === quote && text[index + 1] === quote) {
        index += 1;
      } else if (char === quote) {
        quote = "";
      }
      continue;
    }
    if (char === "'" || char === '"') {
      quote = char;
      continue;
    }
    if (char === "{" || char === "[") {
      stack.push(char);
      continue;
    }
    if (char !== "}" && char !== "]") {
      continue;
    }
    const opener = stack.pop();
    if (
      !opener ||
      (char === "}" && opener !== "{") ||
      (char === "]" && opener !== "[")
    ) {
      return -1;
    }
    if (stack.length === 0) {
      return index;
    }
  }
  return -1;
}

function collectYAMLFlowScalarValues(value, secrets, includeShort) {
  const text = String(value || "");
  const pattern = /:\s*(?:"((?:\\.|[^"\\])*)"|'((?:''|\\.|[^'\\])*)'|([^,\s}\]]+))/g;
  for (const match of text.matchAll(pattern)) {
    const raw = decodeYAMLScalarCapture(match[1], match[2], match[3]);
    if (!raw || isYAMLBlockIndicator(raw)) {
      continue;
    }
    if (/^[{[]/.test(raw)) {
      collectYAMLFlowScalarValues(raw, secrets, includeShort);
      collectYAMLFlowSequenceValues(raw, secrets, includeShort);
      continue;
    }
    addSensitiveValue(secrets, raw, includeShort);
  }
  collectYAMLFlowSequenceValues(text, secrets, includeShort);
}

function collectYAMLFlowSequenceValues(value, secrets, includeShort) {
  const text = String(value || "");
  const itemPattern =
    /"((?:\\.|[^"\\])*)"|'((?:''|\\.|[^'\\])*)'|([^\s,\[\]{}:]+)/g;
  for (const item of text.matchAll(itemPattern)) {
    const end = item.index + item[0].length;
    const next = text.slice(end).match(/^\s*(.)/);
    if (next && next[1] === ":") {
      continue;
    }
    const raw = decodeYAMLScalarCapture(item[1], item[2], item[3]);
    if (!raw || isYAMLBlockIndicator(raw)) {
      continue;
    }
    addSensitiveValue(secrets, raw, includeShort);
  }
}

function collectSensitiveYAMLMappingValues(value, secrets, includeShort) {
  const lines = String(value || "").split(/\r?\n/);
  for (let index = 0; index < lines.length - 1; index += 1) {
    const end = sensitiveYAMLMappingEnd(lines, index);
    if (end === -1) {
      continue;
    }
    for (let childIndex = index + 1; childIndex < end; childIndex += 1) {
      const trimmed = lines[childIndex].trim();
      if (!trimmed) {
        continue;
      }
      const scalar = trimmed.match(
        /^(?:(?:["'][^"'\r\n]+["']|[A-Za-z0-9_][A-Za-z0-9_.-]*)\s*:\s*)?(.+)$/,
      );
      if (!scalar) {
        continue;
      }
      const raw = stripYAMLInlineComment(
        scalar[1].trim().replace(/^-\s+/, ""),
      );
      if (!raw || isYAMLBlockIndicator(raw)) {
        continue;
      }
      if (/^[{[]/.test(raw)) {
        collectYAMLFlowScalarValues(raw, secrets, includeShort);
      }
      addSensitiveValue(secrets, decodeYAMLScalar(raw), includeShort);
    }
    index = end - 1;
  }
}

function decodeYAMLScalarCapture(quotedDouble, quotedSingle, bare) {
  if (quotedDouble !== undefined) {
    return decodeYAMLScalar('"' + quotedDouble + '"');
  }
  if (quotedSingle !== undefined) {
    return decodeYAMLScalar("'" + quotedSingle + "'");
  }
  return stripYAMLInlineComment(String(bare || "").trim());
}

function decodeYAMLScalar(value) {
  const text = stripYAMLInlineComment(String(value || "").trim());
  if (text.length >= 2 && text.startsWith("'") && text.endsWith("'")) {
    return text.slice(1, -1).replace(/''/g, "'");
  }
  if (text.length >= 2 && text.startsWith('"') && text.endsWith('"')) {
    return decodeYAMLDoubleQuotedScalar(text.slice(1, -1));
  }
  return text;
}

function decodeYAMLDoubleQuotedScalar(value) {
  const text = String(value || "");
  const simpleEscapes = {
    "0": "\0",
    a: "\x07",
    b: "\b",
    t: "\t",
    n: "\n",
    v: "\v",
    f: "\f",
    r: "\r",
    e: "\x1b",
    " ": " ",
    '"': '"',
    "/": "/",
    "\\": "\\",
    N: "\x85",
    _: "\xa0",
    L: "\u2028",
    P: "\u2029",
  };
  let out = "";
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (char !== "\\" || index + 1 >= text.length) {
      out += char;
      continue;
    }
    const next = text[index + 1];
    if (Object.prototype.hasOwnProperty.call(simpleEscapes, next)) {
      out += simpleEscapes[next];
      index += 1;
      continue;
    }
    const width = next === "x" ? 2 : next === "u" ? 4 : next === "U" ? 8 : 0;
    const hex = text.slice(index + 2, index + 2 + width);
    if (width && hex.length === width && /^[0-9A-Fa-f]+$/.test(hex)) {
      const codePoint = Number.parseInt(hex, 16);
      if (codePoint <= 0x10ffff) {
        out += String.fromCodePoint(codePoint);
        index += width + 1;
        continue;
      }
    }
    out += "\\" + next;
    index += 1;
  }
  return out;
}

function sensitiveYAMLMappingEnd(lines, index) {
  const parent = sensitiveYAMLMappingParent(lines[index]);
  if (!parent) {
    return -1;
  }
  const parentIndent = lines[index].match(/^[ \t]*/)[0].length;
  let end = index + 1;
  let hasChild = false;
  for (; end < lines.length; end += 1) {
    const child = lines[end];
    if (!child.trim()) {
      continue;
    }
    const childIndent = child.match(/^[ \t]*/)[0].length;
    if (childIndent <= parentIndent) {
      break;
    }
    hasChild = true;
  }
  return hasChild ? end : -1;
}

function sensitiveYAMLMappingParent(value) {
  const text = String(value || "");
  const pattern =
    /(?:(["'])([^"'\r\n]+)\1|([A-Za-z0-9_][A-Za-z0-9_.-]*))\s*:\s*/g;
  let candidate = null;
  for (const match of text.matchAll(pattern)) {
    if (match.index > 0 && /[A-Za-z0-9_]/.test(text[match.index - 1])) {
      continue;
    }
    const key = match[2] ?? match[3];
    if (!isSensitiveName(key) || isNonSecretConfigurationName(key)) {
      continue;
    }
    const parentValue = stripYAMLInlineComment(
      text.slice(match.index + match[0].length).trim(),
    );
    if (
      parentValue &&
      !/^(?:(?:&[A-Za-z0-9_.-]+|![A-Za-z0-9_.!:/-]+)\s*)+$/.test(parentValue)
    ) {
      continue;
    }
    candidate = key;
  }
  return candidate;
}

function stripYAMLInlineComment(value) {
  const text = String(value || "");
  let quote = "";
  let escaped = false;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (quote === '"') {
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === quote) {
        quote = "";
      }
      continue;
    }
    if (quote === "'") {
      if (char === quote && text[index + 1] === quote) {
        index += 1;
      } else if (char === quote) {
        quote = "";
      }
      continue;
    }
    if (char === "'" || char === '"') {
      quote = char;
      continue;
    }
    if (char === "#" && (index === 0 || /\s/.test(text[index - 1]))) {
      return text.slice(0, index).trimEnd();
    }
  }
  return text;
}

function redactSensitiveHeaderMatch(match, prefix, key, quotedDouble, quotedSingle, bare) {
  const secret = quotedDouble ?? quotedSingle ?? bare;
  if (!isSensitiveHeaderMatch(prefix, key, secret)) {
    return match;
  }
  return redactSensitiveMatch(match, prefix, key, quotedDouble, quotedSingle, bare);
}

function redactSensitiveHeaders(value) {
  const text = String(value || "");
  const pattern = new RegExp(SENSITIVE_HEADER_PATTERN, "gi");
  let cursor = 0;
  let out = "";
  let changed = false;
  for (let match = pattern.exec(text); match; match = pattern.exec(text)) {
    const secret = secretValueFromMatch(match);
    if (!isSensitiveHeaderMatch(match[1], match[2], secret)) {
      pattern.lastIndex = match.index + 1;
      continue;
    }
    out +=
      text.slice(cursor, match.index) +
      redactSensitiveHeaderMatch(...match);
    cursor = pattern.lastIndex;
    changed = true;
  }
  return changed ? out + text.slice(cursor) : text;
}

function collectSensitivePatternValues(text, pattern, secrets, includeShort, predicate) {
  for (const match of text.matchAll(new RegExp(pattern, "gi"))) {
    const secret = secretValueFromMatch(match);
    if (predicate(match[1], match[2], secret)) {
      addSensitiveValue(secrets, secret, includeShort);
    }
  }
}

function collectSensitiveHeaderValues(text, secrets, includeShort) {
  const pattern = new RegExp(SENSITIVE_HEADER_PATTERN, "gi");
  for (let match = pattern.exec(text); match; match = pattern.exec(text)) {
    const secret = secretValueFromMatch(match);
    if (!isSensitiveHeaderMatch(match[1], match[2], secret)) {
      pattern.lastIndex = match.index + 1;
      continue;
    }
    addSensitiveValue(secrets, secret, includeShort);
  }
}

function isSensitiveAssignmentMatch(_prefix, key, value) {
  return (
    String(value || "") !== "" &&
    isSensitiveName(key) &&
    !isNonSecretConfigurationName(key, value) &&
    !isSafeSecretLocator(key, value)
  );
}

function isSensitiveFlagMatch(_prefix, key, value) {
  return (
    String(value || "") !== "" &&
    isSensitiveName(key) &&
    !isNonSecretConfigurationName(key, value) &&
    !isSafeSecretLocator(key, value)
  );
}

function isSensitiveJSONMatch(_prefix, key, value) {
  return (
    String(value || "") !== "" &&
    isSensitiveName(key) &&
    !isNonSecretConfigurationName(key, value) &&
    !isSafeSecretLocator(key, value)
  );
}

function isSensitiveHeaderMatch(_prefix, key, value) {
  return (
    String(value || "") !== "" &&
    !isYAMLBlockIndicator(value) &&
    isSensitiveName(key) &&
    !isNonSecretConfigurationName(key, value) &&
    !isSafeSecretLocator(key, value)
  );
}

function isEntryTupleContainerName(value) {
  const segments = nameSegments(value);
  const last = segments[segments.length - 1];
  return ["header", "headers", "environment", "environments", "env"].includes(last);
}

function isSensitiveEntryTuple(value, key) {
  return (
    isEntryTupleContainerName(key) &&
    Array.isArray(value) &&
    value.length >= 2 &&
    typeof value[0] === "string" &&
    isSensitiveName(value[0]) &&
    !isNonSecretConfigurationName(value[0], value[1])
  );
}

function collectSensitiveEntryTuple(value, secrets, depth = 0, includeShort = false) {
  const name = value[0];
  const entryValue = value[1];
  if (!(typeof entryValue === "string" && isSafeSecretLocator(name, entryValue))) {
    collectStringLeaves(entryValue, secrets, depth + 1, true);
  }
  for (let index = 2; index < value.length; index += 1) {
    collectSensitiveValues(value[index], "", secrets, depth + 1, includeShort);
  }
}

function redactSensitiveEntryTuple(value, secrets, depth = 0) {
  const out = value.map((entry) => redactSensitiveValue(entry, "", secrets, depth + 1));
  const name = value[0];
  const entryValue = value[1];
  if (!(typeof entryValue === "string" && isSafeSecretLocator(name, entryValue))) {
    out[1] = entryValue === null || entryValue === undefined ? entryValue : REDACTED;
  }
  return out;
}

function isNonSecretConfigurationName(value, configuredValue) {
  const segments = nameSegments(value);
  const last = segments[segments.length - 1];
  const text =
    configuredValue === null || configuredValue === undefined
      ? ""
      : String(configuredValue).trim();
  if (last === "url" || last === "uri") {
    return isSafeConfigurationURL(text);
  }
  if (last === "domain") {
    return /^\.?[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$/.test(text);
  }
  if (last === "length" || last === "min" || last === "max") {
    return /^\d+$/.test(text);
  }
  if (last === "policy") {
    return /^(?:required|optional|strict|lenient|default|none|allow|deny|prompt|auto)$/i.test(text);
  }
  if (last === "enabled") {
    return /^(?:true|false|0|1)$/i.test(text);
  }
  if (last === "header") {
    return /^(?:Authorization|Proxy-Authorization|X-Api-Key|Api-Key|X-Auth-Token|X-Access-Token)$/i.test(text);
  }
  if (last === "algorithm") {
    return /^(?:argon2(?:id|i|d)?|bcrypt|scrypt|pbkdf2|sha-?[0-9]+|hmac-[a-z0-9-]+|hs[0-9]+|rs[0-9]+|es[0-9]+)$/i.test(text);
  }
  return false;
}

function isSafeConfigurationURL(value) {
  const text = trimString(value);
  if (!text || /\s/.test(text) || new RegExp(OPENAI_STYLE_SECRET_PATTERN).test(text)) {
    return false;
  }
  try {
    const parsed = new URL(text);
    if (!/^https?:$/.test(parsed.protocol) || parsed.username || parsed.password) {
      return false;
    }
    for (const [key, entryValue] of parsed.searchParams.entries()) {
      if (
        isSensitiveName(key) &&
        !isNonSecretConfigurationName(key, entryValue) &&
        !isSecretReferenceOrBearerReference(entryValue)
      ) {
        return false;
      }
    }
    if (hasInlineSensitiveURLFragment(parsed.hash)) {
      return false;
    }
    return true;
  } catch {
    return false;
  }
}

function hasInlineSensitiveURLFragment(value) {
  const fragment = decodeURLComponent(String(value || "").replace(/^#/, ""));
  for (const component of fragment.split(/[&#?]/)) {
    const separator = component.search(/[=:]/);
    if (separator <= 0) {
      continue;
    }
    const key = decodeURLComponent(component.slice(0, separator).trim());
    const entryValue = decodeURLComponent(component.slice(separator + 1).trim());
    if (
      entryValue &&
      isSensitiveName(key) &&
      !isNonSecretConfigurationName(key, entryValue) &&
      !isSecretReferenceOrBearerReference(entryValue)
    ) {
      return true;
    }
  }
  return false;
}

function decodeURLComponent(value) {
  try {
    return decodeURIComponent(String(value || ""));
  } catch {
    return String(value || "");
  }
}

function isCredentialFileAssignment(key, value) {
  const name = String(key || "").toUpperCase();
  if (
    name !== "GOOGLE_APPLICATION_CREDENTIALS" &&
    !/(?:CREDENTIALS|SECRET|KEY)_FILE$/.test(name)
  ) {
    return false;
  }
  return isPathShapedValue(value);
}

function isPathLocatorName(value, pathValue) {
  const segments = nameSegments(value);
  const last = segments[segments.length - 1];
  if (!["file", "path", "env", "name"].includes(last)) {
    return false;
  }
  if (pathValue === undefined) {
    return true;
  }
  const text = trimString(pathValue);
  if (last === "env" || last === "name") {
    return /^[A-Za-z_][A-Za-z0-9_]*$/.test(text);
  }
  return text === "" || isPathShapedValue(text);
}

function isPathShapedValue(value) {
  const text = trimString(value);
  return (
    /^(?:[/.~]|[A-Za-z]:[\\/])/.test(text) ||
    /[\\/]/.test(text) ||
    /^[A-Za-z0-9][A-Za-z0-9._-]*\.[A-Za-z0-9]{1,16}$/.test(text)
  );
}

function isPrivateKeyPathLocator(key, value) {
  const segments = nameSegments(key);
  return (
    segments.length >= 2 &&
    segments[segments.length - 2] === "private" &&
    segments[segments.length - 1] === "key" &&
    isPathShapedValue(value)
  );
}

function isSafeSecretLocator(key, value) {
  return (
    isPathLocatorName(key, value) ||
    isCredentialFileAssignment(key, value) ||
    isPrivateKeyPathLocator(key, value)
  );
}

function nameSegments(value) {
  return String(value || "")
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .replace(/\[/g, "_")
    .replace(/\]/g, "")
    .replace(/^-+/, "")
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .filter(Boolean);
}

function isSensitiveName(value) {
  const segments = nameSegments(value);
  if (!segments.length) {
    return false;
  }
  const joined = segments.join("");
  if (
    /(?:apikey|adminkey|runtimetoken|accesstoken|refreshtoken|idtoken|clientsecret|authtoken|privatekey|setcookie)/.test(joined)
  ) {
    return true;
  }
  if (segments.some((segment) => /^(?:password|pass|passwd|passphrase|authorization|cookie)$/.test(segment))) {
    return true;
  }
  if (segments.includes("credentials") || segments.includes("credential")) {
    return true;
  }
  if (segments.includes("token")) {
    return (
      segments.length === 1 ||
      segments[segments.length - 1] === "token" ||
      ["file", "path", "env", "name"].includes(segments[segments.length - 1]) ||
      segments.includes("value") ||
      segments.includes("secret") ||
      segments.includes("key")
    );
  }
  if (segments.includes("secret")) {
    return (
      segments.length === 1 ||
      segments[segments.length - 1] === "secret" ||
      ["file", "path", "env", "name"].includes(segments[segments.length - 1]) ||
      segments.includes("key") ||
      segments.includes("value") ||
      segments.includes("client") ||
      segments.includes("access")
    );
  }
  return false;
}

function isSensitiveFieldName(value) {
  return isSensitiveName(value);
}

function sensitiveSiblingName(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return "";
  }
  const name = typeof value.name === "string" ? value.name : value.key;
  return typeof name === "string" &&
    isSensitiveName(name) &&
    !isNonSecretConfigurationName(name, value.value)
    ? name
    : "";
}

function isCommandFieldName(value) {
  return /^(?:command|mcp_command|live_process_command)$/.test(String(value || ""));
}

function isSensitiveFlagToken(value) {
  const token = trimString(value);
  return Boolean(token) && !token.includes("=") && isSensitiveName(token);
}

function isSecretReference(value) {
  const text = trimString(value);
  return (
    /^env:[A-Za-z_][A-Za-z0-9_]*$/.test(text) ||
    /^file:.+$/.test(text) ||
    /^\$[A-Za-z_][A-Za-z0-9_]*$/.test(text) ||
    /^\$\{[A-Za-z_][A-Za-z0-9_]*\}$/.test(text) ||
    /^%[A-Za-z_][A-Za-z0-9_]*%$/.test(text) ||
    /^\$env:[A-Za-z_][A-Za-z0-9_]*$/i.test(text)
  );
}

function isSecretReferenceOrBearerReference(value) {
  const text = trimString(value).replace(/^['"]|['"]$/g, "");
  if (isSecretReference(text)) {
    return true;
  }
  const authorization = text.match(/^(?:Bearer|Basic)\s+(.+)$/i);
  return Boolean(authorization && isSecretReference(authorization[1]));
}

function secretValueFromMatch(match) {
  return match[3] ?? match[4] ?? match[5] ?? "";
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
    return redactRpcResponse({
      jsonrpc: "2.0",
      id: message.id,
      result: {
        protocolVersion: "2025-06-18",
        capabilities: { tools: {} },
        serverInfo: { name: SERVER_NAME, version: SERVER_VERSION },
      },
    });
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
      return redactRpcResponse({ jsonrpc: "2.0", id: message.id, result });
    } catch (error) {
      return redactRpcResponse({
        jsonrpc: "2.0",
        id: message.id,
        error: {
          code: -32000,
          message: error && error.message ? error.message : String(error),
          data: error && error.payload ? error.payload : undefined,
        },
      }, error && error.redactionSecrets);
    }
  }

  return redactRpcResponse({
    jsonrpc: "2.0",
    id: message.id,
    error: { code: -32601, message: `method not found: ${message.method}` },
  });
}

function redactRpcResponse(response, extraSecrets = newSecretSet()) {
  if (!response || typeof response !== "object") {
    return response;
  }
  const out = { ...response };
  if ("result" in response) {
    const secrets = collectSensitiveValues(response.result, "", newSecretSet());
    out.result = secrets.overflow
      ? redactResultOnOverflow(response.result)
      : redactSensitiveValue(response.result, "", secrets);
  }
  if (response.error && typeof response.error === "object") {
    const secrets = copySecretSet(extraSecrets);
    if (response.error.data !== undefined) {
      collectSensitiveValues(response.error.data, "", secrets);
    }
    out.error = {
      ...response.error,
      message: redactSensitiveString(response.error.message, secrets),
      data:
        response.error.data === undefined
          ? undefined
          : redactSensitiveValue(response.error.data, "", secrets),
    };
  }
  return out;
}

function redactResultOnOverflow(result) {
  const out = {};
  if (result && Array.isArray(result.content)) {
    out.content = result.content.map((entry) => {
      if (entry && typeof entry === "object" && entry.type === "text") {
        return { type: "text", text: REDACTED };
      }
      return { type: "text", text: REDACTED };
    });
  } else {
    out.content = [{ type: "text", text: REDACTED }];
  }
  if (result && typeof result.isError === "boolean") {
    out.isError = result.isError;
  }
  return out;
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
  buildConnectArgs,
  buildCreateArgs,
  buildListArgs,
  callTool,
  handleRpc,
  normalizedPayload,
  toolDefinitions,
  validateControlPlaneOverride,
};

if (require.main === module) {
  main().catch((error) => {
    process.stderr.write(`${error && error.stack ? error.stack : error}\n`);
    process.exit(1);
  });
}
