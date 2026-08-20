// TypeScript wrapper for tests that need tunnel-client-driven MCP tunnels.
//
// The local helper starts `tunnel-client dev proxy`, which includes a local
// in-memory control plane. The remote helper starts `tunnel-client run` against
// a caller-provided control plane and MCP server.

import { spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { once } from "node:events";
import { createInterface, type Interface } from "node:readline";

export type Command = string | readonly string[];

export interface MCPTunnelClientProxyInfo {
  tunnel_id: string;
  mcp_url: string;
  control_plane_base_url: string;
  control_plane_transport: string;
  backend: string;
  control_plane_unix_socket?: string;
  health_url?: string;
}

export interface MCPTunnelClientProxyOptions {
  command: Command;
  mcpServerUrls?: readonly string[];
  mcpCommands?: readonly Command[];
  tunnelId?: string;
  urlFile?: string;
  listen?: string;
  extraArgs?: readonly string[];
  env?: NodeJS.ProcessEnv;
  cwd?: string;
  startupTimeoutMs?: number;
}

export interface MCPTunnelClientRunOptions {
  command: Command;
  tunnelId: string;
  mcpServerUrls?: readonly string[];
  mcpCommands?: readonly Command[];
  controlPlaneBaseUrl?: string;
  controlPlaneApiKeyEnv?: string;
  healthUrlFile?: string;
  healthListenAddr?: string;
  extraArgs?: readonly string[];
  env?: NodeJS.ProcessEnv;
  cwd?: string;
  startupTimeoutMs?: number;
}

export type MCPTunnelClientProxyOverrides = Omit<
  MCPTunnelClientProxyOptions,
  "command" | "mcpServerUrls" | "mcpCommands"
>;

export class MCPTunnelClientProxyProcess {
  readonly child: ChildProcess;
  readonly info: MCPTunnelClientProxyInfo;

  constructor(child: ChildProcess, info: MCPTunnelClientProxyInfo) {
    this.child = child;
    this.info = info;
  }

  async stop(timeoutMs = 5000): Promise<void> {
    await stopProcess(this.child, timeoutMs);
  }
}

export class MCPTunnelClientProcess {
  readonly child: ChildProcess;
  readonly healthUrl?: string;
  private temporaryHealthDir?: string;

  constructor(child: ChildProcess, healthUrl?: string, temporaryHealthDir?: string) {
    this.child = child;
    this.healthUrl = healthUrl;
    this.temporaryHealthDir = temporaryHealthDir;
  }

  async stop(timeoutMs = 5000): Promise<void> {
    await stopProcess(this.child, timeoutMs);
    if (this.temporaryHealthDir) {
      await rm(this.temporaryHealthDir, { recursive: true, force: true });
      this.temporaryHealthDir = undefined;
    }
  }
}

export async function startLocalMCPTunnelProxy(
  options: MCPTunnelClientProxyOptions,
): Promise<MCPTunnelClientProxyProcess> {
  const args = mcpTunnelClientProxyCommand(options);
  const [command, ...commandArgs] = args;
  if (command === undefined) {
    throw new Error("tunnel-client command must not be empty");
  }
  const child = spawn(command, commandArgs, {
    cwd: options.cwd,
    env: { ...process.env, ...(options.env ?? {}) },
    stdio: ["ignore", "pipe", "inherit"],
  });
  try {
    const info = await readConnectionInfo(child, options.startupTimeoutMs ?? 10000);
    return new MCPTunnelClientProxyProcess(child, info);
  } catch (error) {
    child.kill("SIGTERM");
    throw error;
  }
}

export async function startMCPTunnelClient(
  options: MCPTunnelClientRunOptions,
): Promise<MCPTunnelClientProcess> {
  let temporaryHealthDir: string | undefined;
  let healthUrlFile = options.healthUrlFile;
  if (!healthUrlFile) {
    temporaryHealthDir = await mkdtemp(path.join(tmpdir(), "mcp-tunnel-client-"));
    healthUrlFile = path.join(temporaryHealthDir, "health.url");
  }
  const args = mcpTunnelClientRunCommand(options, healthUrlFile);
  const [command, ...commandArgs] = args;
  if (command === undefined) {
    throw new Error("tunnel-client command must not be empty");
  }
  const child = spawn(command, commandArgs, {
    cwd: options.cwd,
    env: { ...process.env, ...(options.env ?? {}) },
    stdio: ["ignore", "ignore", "inherit"],
  });
  try {
    const healthUrl = await waitForHealthUrl(
      child,
      healthUrlFile,
      options.startupTimeoutMs ?? 10000,
    );
    return new MCPTunnelClientProcess(child, healthUrl, temporaryHealthDir);
  } catch (error) {
    child.kill("SIGTERM");
    if (temporaryHealthDir) {
      await rm(temporaryHealthDir, { recursive: true, force: true });
    }
    throw error;
  }
}

export function startHttpMCPTunnelProxy(
  command: Command,
  mcpServerUrl: string,
  options: MCPTunnelClientProxyOverrides = {},
): Promise<MCPTunnelClientProxyProcess> {
  return startLocalMCPTunnelProxy({ ...options, command, mcpServerUrls: [mcpServerUrl] });
}

export function startStdioMCPTunnelProxy(
  command: Command,
  mcpCommand: Command,
  options: MCPTunnelClientProxyOverrides = {},
): Promise<MCPTunnelClientProxyProcess> {
  return startLocalMCPTunnelProxy({ ...options, command, mcpCommands: [mcpCommand] });
}

export function mcpTunnelClientProxyCommand(
  options: MCPTunnelClientProxyOptions,
): string[] {
  const args = [...commandPrefix(options.command), "dev", "proxy"];
  if (options.listen) {
    args.push("--listen", options.listen);
  }
  if (options.tunnelId) {
    args.push("--tunnel-id", options.tunnelId);
  }
  for (const mcpServerUrl of options.mcpServerUrls ?? []) {
    args.push("--mcp-server-url", mcpServerUrl);
  }
  for (const mcpCommand of options.mcpCommands ?? []) {
    args.push("--mcp-command", mcpCommandArg(mcpCommand));
  }
  if (options.urlFile) {
    args.push("--url-file", options.urlFile);
  }
  args.push(...(options.extraArgs ?? []));
  args.push("--print-json");
  return args;
}

export function mcpTunnelClientRunCommand(
  options: MCPTunnelClientRunOptions,
  healthUrlFile = options.healthUrlFile,
): string[] {
  const args = [...commandPrefix(options.command), "run"];
  if (options.controlPlaneBaseUrl) {
    args.push("--control-plane.base-url", options.controlPlaneBaseUrl);
  }
  args.push("--control-plane.tunnel-id", options.tunnelId);
  const apiKeyEnv = options.controlPlaneApiKeyEnv ?? "CONTROL_PLANE_API_KEY";
  if (apiKeyEnv) {
    args.push("--control-plane.api-key", `env:${apiKeyEnv}`);
  }
  for (const mcpServerUrl of options.mcpServerUrls ?? []) {
    args.push("--mcp-server-url", mcpServerUrl);
  }
  for (const mcpCommand of options.mcpCommands ?? []) {
    args.push("--mcp-command", mcpCommandArg(mcpCommand));
  }
  const healthListenAddr = options.healthListenAddr ?? "127.0.0.1:0";
  if (healthListenAddr) {
    args.push("--health.listen-addr", healthListenAddr);
  }
  if (healthUrlFile) {
    args.push("--health.url-file", healthUrlFile);
  }
  args.push(...(options.extraArgs ?? []));
  return args;
}

async function readConnectionInfo(
  child: ChildProcess,
  startupTimeoutMs: number,
): Promise<MCPTunnelClientProxyInfo> {
  if (!child.stdout) {
    throw new Error("tunnel-client stdout pipe was not created");
  }
  const lines = createInterface({ input: child.stdout });
  return await Promise.race([
    parseConnectionInfo(lines, startupTimeoutMs),
    once(child, "error").then(([error]) => {
      throw error instanceof Error ? error : new Error(String(error));
    }),
  ]);
}

async function parseConnectionInfo(
  lines: Interface,
  startupTimeoutMs: number,
): Promise<MCPTunnelClientProxyInfo> {
  let body = "";
  const timeout = setTimeout(() => {
    lines.close();
  }, startupTimeoutMs);
  timeout.unref();
  try {
    for await (const line of lines) {
      if (body === "" && !line.trimStart().startsWith("{")) {
        continue;
      }
      body += `${line}\n`;
      try {
        const decoded = JSON.parse(body);
        validateConnectionInfo(decoded);
        return decoded;
      } catch (error) {
        if (error instanceof SyntaxError) {
          continue;
        }
        throw error;
      }
    }
  } finally {
    clearTimeout(timeout);
  }
  throw new Error("tunnel-client exited before printing connection JSON");
}

function validateConnectionInfo(info: unknown): asserts info is MCPTunnelClientProxyInfo {
  if (!isRecord(info)) {
    throw new Error("tunnel-client proxy JSON must be an object");
  }
  for (const key of [
    "tunnel_id",
    "mcp_url",
    "control_plane_base_url",
    "control_plane_transport",
    "backend",
  ]) {
    if (typeof info[key] !== "string" || info[key] === "") {
      throw new Error(`tunnel-client proxy JSON missing ${key}`);
    }
  }
  for (const key of ["control_plane_unix_socket", "health_url"]) {
    if (info[key] !== undefined && typeof info[key] !== "string") {
      throw new Error(`tunnel-client proxy JSON field ${key} must be a string`);
    }
  }
}

async function waitForHealthUrl(
  child: ChildProcess,
  healthUrlFile: string,
  startupTimeoutMs: number,
): Promise<string> {
  const deadline = Date.now() + startupTimeoutMs;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new Error("tunnel-client exited before publishing health URL");
    }
    try {
      const healthUrl = (await readFile(healthUrlFile, "utf8")).trim();
      if (healthUrl && (await healthReady(healthUrl))) {
        return healthUrl;
      }
    } catch (error) {
      if (!isNotFound(error)) {
        throw error;
      }
    }
    await delay(50);
  }
  throw new Error("timed out waiting for tunnel-client health URL");
}

