package healthstate

import "time"

// OAuthDetails describes startup discovery, not user authentication.
type OAuthDetails struct {
	DiscoveryComplete bool                    `json:"discovery_complete"`
	ActiveProbes      []OAuthProbeObservation `json:"active_probes,omitempty"`
	RecentProbes      []OAuthProbeObservation `json:"recent_probes,omitempty"`
}

func (OAuthDetails) healthDetails() {}

// OAuthProbeObservation describes one startup OAuth RoundTripper invocation.
// The bounded projection excludes request URLs, raw headers, bodies, and errors.
type OAuthProbeObservation struct {
	ProbeID           string     `json:"probe_id"`
	Origin            string     `json:"origin"`
	ProbeType         string     `json:"probe_type"`
	Method            string     `json:"method"`
	StartedAt         time.Time  `json:"started_at"`
	ResponseAt        *time.Time `json:"response_at,omitempty"`
	StatusCode        int        `json:"status_code,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	Disposition       string     `json:"disposition"`
	ResponseRequestID string     `json:"response_request_id,omitempty"`
}

// CloudflaredDetails contains no companion token or command material.
type CloudflaredDetails struct {
	Enabled bool `json:"enabled"`
	Ready   bool `json:"ready"`
}

func (CloudflaredDetails) healthDetails() {}

// ProxyDetails summarizes at most sixteen existing proxy-checker routes.
type ProxyDetails struct {
	RouteCount int                 `json:"route_count"`
	Routes     []ProxyRouteDetails `json:"routes"`
}
type ProxyRouteDetails struct {
	Label           string     `json:"label"`
	Kind            string     `json:"kind"`
	State           string     `json:"state"`
	LastCheck       *time.Time `json:"last_check,omitempty"`
	LastSuccess     *time.Time `json:"last_success,omitempty"`
	FailureCategory string     `json:"failure_category,omitempty"`
}

func (ProxyDetails) healthDetails() {}

// HarpoonDetails describes catalog population, not target reachability.
type HarpoonDetails struct {
	TargetCount  int    `json:"target_count"`
	CatalogState string `json:"catalog_state"`
}

func (HarpoonDetails) healthDetails() {}
