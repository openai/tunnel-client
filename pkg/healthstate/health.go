// Package healthstate defines bounded, passive runtime health snapshots.
// Component owners publish observations without performing work on health reads.
package healthstate

import "time"

const (
	SchemaVersion     = 1
	MaxComponents     = 16
	MaxComponentBytes = 24 * 1024
	MaxAggregateBytes = 64 * 1024
)

// Status describes observed component evidence, independently of readiness.
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusUnknown  Status = "unknown"
	StatusDisabled Status = "disabled"
)

// Details permits only the explicit, privacy-reviewed snapshot shapes in this
// package. Providers must not return arbitrary payloads or configuration maps.
type Details interface{ healthDetails() }

// Component reads already collected state. Snapshot must not perform I/O or
// wait for new evidence; its return value must not alias mutable provider state.
type Component interface {
	Name() string
	Snapshot(now time.Time) ComponentSnapshot
}

type ComponentSnapshot struct {
	Status     Status     `json:"status"`
	State      string     `json:"state"`
	ReasonCode string     `json:"reason_code,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	Limited    bool       `json:"limited"`
	Details    Details    `json:"details,omitempty"`
}

type Runtime struct {
	InstanceID    string    `json:"instance_id"`
	Version       string    `json:"version,omitempty"`
	Flavor        string    `json:"flavor,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds float64   `json:"uptime_seconds"`
	Lifecycle     string    `json:"lifecycle"`
	Limited       bool      `json:"limited,omitempty"`
}

type Snapshot struct {
	SchemaVersion int                          `json:"schema_version"`
	Live          bool                         `json:"live"`
	Ready         bool                         `json:"ready"`
	SnapshotAt    time.Time                    `json:"snapshot_at"`
	Runtime       Runtime                      `json:"runtime"`
	Components    map[string]ComponentSnapshot `json:"components,omitempty"`
	Truncated     bool                         `json:"truncated,omitempty"`
}

type ComponentResponse struct {
	SchemaVersion int       `json:"schema_version"`
	SnapshotAt    time.Time `json:"snapshot_at"`
	Component     string    `json:"component"`
	ComponentSnapshot
}
