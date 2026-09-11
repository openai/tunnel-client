package healthstate

import "time"

// MCPDetails reports observations of the configured main MCP channel. Discovery
// is historical evidence from forwarded traffic, not an active health probe.
type MCPDetails struct {
	Channel         string           `json:"channel"`
	Transport       string           `json:"transport"`
	ChildState      string           `json:"child_state,omitempty"`
	ChildGeneration string           `json:"child_generation,omitempty"`
	InitializeEpoch uint64           `json:"initialize_epoch"`
	Evidence        string           `json:"evidence"`
	Initialize      MCPInitialize    `json:"initialize"`
	ToolsList       MCPToolsList     `json:"tools_list"`
	StartupProbe    *MCPStartupProbe `json:"startup_probe,omitempty"`
}

func (MCPDetails) healthDetails() {}

// MCPInitialize contains only the bounded identity and recognized capabilities
// from a successful initialize response in this child generation and epoch.
type MCPInitialize struct {
	OK               bool       `json:"ok"`
	IdentityComplete bool       `json:"identity_complete"`
	Limited          bool       `json:"limited"`
	ObservedAt       *time.Time `json:"observed_at,omitempty"`
	ProtocolVersion  string     `json:"protocol_version,omitempty"`
	ServerName       string     `json:"server_name,omitempty"`
	ServerVersion    string     `json:"server_version,omitempty"`
	CapabilityNames  []string   `json:"capability_names"`
}

// MCPToolsList is a bounded projection of one observed catalog traversal.
// Complete is false if any page or name is missing; RetainedCount is not an
// estimate of the server's full catalog size.
type MCPToolsList struct {
	OK            bool       `json:"ok"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
	ToolNames     []string   `json:"tool_names"`
	RetainedCount int        `json:"retained_count"`
	Complete      bool       `json:"complete"`
	Partial       bool       `json:"partial"`
	Limited       bool       `json:"limited"`
}

// MCPStartupProbe is separate from same-child evidence. It never exposes the
// probe's raw error, response bodies, or authentication material.
type MCPStartupProbe struct {
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}
