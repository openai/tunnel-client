package controlplane

import (
	"sync"
	"time"
)

// TunnelMetadata captures minimal tunnel metadata for operator visibility.
type TunnelMetadata struct {
	ID          string
	Name        string
	Description string
}

// MetadataState tracks the result of a background metadata fetch.
type MetadataState struct {
	done     chan struct{}
	mu       sync.Mutex
	metadata *TunnelMetadata
	err      error
	once     sync.Once
}

// NewMetadataState constructs a MetadataState ready for updates.
func NewMetadataState() *MetadataState {
	return &MetadataState{
		done: make(chan struct{}),
	}
}

// Set records the metadata result and signals waiters.
func (m *MetadataState) Set(metadata *TunnelMetadata, err error) {
	if m == nil {
		return
	}
	m.once.Do(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.metadata = metadata
		m.err = err
		close(m.done)
	})
}

// Wait blocks until metadata is available or the timeout elapses.
func (m *MetadataState) Wait(timeout time.Duration) (*TunnelMetadata, error, bool) {
	if m == nil {
		return nil, nil, false
	}

	select {
	case <-m.done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.metadata, m.err, true
	default:
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-m.done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.metadata, m.err, true
	case <-timer.C:
		select {
		case <-m.done:
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.metadata, m.err, true
		default:
			return nil, nil, false
		}
	}
}
