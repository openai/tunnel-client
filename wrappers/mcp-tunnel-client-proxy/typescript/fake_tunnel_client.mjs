import { writeFileSync } from "node:fs";

const argFile = process.env.MCP_TUNNEL_CLIENT_ARG_FILE;
if (!argFile) {
  throw new Error("MCP_TUNNEL_CLIENT_ARG_FILE is required");
}
writeFileSync(argFile, JSON.stringify(process.argv.slice(2)));

console.log(JSON.stringify({
  tunnel_id: "tunnel_typescript_example",
  mcp_url: "http://127.0.0.1:18081/v1/mcp/tunnel_typescript_example",
  control_plane_base_url: "http://tunnel-client-local-proxy",
  control_plane_transport: "unix",
  control_plane_unix_socket: "/tmp/local-proxy/control.sock",
  backend: "go-in-memory",
}, null, 2));

let running = true;
process.on("SIGTERM", () => {
  running = false;
});

while (running) {
  await new Promise((resolve) => setTimeout(resolve, 10));
}
