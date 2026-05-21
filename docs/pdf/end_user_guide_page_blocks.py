PAGE_BACKGROUNDS = {
    "cover": "images/generated/pdf-pages/page-cover-v1.webp",
    "interior": "images/generated/pdf-pages/page-interior-v1.webp",
    "screenshots": "images/generated/pdf-pages/page-screenshots-v1.webp",
    "code": "images/generated/pdf-pages/page-code-v1.webp",
}


PAGE_BLOCKS = [
    {
        "id": "cover",
        "background": "cover",
        "title": "Tunnel End-User Guide",
        "kicker": "OpenAI tunnel-client",
        "deck": "Connect a local or private MCP server to ChatGPT and Codex without exposing it to the public internet.",
        "pills": [
            "Permissions and groups",
            "Tunnel creation",
            "Local /ui checks",
            "ChatGPT connector",
            "Codex workflows",
        ],
        "paragraphs": [
            'This guide is the shortest complete operator path from "I have a private MCP server" to "ChatGPT and Codex can reach it through a tunnel." It keeps the control-plane values, local runtime steps, and product setup screens in one place so you do not need to stitch together four different docs or a Slack thread first.',
        ],
        "callout_pair": {
            "left": {
                "title": "What you need",
                "bullets": [
                    "A private or local MCP server that tunnel-client can reach.",
                    "A tunnel_id from OpenAI Platform Tunnels management.",
                    "A supported tunnel-client binary from Platform Tunnels management or the latest public release.",
                    "A runtime API key for the long-lived daemon.",
                    "An admin key only if you will create, list, update, or delete tunnels from the CLI.",
                ],
            },
            "right": {
                "title": "What usually blocks operators",
                "text": "tunnel_id is not the same thing as the runtime API key, and a tunnel showing up in Platform does not automatically mean it will appear in ChatGPT. This guide calls out those boundaries directly.",
            },
        },
    },
    {
        "id": "what-tunnel-client-does",
        "background": "interior",
        "title": "What tunnel-client does",
        "paragraphs": [
            "tunnel-client is the customer-run process that keeps an outbound-only HTTPS connection open to the OpenAI tunnel control plane.",
            "It receives work for one tunnel, forwards that work to your MCP server, and exposes local operator surfaces at /healthz, /readyz, /metrics, and /ui.",
            "If you want the shortest local discovery path first, run:",
        ],
        "code_blocks": [
            "tunnel-client help quickstart",
        ],
        "bullets": [
            "/healthz tells you the process is alive.",
            "/readyz tells you startup checks and downstream MCP checks have passed.",
            "/ui gives you the operator dashboard for the active runtime.",
        ],
    },
    {
        "id": "before-you-start-links",
        "background": "interior",
        "title": "Before you start",
        "paragraphs": [
            "Use these exact setup pages when you need to create or inspect values.",
        ],
        "link_list": [
            (
                "Tunnels management and supported tunnel-client download",
                "https://platform.openai.com/settings/organization/tunnels",
            ),
            (
                "Latest public tunnel-client release",
                "https://github.com/openai/tunnel-client/releases/latest",
            ),
            (
                "Organization roles",
                "https://platform.openai.com/settings/organization/people/roles",
            ),
            (
                "Organization groups",
                "https://platform.openai.com/settings/organization/people/groups",
            ),
            ("Runtime API keys", "https://platform.openai.com/settings/organization/api-keys"),
            ("Admin API keys", "https://platform.openai.com/settings/organization/admin-keys"),
            ("ChatGPT connector settings", "https://chatgpt.com/#settings/Connectors"),
        ],
    },
    {
        "id": "key-values-and-permissions",
        "background": "interior",
        "title": "Keep these three values straight",
        "key_cards": [
            {
                "title": "CONTROL_PLANE_TUNNEL_ID",
                "where": "Platform Tunnels management, or tunnel-client admin tunnels create|list|get ...",
                "what": "Identifies the tunnel object that ChatGPT and tunnel-client must both use.",
                "when": "Always.",
            },
            {
                "title": "CONTROL_PLANE_API_KEY",
                "where": "Platform Runtime API keys.",
                "what": "Authenticates tunnel-client doctor and tunnel-client run.",
                "when": "Always.",
            },
            {
                "title": "OPENAI_ADMIN_KEY",
                "where": "Platform Admin API keys.",
                "what": "Authenticates tunnel-client admin tunnels list|create|update|delete.",
                "when": "Only for tunnel CRUD.",
            },
        ],
        "paragraphs": [
            "The permission split is equally important.",
        ],
        "bullets": [
            "Runtime users need Tunnels Read + Use.",
            "Tunnel managers need Tunnels Read + Manage.",
            "People who create admin keys need the Platform admin-key permission in addition to any tunnel permissions they need.",
        ],
    },
    {
        "id": "roles-and-groups",
        "background": "screenshots",
        "title": "Roles and groups",
        "paragraphs": [
            "Use groups instead of editing people one by one, and make the runtime / manager split explicit.",
        ],
        "image_row": [
            {
                "path": "images/tunnel-permissions-role.png",
                "caption": "Platform > Organization roles > Tunnels permissions.",
            },
            {
                "path": "images/tunnel-permissions-group-role.png",
                "caption": "Platform > Organization groups > assign the tunnel role to the right operator group.",
            },
        ],
        "image_row_height": 400,
        "bullets": [
            "Create a runtime-user role with Tunnels Read + Use.",
            "Create a manager role with Tunnels Read + Manage, plus Use if the same people also run the daemon or configure the connector.",
            "Assign those roles to groups instead of editing people one by one.",
            "After the role assignment is in place, create new runtime or admin keys if practical, then rerun tunnel-client doctor --explain.",
        ],
    },
    {
        "id": "create-the-tunnel",
        "background": "screenshots",
        "title": "Create the tunnel",
        "paragraphs": [
            "The tunnel itself is the shared anchor between Platform, ChatGPT, and your local runtime.",
            "You can create it from the Platform Tunnels page or with the admin-key-backed CLI path:",
        ],
        "code_blocks": [
            'tunnel-client admin tunnels create \\\n  --name "Production MCP Tunnel" \\\n  --description "Routes ChatGPT connector traffic to the production MCP server" \\\n  --organization-id <ORG_ID> \\\n  --workspace-id <WORKSPACE_ID>',
        ],
        "image_stack": [
            {
                "path": "images/tunnel-create-modal.png",
                "caption": "Platform > Tunnels > Create tunnel modal.",
            },
        ],
        "image_stack_height": 500,
        "image_stack_width": 820,
        "bullets": [
            "The runtime daemon and the ChatGPT connector must use the same tunnel_id.",
            "If the tunnel should appear in a ChatGPT workspace picker, create it with the correct workspace scope.",
            "Use the Platform UI when you want the cleanest self-serve path. Use the admin CLI when you already have OPENAI_ADMIN_KEY and need repeatable create, list, or update operations.",
        ],
    },
    {
        "id": "first-success-terminal",
        "background": "code",
        "title": "Get to first success in the terminal",
        "paragraphs": [
            "Start with the binary explaining itself before you hand-edit configuration:",
        ],
        "code_blocks": [
            "tunnel-client help quickstart\ntunnel-client help doctor\ntunnel-client help plugin",
            'export CONTROL_PLANE_API_KEY="sk-..."\ntunnel-client run \\\n  --embedded-mcp-stub \\\n  --control-plane.tunnel-id tunnel_0123456789abcdef0123456789abcdef \\\n  --health.listen-addr 127.0.0.1:0 \\\n  --health.url-file /tmp/tunnel-client-health.url\ncurl -fsS "$(cat /tmp/tunnel-client-health.url)/readyz"\nopen "$(cat /tmp/tunnel-client-health.url)/ui"',
        ],
    },
    {
        "id": "profiles-and-readiness",
        "background": "code",
        "title": "Profiles and readiness",
        "paragraphs": [
            "If you want a named profile instead of the one-command demo path:",
        ],
        "code_blocks": [
            'tunnel-client init \\\n  --sample sample_mcp_stdio_local \\\n  --profile local-stdio \\\n  --tunnel-id tunnel_0123456789abcdef0123456789abcdef \\\n  --mcp-command "python /path/to/server.py"\ntunnel-client doctor --profile local-stdio --explain\ntunnel-client run --profile local-stdio',
        ],
        "bullets": [
            "/healthz returns HTTP 200 when the process is alive.",
            "/readyz returns HTTP 200 when the startup checks and downstream MCP readiness checks have passed.",
            "/ui gives you the local operator dashboard.",
            "If doctor --explain says the runtime key is missing, fix CONTROL_PLANE_API_KEY.",
            "If Platform knows the tunnel but ChatGPT does not, fix the workspace or connector permissions before you assume the daemon is wrong.",
        ],
    },
    {
        "id": "local-ui-overview",
        "background": "screenshots",
        "title": "Check the local UI",
        "paragraphs": [
            "The local UI is where you confirm the runtime is really alive, not just launched.",
            "These screenshots were captured from live local runs, with the Overview tab refreshed on May 21, 2026.",
        ],
        "image_row": [
            {
                "path": "screenshots/admin-overview.png",
                "caption": "Overview: health, readiness, tunnel, and MCP status in one place.",
            },
            {
                "path": "screenshots/admin-metrics.png",
                "caption": "Metrics: quick read of the exported Prometheus counters from /metrics.",
            },
        ],
        "image_row_height": 420,
        "bullets": [
            "Open /readyz first. If it is not ready, the connector will not be reliable yet.",
            "Open /ui#overview to confirm the active tunnel and MCP target.",
            "Open /ui#metrics when you want a quick read on request volume or readiness counters.",
        ],
    },
    {
        "id": "local-ui-logs-and-codex",
        "background": "screenshots",
        "title": "Logs, Codex, and support bundles",
        "image_row": [
            {
                "path": "screenshots/admin-logs.png",
                "caption": "Logs: live stream, filtering, and support-bundle export.",
            },
            {
                "path": "screenshots/admin-codex.png",
                "caption": "Assistant: Codex status, login state, and bridge activity from the same local runtime.",
            },
        ],
        "image_row_height": 340,
        "image_stack": [
            {
                "path": "screenshots/admin-log-export.webp",
                "caption": "Local Logs tab export confirmation. Use this when you need a redacted support bundle for debugging.",
            },
        ],
        "image_stack_height": 320,
        "image_stack_width": 860,
        "bullets": [
            "Open /ui#logs when you need the real error message instead of guessing from symptoms.",
            "Open /ui#codex when you are validating the Codex bridge, login state, or plugin setup.",
        ],
    },
    {
        "id": "connect-chatgpt",
        "background": "screenshots",
        "title": "Connect ChatGPT",
        "paragraphs": [
            "Once the local runtime is healthy, open https://chatgpt.com/#settings/Connectors and choose Connection: Tunnel.",
            "Then select the tunnel or paste the tunnel_id.",
        ],
        "image_stack": [
            {
                "path": "images/chatgpt-connector-tunnel-select.png",
                "caption": "ChatGPT > Settings > Connectors > Connection: Tunnel.",
            },
        ],
        "image_stack_height": 560,
        "image_stack_width": 760,
        "bullets": [
            "Leave tunnel-client run ... running while you do this.",
            "Confirm the tunnel was created with the correct workspace scope.",
            "Confirm the connector operator has Tunnels Read + Use.",
            "Confirm the daemon is still healthy and /readyz is passing.",
            "Confirm the tunnel is not so new that the control plane is still propagating it.",
        ],
    },
    {
        "id": "codex-commands",
        "background": "code",
        "title": "Use it from Codex",
        "paragraphs": [
            "You have two supported Codex paths: tunnel-client codex assistant ... for the shortest terminal assistant bridge, or tunnel-client codex plugin install plus the native tunnel-client runtimes ... and tunnel-client admin-profiles ... commands when you want the persistent local plugin surface.",
            "Useful commands:",
        ],
        "code_blocks": [
            'tunnel-client codex assistant "Summarize what tunnel-client is for."\ntunnel-client codex status\ntunnel-client codex plugin install\ntunnel-client runtimes list\ntunnel-client runtimes status <alias>\ntunnel-client admin-profiles list',
            "tunnel-client runtimes connect \\\n  --alias prod-mcp \\\n  --tunnel-id tunnel_0123456789abcdef0123456789abcdef \\\n  --runtime-api-key env:CONTROL_PLANE_API_KEY \\\n  --mcp-server-url https://mcp.example.com/mcp",
        ],
    },
    {
        "id": "starter-phrases",
        "background": "interior",
        "title": "Starter phrases for Codex",
        "paragraphs": [
            "Copy these exactly when you want Codex to take the first operator steps for you.",
        ],
        "bullets": [
            "Figure out what tunnel-client is for from the binary help, then get me to /ui with the shortest local path.",
            "I only have the source checkout. Figure out how to build tunnel-client, then get me to /ui with the shortest local path.",
            "Use tunnel-client to create or reuse a profile, run doctor --explain, and then start the daemon.",
            "Run tunnel-client codex assistant and summarize what this checkout is for in one sentence.",
            "Install the Codex plugin from the tunnel-client binary, connect the provided tunnel id, and tell me whether the runtime is launched, healthy, or ready.",
            "Use tunnel-client runtimes to attach a local MCP server to an existing tunnel id and report the ui_url.",
        ],
    },
    {
        "id": "faq-and-companion-docs",
        "background": "interior",
        "title": "FAQ",
        "faq_list": [
            {
                "q": "Where do I get CONTROL_PLANE_TUNNEL_ID?",
                "a": "From Platform Tunnels management, or from tunnel-client admin tunnels create|list|get ... if you already have OPENAI_ADMIN_KEY.",
            },
            {
                "q": "Where do I get CONTROL_PLANE_API_KEY?",
                "a": "From Platform Runtime API keys. This is the key that tunnel-client doctor and tunnel-client run expect.",
            },
            {
                "q": "When do I need OPENAI_ADMIN_KEY?",
                "a": "Only when you are creating, listing, updating, or deleting tunnels through the admin CLI. Do not swap the admin key in for the long-lived daemon.",
            },
            {
                "q": "Why can the tunnel exist in Platform but still not appear in ChatGPT?",
                "a": "Usually one of three reasons: the tunnel was created without the correct workspace scope, the connector operator does not have Tunnels Use, or the local daemon is not running and ready.",
            },
            {
                "q": "How do I tell whether the local runtime is healthy enough for ChatGPT or Codex?",
                "a": "Check /readyz first. Then open /ui#overview and /ui#logs. A launched process is not enough; the runtime needs to be ready.",
            },
            {
                "q": "Should I use Platform or the admin CLI to create the tunnel?",
                "a": "Use Platform when you want the clearest self-serve operator path. Use the admin CLI when you need a repeatable scriptable flow and you already have the admin key.",
            },
        ],
        "link_list": [
            ("permissions.md", "docs/permissions.md for the complete roles and groups walkthrough"),
            ("onboarding.md", "docs/onboarding.md for the broader CLI-first startup paths"),
            (
                "configuration.md",
                "docs/configuration.md for the full runtime, logs, metrics, and assistant surface",
            ),
            ("connectors.md", "docs/connectors.md for connector transport and auth behavior"),
            (
                "enterprise-customer-onboarding.md",
                "docs/enterprise-customer-onboarding.md for the customer-shareable architecture explanation",
            ),
        ],
    },
]
