package admin

// Tunnel represents the management API tunnel metadata.
type Tunnel struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Creator         string   `json:"creator,omitempty"`
	TenantIDs       []string `json:"tenant_ids,omitempty"`
	WorkspaceIDs    []string `json:"workspace_ids,omitempty"`
	OrganizationIDs []string `json:"organization_ids,omitempty"`
}

// TunnelListResponse wraps list responses.
type TunnelListResponse struct {
	Tunnels []Tunnel `json:"tunnels"`
}

// TunnelCreateRequest is the payload for POST /v1/tunnels.
type TunnelCreateRequest struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	TenantIDs       []string `json:"tenant_ids,omitempty"`
	WorkspaceIDs    []string `json:"workspace_ids,omitempty"`
	OrganizationIDs []string `json:"organization_ids,omitempty"`
}

// TunnelUpdateRequest is the payload for POST /v1/tunnels/{id}`.
// Pointer fields distinguish between "omit" (nil) and "replace with empty" ([]).
type TunnelUpdateRequest struct {
	Name            *string   `json:"name,omitempty"`
	Description     *string   `json:"description,omitempty"`
	TenantIDs       *[]string `json:"tenant_ids,omitempty"`
	WorkspaceIDs    *[]string `json:"workspace_ids,omitempty"`
	OrganizationIDs *[]string `json:"organization_ids,omitempty"`
}
