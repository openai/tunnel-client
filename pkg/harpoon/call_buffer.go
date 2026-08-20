package harpoon

import (
	"strings"
	"sync"
	"time"
)

const callBufferSize = 100

// CallEntry captures a recent harpoon call for the admin UI.
type CallEntry struct {
	Timestamp               time.Time `json:"timestamp"`
	Label                   string    `json:"label"`
	URL                     string    `json:"url"`
	Method                  string    `json:"method"`
	Status                  int       `json:"status"`
	LatencyMS               int       `json:"latency_ms"`
	ResponseContentType     string    `json:"response_content_type,omitempty"`
	ReqBytes                int       `json:"req_bytes"`
	RespBytes               int       `json:"resp_bytes"`
	Error                   string    `json:"error,omitempty"`
	RequestBody             string    `json:"request_body,omitempty"`
	ResponseBody            string    `json:"response_body,omitempty"`
	ResponseBodyTransformed string    `json:"response_body_transformed,omitempty"`
	BodyIsBase64            bool      `json:"body_is_base64,omitempty"`
}

// CallBuffer keeps a fixed-size ring buffer of recent calls.
type CallBuffer struct {
	mu      sync.Mutex
	entries []CallEntry
	next    int
	count   int
}

// NewCallBuffer constructs a ring buffer for recent calls.
func NewCallBuffer() *CallBuffer {
	return &CallBuffer{
		entries: make([]CallEntry, callBufferSize),
	}
}

// RecordCall records a call entry into the ring buffer.
func (b *CallBuffer) RecordCall(entry CallEntry) {
	if b == nil || len(b.entries) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.entries[b.next] = entry
	b.next = (b.next + 1) % len(b.entries)
	if b.count < len(b.entries) {
		b.count++
	}
}

// Snapshot returns up to limit entries, newest first, optionally filtered by label.
func (b *CallBuffer) Snapshot(limit int, label string) []CallEntry {
	if b == nil || len(b.entries) == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	max := b.count
	if max == 0 {
		return nil
	}
	if limit <= 0 || limit > len(b.entries) {
		limit = len(b.entries)
	}
	if limit > max {
		limit = max
	}
	label = strings.TrimSpace(label)

	out := make([]CallEntry, 0, limit)
	for i := 0; i < b.count && len(out) < limit; i++ {
		idx := b.next - 1 - i
		if idx < 0 {
			idx += len(b.entries)
		}
		entry := b.entries[idx]
		if label != "" && entry.Label != label {
			continue
		}
		out = append(out, entry)
	}
	return out
}
