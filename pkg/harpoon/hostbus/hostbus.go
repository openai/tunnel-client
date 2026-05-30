package hostbus

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"time"
)

// URLBundle captures a set of URLs discovered by any component. It keeps
// transport hints generic and intentionally avoids OAuth-specific fields.
type URLBundle struct {
	FetchedAt time.Time
	URLs      []URLRecord
}

// URLRecord describes a single URL plus optional metadata tags.
type URLRecord struct {
	URL            *url.URL
	Description    string
	Tags           []Tag
	UnixSocketPath string
}

// TagKey identifies a URL tag category.
type TagKey int

const (
	TagKeyUnspecified TagKey = iota
	TagKeySource
	TagKeyRole
	TagKeyIndex
	TagKeyGroup
)

// Tag associates a tag key with a value.
type Tag struct {
	Key   TagKey
	Value string
}

// HostRegistrationBus is the public interface for publishing URL bundles.
// Implementations are package-private to prevent external construction.
type HostRegistrationBus interface {
	Publish(ctx context.Context, bundle URLBundle) error
	Close() error
}

// hostRegistrationBus is a single-subscriber bus for URL bundles.
type hostRegistrationBus struct {
	subscriber chan URLBundle
	done       chan struct{}
	once       sync.Once
}

// New constructs a new host registration bus with the provided subscriber channel.
func New(subscriber chan URLBundle) (HostRegistrationBus, error) {
	if subscriber == nil {
		return nil, errors.New("hostbus: subscriber channel is required")
	}
	return &hostRegistrationBus{subscriber: subscriber, done: make(chan struct{})}, nil
}

// Publish delivers a bundle to the configured subscriber. It blocks until
// delivered or ctx is canceled.
func (b *hostRegistrationBus) Publish(ctx context.Context, bundle URLBundle) error {
	if b == nil || b.subscriber == nil {
		return errors.New("hostbus: subscriber channel is required")
	}
	select {
	case <-b.done:
		return errors.New("hostbus: closed")
	case <-ctx.Done():
		return ctx.Err()
	case b.subscriber <- bundle:
		return nil
	}
}

// Close signals publishers to stop waiting for delivery.
func (b *hostRegistrationBus) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		close(b.done)
	})
	return nil
}

var _ HostRegistrationBus = (*hostRegistrationBus)(nil)
