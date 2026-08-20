import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import {
  mcpTunnelClientProxyCommand,
  startHttpMCPTunnelProxy,
  startStdioMCPTunnelProxy,
  type Command,
  type MCPTunnelClientProxyProcess,
} from "../../wrappers/mcp-tunnel-client-proxy/typescript/mcp_tunnel_client_proxy.js";

export const EXAMPLE_TUNNEL_ID = "tunnel_22222222222222222222222222222222";

export interface ExampleMCPServer {
  readonly calls: readonly string[];
  readonly url: string;
  close(): Promise<void>;
}

export function buildHttpMCPTunnelProxyCommand(
  tunnelClientBinary: string,
  mcpServerUrl: string,
): string[] {
  return mcpTunnelClientProxyCommand({
    command: [tunnelClientBinary],
    mcpServerUrls: [mcpServerUrl],
    tunnelId: EXAMPLE_TUNNEL_ID,
  });
}

export function buildStdioMCPTunnelProxyCommand(
  tunnelClientBinary: string,
  mcpCommand: Command,
): string[] {
  return mcpTunnelClientProxyCommand({
    command: [tunnelClientBinary],
    mcpCommands: [mcpCommand],
    tunnelId: EXAMPLE_TUNNEL_ID,
  });
}

export async function withHttpMCPTunnelProxy<T>(
  tunnelClientBinaryCommand: Command,
  mcpServerUrl: string,
  run: (proxy: MCPTunnelClientProxyProcess) => Promise<T>,
): Promise<T> {
  const proxy = await startHttpMCPTunnelProxy(tunnelClientBinaryCommand, mcpServerUrl);
  try {
    return await run(proxy);
  } finally {
    await proxy.stop();
  }
}

export async function withStdioMCPTunnelProxy<T>(
  tunnelClientBinaryCommand: Command,
  mcpCommand: Command,
  run: (proxy: MCPTunnelClientProxyProcess) => Promise<T>,
): Promise<T> {
  const proxy = await startStdioMCPTunnelProxy(tunnelClientBinaryCommand, mcpCommand);
  try {
    return await run(proxy);
  } finally {
    await proxy.stop();
  }
}

export async function startExampleMCPServer(): Promise<ExampleMCPServer> {
  const calls: string[] = [];
  const sessionID = "mcp-tunnel-client-proxy-typescript-example-session";
  const server = createServer(async (request, response) => {
    if (request.method === "OPTIONS") {
      response.writeHead(204, {
        Allow: "GET, POST, OPTIONS",
      });
      response.end();
      return;
    }
    if (request.method !== "POST" || request.url !== "/mcp") {
      response.writeHead(404);
      response.end();
      return;
    }

    let body: JSONRPCRequest;
    try {
      body = await readJSON(request);
    } catch (error) {
      writeJSON(response, 400, {}, jsonRPCError(null, -32700, String(error)));
      return;
    }
    if (body.method === "initialize") {
      writeJSON(response, 200, {
        "Mcp-Session-Id": sessionID,
      }, {
        jsonrpc: "2.0",
        id: body.id,
        result: {
          protocolVersion: "2025-06-18",
          capabilities: { tools: {} },
          serverInfo: {
            name: "mcp-tunnel-client-proxy-typescript-example",
            version: "0.0.1",
          },
        },
      });
      return;
    }
    if (body.method === "notifications/initialized") {
      response.writeHead(202);
      response.end();
      return;
    }
    if (body.method === "tools/call") {
      const name = stringArg(body.params, "name");
      const toolName = stringArg(body.params?.arguments, "name");
      if (name !== "echo") {
        writeJSON(response, 200, {}, jsonRPCError(body.id, -32601, `unknown tool ${name}`));
        return;
      }
      calls.push(toolName);
      writeJSON(response, 200, {}, {
        jsonrpc: "2.0",
        id: body.id,
        result: {
          content: [{ type: "text", text: `hello ${toolName}` }],
          structuredContent: { greeting: `hello ${toolName}` },
        },
      });
      return;
    }
    writeJSON(response, 200, {}, jsonRPCError(body.id, -32601, `unknown method ${body.method}`));
  });

  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  if (address === null || typeof address === "string") {
    throw new Error(`unexpected server address: ${String(address)}`);
  }
  return {
    calls,
    url: `http://127.0.0.1:${address.port}/mcp`,
    close: () => closeServer(server),
  };
}

