# Secure MCP Tunnel client docs

This folder contains customer-facing operator docs and contributor docs for
`tunnel-client`, the customer-run agent behind Secure MCP Tunnel.

## Find The Right Guide

| If you need to... | Read... |
| --- | --- |
| Connect a local or private MCP server to ChatGPT or Codex | [`onboarding.md`](onboarding.md) |
| Explain the outbound-only network model or trust boundary | [`architecture.md`](architecture.md) |
| Implement a tunnel client in another language | [`protocol.md`](protocol.md) and [`openapi.json`](openapi.json) |
| Provision tunnel roles, groups, IDs, or API keys | [`permissions.md`](permissions.md) |
| Choose Docker, Kubernetes, or VM deployment | [`deployment/overview.md`](deployment/overview.md) |
| Debug readiness, connector discovery, OAuth, or support bundles | [`troubleshooting.md`](troubleshooting.md) |

## Start Here

- **Public Secure MCP Tunnel guide**:
  [`developers.openai.com/api/docs/guides/secure-mcp-tunnels`](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels)
- **Shareable end-user guide**: [`end-user-guide.md`](end-user-guide.md)
- **Onboarding (recommended first read)**: [`onboarding.md`](onboarding.md)
- **Permissions, roles, and groups**: [`permissions.md`](permissions.md)
- **Architecture diagrams**: [`architecture.md`](architecture.md)
- **Connector behavior**: [`connectors.md`](connectors.md)
- **Wire protocol for client implementers**: [`protocol.md`](protocol.md)
- **OpenAPI contract**: [`openapi.json`](openapi.json)
- **Enterprise customer handoff**: [`enterprise-customer-onboarding.md`](enterprise-customer-onboarding.md)

## Operator docs

- **Configuration reference**: [`configuration.md`](configuration.md)
- **Deployments**: [`deployment/overview.md`](deployment/overview.md)
- **Troubleshooting**: [`troubleshooting.md`](troubleshooting.md)
- **Connector behavior and pitfalls**: [`connectors.md`](connectors.md)

## Maintainer / contributor docs

- **Guide theme and visual manifest**: [`pdf/`](pdf)
- **Guide HTML archive command**: `make end-user-guide-html`
- **Guide slide deck command**: `make end-user-guide-slides`
- **Guide screenshot refresh command**: `make end-user-guide-screenshots`
- **Development & testing**: [`development.md`](development.md)
- **Wire protocol and OpenAPI contract**: [`protocol.md`](protocol.md), [`openapi.json`](openapi.json)
- **Roadmap / design notes**: [`roadmap.md`](roadmap.md)