async function healthReady(baseUrl: string): Promise<boolean> {
  try {
    const response = await fetch(`${baseUrl.replace(/\/+$/, "")}/readyz`);
    await response.arrayBuffer();
    return response.ok;
  } catch {
    return false;
  }
}

async function stopProcess(child: ChildProcess, timeoutMs: number): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) {
    return;
  }
  child.kill("SIGTERM");
  const timeout = new Promise<"timeout">((resolve) => {
    const timer = setTimeout(() => resolve("timeout"), timeoutMs);
    timer.unref();
  });
  const exited = once(child, "exit").then(() => "exit" as const);
  if ((await Promise.race([timeout, exited])) === "timeout") {
    child.kill("SIGKILL");
    await once(child, "exit");
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isNotFound(error: unknown): boolean {
  return isRecord(error) && error.code === "ENOENT";
}

function commandPrefix(command: Command): string[] {
  if (typeof command === "string") {
    return [command];
  }
  if (!Array.isArray(command) || command.length === 0) {
    throw new Error("tunnel-client command must not be empty");
  }
  return command.map(String);
}

function mcpCommandArg(command: Command): string {
  if (typeof command === "string") {
    return command;
  }
  if (!Array.isArray(command) || command.length === 0) {
    throw new Error("MCP command must not be empty");
  }
  return command.map(shellQuote).join(" ");
}

function shellQuote(value: string): string {
  if (/^[A-Za-z0-9_/:=.,@%+-]+$/.test(value)) {
    return value;
  }
  return `'${value.replaceAll("'", "'\"'\"'")}'`;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