export async function callEchoToolThroughMCPTunnelProxy(
  tunnelClientBinaryCommand: Command,
  mcpServerUrl: string,
  name: string,
): Promise<Record<string, unknown>> {
  const proxy = await startHttpMCPTunnelProxy(tunnelClientBinaryCommand, mcpServerUrl);
  try {
    return await callEchoTool(proxy.info.mcp_url, name);
  } finally {
    await proxy.stop();
  }
}

export async function callEchoTool(
  mcpUrl: string,
  name: string,
): Promise<Record<string, unknown>> {
  const initialized = await postJSONRPC(mcpUrl, {
    jsonrpc: "2.0",
    id: "initialize-0",
    method: "initialize",
    params: {
      protocolVersion: "2025-06-18",
      capabilities: { sampling: {}, roots: { listChanged: true } },
      clientInfo: {
        name: "mcp-tunnel-client-proxy-typescript-example",
        version: "0.0.1",
      },
    },
  });
  const sessionID = initialized.headers.get("mcp-session-id");
  if (!sessionID) {
    throw new Error("initialize response missing Mcp-Session-Id");
  }
  await postJSONRPC(
    mcpUrl,
    {
      jsonrpc: "2.0",
      method: "notifications/initialized",
      params: {},
    },
    { "Mcp-Session-Id": sessionID },
  );
  const response = await postJSONRPC(
    mcpUrl,
    {
      jsonrpc: "2.0",
      id: "tool-1",
      method: "tools/call",
      params: { name: "echo", arguments: { name } },
    },
    { "Mcp-Session-Id": sessionID },
  );
  return response.body;
}

interface JSONRPCRequest {
  readonly id?: unknown;
  readonly method?: string;
  readonly params?: JSONRPCParams;
}

interface JSONRPCParams {
  readonly name?: unknown;
  readonly arguments?: JSONRPCToolArguments;
}

interface JSONRPCToolArguments {
  readonly name?: unknown;
}

async function readJSON(request: IncomingMessage): Promise<JSONRPCRequest> {
  const chunks: Buffer[] = [];
  for await (const chunk of request) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
  }
  const decoded = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  if (typeof decoded !== "object" || decoded === null) {
    throw new Error("JSON-RPC request must be an object");
  }
  return decoded;
}

function writeJSON(
  response: ServerResponse,
  statusCode: number,
  headers: Record<string, string>,
  body: Record<string, unknown>,
): void {
  response.writeHead(statusCode, {
    "Content-Type": "application/json",
    ...headers,
  });
  response.end(JSON.stringify(body));
}

function jsonRPCError(id: unknown, code: number, message: string): Record<string, unknown> {
  return {
    jsonrpc: "2.0",
    id,
    error: { code, message },
  };
}

function stringArg(
  value: JSONRPCParams | JSONRPCToolArguments | undefined,
  key: "name",
): string {
  const raw = value?.[key];
  if (typeof raw !== "string") {
    throw new Error(`missing string param ${key}`);
  }
  return raw;
}

async function postJSONRPC(
  mcpUrl: string,
  body: Record<string, unknown>,
  headers: Record<string, string> = {},
): Promise<{ body: Record<string, unknown>; headers: Headers }> {
  const response = await fetch(mcpUrl, {
    method: "POST",
    headers: {
      Accept: "application/json, text/event-stream",
      "Content-Type": "application/json",
      ...headers,
    },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw new Error(`JSON-RPC status ${response.status}: ${await response.text()}`);
  }
  const text = await response.text();
  return {
    body: text ? JSON.parse(text) : {},
    headers: response.headers,
  };
}

async function closeServer(server: Server): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error);
        return;
      }
      resolve();
    });
  });
}
