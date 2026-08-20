# Kubernetes sidecar deployment

Run `tunnel-client` as a sidecar container in the same Pod as your MCP server. This is a good fit when the MCP server is reachable via `localhost`.

## Example (snippet)

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: mcp-with-tunnel
spec:
  containers:
    - name: mcp-server
      image: your-mcp-image:latest
      ports:
        - containerPort: 3000
    - name: tunnel-client
      image: ghcr.io/openai/tunnel-client:v1.2.3
      env:
        - name: CONTROL_PLANE_TUNNEL_ID
          value: tunnel_0123456789abcdef0123456789abcdef
        - name: CONTROL_PLANE_BASE_URL
          value: https://api.openai.com
        - name: MCP_SERVER_URL
          value: http://127.0.0.1:3000/mcp
        - name: LOG_LEVEL
          value: info
        - name: LOG_FORMAT
          value: json
        - name: HEALTH_LISTEN_ADDR
          value: ":8080"
        - name: CONTROL_PLANE_API_KEY
          valueFrom:
            secretKeyRef:
              name: openai-api-key
              key: api_key
      ports:
        - name: health
          containerPort: 8080
      livenessProbe:
        httpGet:
          path: /healthz
          port: health
        initialDelaySeconds: 5
        periodSeconds: 10
      readinessProbe:
        httpGet:
          path: /readyz
          port: health
        initialDelaySeconds: 5
        periodSeconds: 10
```

## Considerations

- Replace the example `v1.2.3` tag with a released version or digest.
- Lock down egress with NetworkPolicy: allow `api.openai.com:443` plus access to the MCP port.
- The default health listener is `127.0.0.1:8080`. This example sets
  `HEALTH_LISTEN_ADDR=:8080` so kubelet probes can reach `/healthz` and `/readyz`
  on the Pod IP; keep the health port inside trusted cluster networking.
- If the MCP container can bind its listener after `tunnel-client` starts, set
  `MCP_STARTUP_WAIT_TIMEOUT` (for example, `60s`). The opt-in gate delays the
  first control-plane poll until the main MCP HTTP listener is reachable, so an
  already queued command is not consumed during the sidecar startup race. It
  does not retry or replay commands after polling begins.
