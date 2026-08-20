package apierror

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const maxDetailLength = 1024

// Info captures the structured tunnel-service API error envelope used by both
// runtime control-plane calls and admin CRUD commands.
type Info struct {
	Code       string
	Type       string
	Message    string
	Body       string
	Mitigation string
}

// Parse extracts the standard {"error": {...}} envelope, falling back to
// top-level message/type/code or detail shapes used by adjacent API middleware.
func Parse(body []byte) Info {
	var info Info
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return info
	}

	var payload struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
		Detail  any    `json:"detail"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		info.Body = TruncateDetail(string(body))
		return info
	}

	if payload.Error != nil {
		info.Code = payload.Error.Code
		info.Type = payload.Error.Type
		info.Message = payload.Error.Message
		info.Mitigation = MitigationForCode(info.Code)
		return info
	}

	info.Code = payload.Code
	info.Type = payload.Type
	info.Message = payload.Message
	if info.Message == "" && payload.Detail != nil {
		info.Message = stringifyDetail(payload.Detail)
	}
	info.Mitigation = MitigationForCode(info.Code)
	return info
}

func Detail(info Info) string {
	parts := make([]string, 0, 3)
	if info.Code != "" {
		parts = append(parts, info.Code)
	}
	if info.Message != "" {
		parts = append(parts, info.Message)
	} else if info.Body != "" {
		parts = append(parts, info.Body)
	}
	detail := strings.Join(parts, ": ")
	if info.Mitigation != "" {
		if detail == "" {
			return "mitigation: " + info.Mitigation
		}
		return detail + " mitigation: " + info.Mitigation
	}
	return detail
}

func TruncateDetail(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxDetailLength {
		return value
	}
	return value[:maxDetailLength] + "..."
}

func stringifyDetail(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(data)
	}
}

func MitigationForCode(code string) string {
	switch code {
	case "invalid_tunnel_id_format":
		return "set --control-plane.tunnel-id, CONTROL_PLANE_TUNNEL_ID, or control_plane.tunnel_id to the exact tunnel_... ID from Platform tunnel settings"
	case "invalid_channel_format":
		return "use only lowercase letters, digits, hyphens, or underscores in MCP channel names"
	case "rate_limit_exceeded":
		return "reduce request concurrency or volume and let tunnel-client retry after backoff"
	case "request_body_too_large":
		return "send a smaller MCP request body and avoid large inline payloads"
	case "invalid_json_payload":
		return "fix the MCP caller or server to send a valid JSON object request body"
	case "missing_mcp_session_id":
		return "send DELETE session termination requests with the Mcp-Session-Id header from the active MCP session"
	case "rfc9728_www_authenticate_unsupported":
		return "use the well-known OAuth discovery endpoints instead of WWW-Authenticate probing"
	case "tunnel_missing_principals":
		return "use a runtime API key associated with the intended organization or workspace"
	case "tunnel_management_permission_required":
		return "use an admin key or account with Tunnels Read and Manage for the target org/workspace"
	case "tunnel_use_forbidden":
		return "use credentials for a principal that has access to this tunnel, or recreate/connect the runtime in the owning org/workspace"
	case "tunnel_active_organization_required":
		return "set --control-plane.organization-id, CONTROL_PLANE_ORGANIZATION_ID, or control_plane.organization_id to the tunnel's organization ID"
	case "tunnel_active_organization_context_required":
		return "select or pass the intended organization ID before listing principals or managing principal-validation overrides"
	case "tunnel_principal_validation_override_management_permission_required":
		return "use a credential authorized for tunnel principal-validation override management"
	case "tunnel_principal_validation_override_automatically_derivable":
		return "create or update the tunnel directly; no reviewed override is needed for that principal set"
	case "tunnel_principal_association_unverified":
		return "use an automatically verifiable org/workspace/tenant combination or request a reviewed association override"
	case "tunnel_principal_limit_exceeded":
		return "reduce each of tenant_ids, workspace_ids, and organization_ids to the documented maximum"
	case "tunnel_request_mismatch":
		return "verify the client uses the same control_plane.tunnel_id that received the command"
	case "pending_request_not_found":
		return "ignore isolated races; if repeated, ensure only healthy clients acknowledge requests for this tunnel"
	case "tunnel_queue_full":
		return "reduce MCP request concurrency or add enough healthy tunnel-client capacity to drain the tunnel"
	case "tunnel_client_not_seen":
		return "start or restart tunnel-client with the matching control_plane.tunnel_id and control_plane.api_key"
	case "tunnel_client_not_connected":
		return "start or restart tunnel-client, then check its /readyz endpoint and logs"
	case "oauth_shim_invalid_target_uri":
		return "use an absolute http(s) URI or harpoon://<label> that matches harpoon.targets[].label"
	case "oauth_shim_unshimmable_endpoint":
		return "configure supported OAuth metadata endpoints and grouped harpoon targets for auth-server metadata"
	case "oauth_shim_target_not_found":
		return "add or correct the matching harpoon.targets[].label entry and restart tunnel-client"
	case "oauth_shim_harpoon_call_failed":
		return "check the Harpoon target URL, upstream MCP server health, and tunnel-client logs"
	case "oauth_shim_upstream_timeout":
		return "ensure the Harpoon target and upstream MCP server respond before the tunnel timeout"
	default:
		return ""
	}
}
