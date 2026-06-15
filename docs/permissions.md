# Tunnel Permissions, Roles, and Groups

Use this guide before creating tunnels, API keys, or ChatGPT connectors. The
common failure mode is mixing three separate things:

- **Tunnel metadata management**: creating, listing, editing, and deleting tunnel
  records in the Platform UI or `/v1/tunnels*`.
- **Tunnel runtime use**: allowing the long-lived `tunnel-client` daemon, or a
  ChatGPT connector, to use an existing tunnel.
- **Key creation**: creating the runtime API key for the daemon, or the admin API
  key used by `tunnel-client admin tunnels ...`.

## Setup URLs

- Tunnels management:
  `https://platform.openai.com/settings/organization/tunnels`
- Organization roles:
  `https://platform.openai.com/settings/organization/people/roles`
- Organization groups:
  `https://platform.openai.com/settings/organization/people/groups`
- Runtime API keys:
  `https://platform.openai.com/settings/organization/api-keys`
- Admin API keys:
  `https://platform.openai.com/settings/organization/admin-keys`
- ChatGPT connector settings:
  `https://chatgpt.com/#settings/Connectors`
- Public Admin API reference:
  `https://developers.openai.com/api/reference`

## Permission Names

The Platform roles UI labels tunnel permissions as:

| UI label | Permission atom | Use it for |
| --- | --- | --- |
| Read | `api.organization.tunnel.read` | Viewing tunnel records and metadata. |
| Manage | `api.organization.tunnel.write` | Creating, editing, and deleting tunnel records. |
| Use | `api.organization.tunnel.use` | Running or attaching to an existing tunnel. |

![Tunnels permission selector](images/tunnel-permissions-role.png)

The Platform roles/permissions surfaces were checked on 2026-04-24 and exposed
these three organization-level tunnel permission atoms. They also exposed a
predefined per-tunnel **User** role for reading and using one tunnel. In
practice, treat **Use** as the permission that must be present on the principal
whose runtime API key or ChatGPT connector will use the tunnel.

## Recommended Roles

Create roles around jobs to be done. Groups are optional but strongly preferred
so you can add or remove people without editing roles each time.

### Tunnel runtime users

Grant:

- Tunnels: **Read**
- Tunnels: **Use**

Assign this role to:

- The person or service-account owner that creates the runtime API key exported
  as `CONTROL_PLANE_API_KEY`.
- Operators who need to select an existing tunnel in ChatGPT connector settings.
- Codex users who run `tunnel-client runtimes connect --tunnel-id ...` without
  admin tunnel CRUD.

Do not give this group Admin keys permissions or Tunnels **Manage** unless they
also need to create/edit tunnel records.

### Tunnel managers

Grant:

- Tunnels: **Read**
- Tunnels: **Manage**
- Tunnels: **Use** if the same people also attach ChatGPT connectors or run the
  daemon.

Assign this role to:

- Platform admins who create, edit, or delete tunnels in the UI.
- Operators who run `tunnel-client admin tunnels create|list|update|delete`.

`tunnel-client admin tunnels list|create|update|delete` also requires an admin
API key through `OPENAI_ADMIN_KEY` or `--admin-key`. Keep that key separate from
the daemon runtime key.

### Admin key managers

Grant the minimum Platform permission needed to create or manage admin API keys
from `https://platform.openai.com/settings/organization/admin-keys`. In many
organizations this is limited to Owners; in organizations using custom roles,
grant the Admin keys Read/Manage permissions only to trusted platform admins.

If these people will also manage tunnels, assign the **Tunnel managers** role as
well. If they only create keys for someone else, do not grant Tunnels **Use**
unless they need to run or attach to a tunnel themselves.

## Group Workflow

1. Open Organization roles and create a role such as `tunnel-runtime-users` or
   `tunnel-managers`.
2. In **Permissions**, set the Tunnels row to the required Read/Manage/Use
   combination.
3. Open Organization groups and create a group for the role.
4. Use the group's **Roles** action to assign the tunnel role.
5. Add members to the group.
6. Have affected members create new runtime/admin keys after the role assignment
   is in place when practical, then rerun `tunnel-client doctor --explain`.

![Assign a tunnel role to a group](images/tunnel-permissions-group-role.png)

## Creating a Tunnel

Create tunnels from Tunnels management or with:

```bash
tunnel-client admin tunnels create \
  --name "Production MCP Tunnel" \
  --description "Routes ChatGPT connector traffic to the production MCP server" \
  --organization-id <ORG_ID> \
  --workspace-id <WORKSPACE_ID>
```

At least one organization or workspace scope is required by the CLI. Include the
ChatGPT workspace ID when the tunnel should appear in that workspace's connector
picker.

![Create tunnel modal](images/tunnel-create-modal.png)

After create succeeds, wait 25-30 seconds before expecting the new tunnel to be
active and ready.

## Creating Keys

Use two different keys:

- `CONTROL_PLANE_API_KEY`: runtime key used by `tunnel-client doctor` and
  `tunnel-client run`. In Platform Runtime API keys, create a **Restricted**
  key and select Tunnels **Read** + **Use**. Do not use **All** or an admin key
  for the long-lived daemon. The key's principal still needs Tunnels **Read** +
  **Use** for the target tunnel. It can also read one known tunnel through
  `tunnel-client admin tunnels get <tunnel_id>`.
- `OPENAI_ADMIN_KEY`: admin key used only for
  `tunnel-client admin tunnels list|create|update|delete`. Do not put this key
  in the long-lived daemon config.

Use secret references in profiles:

```yaml
control_plane:
  tunnel_id: tunnel_0123456789abcdef0123456789abcdef
  api_key: env:CONTROL_PLANE_API_KEY
```

For Codex plugin flows, store references, not literal keys:

```bash
tunnel-client admin-profiles set platform-admin \
  --admin-key env:OPENAI_ADMIN_KEY

tunnel-client runtimes connect \
  --alias prod-mcp \
  --tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --runtime-api-key env:CONTROL_PLANE_API_KEY \
  --mcp-server-url https://mcp.example.com/mcp
```

## ChatGPT Connector Setup

In ChatGPT connector settings, choose **Connection: Tunnel**, then select an
available tunnel or paste a tunnel ID.

![Select a tunnel in ChatGPT connector settings](images/chatgpt-connector-tunnel-select.png)

If the tunnel does not appear:

- Confirm the tunnel has the correct workspace ID attached.
- Confirm the connector admin has Tunnels **Read** + **Use**.
- Confirm `tunnel-client run ...` is healthy; connector discovery and tool calls
  require the daemon to stay running.
- Confirm the tunnel was created at least 25-30 seconds ago.

## Troubleshooting Permission Errors

- **403 while creating, updating, listing, or deleting tunnels**: the admin key
  path is missing Tunnels **Manage**, the wrong admin key is being used, or the
  command is scoped to the wrong organization/workspace.
- **403 while polling/running the daemon**: the runtime key principal likely
  lacks Tunnels **Use** for the tunnel.
- **Tunnel is visible in Platform but not in ChatGPT**: the tunnel may lack the
  workspace ID, or the ChatGPT connector admin may lack Tunnels **Use**.
- **`admin tunnels get <id>` works but `admin tunnels list` fails**: this is
  expected when you only have the runtime key. `get` can use the runtime key for
  read-only metadata; list/create/update/delete require `OPENAI_ADMIN_KEY`.

Keep least privilege as the default: most daemon runners need Read + Use, while
only trusted platform admins need Manage and admin-key access.
