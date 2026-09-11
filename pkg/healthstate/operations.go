package healthstate

import "time"

// PollingDetails describes observations of the existing polling loop.
type PollingDetails struct {
	LastAttempt           *time.Time `json:"last_attempt,omitempty"`
	LastSuccess           *time.Time `json:"last_success,omitempty"`
	LastError             *time.Time `json:"last_error,omitempty"`
	ConsecutiveFailures   uint64     `json:"consecutive_failures"`
	CurrentPollAgeSeconds float64    `json:"current_poll_age_seconds"`
	ConfiguredWaitSeconds float64    `json:"configured_wait_seconds"`
	EffectiveWaitSeconds  float64    `json:"effective_wait_seconds"`
	DeadlineSeconds       float64    `json:"deadline_seconds"`
	NextRetry             *time.Time `json:"next_retry,omitempty"`
	FailureCategory       string     `json:"failure_category,omitempty"`
	HTTPStatus            int        `json:"http_status,omitempty"`
}

func (PollingDetails) healthDetails() {}

// DeliveryDetails separates an accepted upload from a benign terminal 404.
type DeliveryDetails struct {
	InProgress       uint64     `json:"in_progress"`
	LastAccepted     *time.Time `json:"last_accepted,omitempty"`
	LastCompleted    *time.Time `json:"last_completed,omitempty"`
	LastFailure      *time.Time `json:"last_failure,omitempty"`
	Disposition      string     `json:"disposition"`
	FailureCategory  string     `json:"failure_category,omitempty"`
	HTTPStatus       int        `json:"http_status,omitempty"`
	Attempts         uint64     `json:"attempts"`
	Retries          uint64     `json:"retries"`
	Accepted         uint64     `json:"accepted"`
	Completed        uint64     `json:"completed"`
	TerminalFailures uint64     `json:"terminal_failures"`
}

func (DeliveryDetails) healthDetails() {}

// QueueDetails describes the local command buffer, without exposing contents.
type QueueDetails struct {
	Depth               int        `json:"depth"`
	Capacity            int        `json:"capacity"`
	Utilization         float64    `json:"utilization"`
	LastEnqueue         *time.Time `json:"last_enqueue,omitempty"`
	LastDequeue         *time.Time `json:"last_dequeue,omitempty"`
	Enqueued            uint64     `json:"enqueued"`
	Dequeued            uint64     `json:"dequeued"`
	BackpressureSeconds float64    `json:"backpressure_seconds"`
}

func (QueueDetails) healthDetails() {}

// DispatcherDetails counts executing operations, independently of idle workers.
type DispatcherDetails struct {
	Active                 uint64     `json:"active"`
	PoolLimit              int        `json:"pool_limit"`
	LastStart              *time.Time `json:"last_start,omitempty"`
	LastCompletion         *time.Time `json:"last_completion,omitempty"`
	OldestActiveAgeSeconds float64    `json:"oldest_active_age_seconds"`
	Completed              uint64     `json:"completed"`
	Failures               uint64     `json:"failures"`
	Timeouts               uint64     `json:"timeouts"`
	Accepting              bool       `json:"accepting"`
}

func (DispatcherDetails) healthDetails() {}
