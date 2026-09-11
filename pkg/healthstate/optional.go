package healthstate

import "time"

// OAuthDetails describes startup discovery, not user authentication.
type OAuthDetails struct {
	DiscoveryComplete bool `json:"discovery_complete"`
}

func (OAuthDetails) healthDetails() {}

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
