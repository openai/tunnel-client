# VM / systemd deployment

This is a simple pattern for running `tunnel-client` as a long-lived service on a VM.

## Example systemd unit

Create `/etc/systemd/system/tunnel-client.service`:

```ini
[Unit]
Description=OpenAI tunnel client
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/tunnel-client
ExecStart=/opt/tunnel-client/tunnel-client run --log.level=info --log.format=json
Restart=always
RestartSec=2

# Prefer an EnvironmentFile or a secrets manager; shown inline for clarity.
Environment=CONTROL_PLANE_TUNNEL_ID=tunnel_0123456789abcdef0123456789abcdef
Environment=MCP_SERVER_URL=https://mcp.internal.example.com/mcp
Environment=CONTROL_PLANE_API_KEY=sk-...
Environment=HEALTH_LISTEN_ADDR=127.0.0.1:8080

[Install]
WantedBy=multi-user.target
```

Then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tunnel-client
sudo systemctl status tunnel-client
```
