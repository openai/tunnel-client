# Kubernetes dedicated Deployment/Pod

Run `tunnel-client` as its own Deployment when your MCP server is already reachable via a Service and you want independent upgrades/restarts.

## Example Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tunnel-client
spec:
  replicas: 5
  selector:
    matchLabels: { app: tunnel-client }
  template:
    metadata:
      labels: { app: tunnel-client }
    spec:
      containers:
        - name: tunnel-client
          image: ghcr.io/openai/tunnel-client:v1.2.3
          env:
            - name: CONTROL_PLANE_TUNNEL_ID
              value: tunnel_0123456789abcdef0123456789abcdef
            - name: MCP_SERVER_URL
              # This Service must be stateless, share MCP session state, or keep
              # MCP sessions sticky to the correct backend.
              value: http://mcp-server.default.svc.cluster.local:3000/mcp
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
            httpGet: { path: /healthz, port: health }
            initialDelaySeconds: 5
          readinessProbe:
            httpGet: { path: /readyz, port: health }
            initialDelaySeconds: 5
```

The default health listener is `127.0.0.1:8080`. This example sets
`HEALTH_LISTEN_ADDR=:8080` so kubelet probes can reach `/healthz` and `/readyz`
on the Pod IP; keep the health port inside trusted cluster networking.
Replace the example `v1.2.3` tag with a released version or digest.

## Replicas and active-active behavior

The example uses `replicas: 5` to show an active-active deployment. This is safe
only when the `MCP_SERVER_URL` target can handle MCP session traffic from any
`tunnel-client` replica. In this example,
`http://mcp-server.default.svc.cluster.local:3000/mcp` should resolve to
equivalent MCP backends that are stateless, provide shared session state, are a
single HTTP MCP host, or sit behind a sticky/session-aware Service or load
balancer.

Tunnel service keeps one shared queue per tunnel and does not assign messages to
a specific `tunnel-client` replica. Each queued message is delivered to
whichever replica polls it first. For `stdio`, `localhost`, or non-sticky
per-replica MCP servers, related session messages can land on different
clients/backends and fail. In those cases, keep one `tunnel-client` per tunnel
or use distinct tunnel IDs per replica.
