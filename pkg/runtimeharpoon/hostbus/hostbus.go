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

	// ack is attached only by PublishAndWait. It stays private so URL bundles
	// remain transport-neutral for ordinary publishers and subscribers.
	ack *publicationAck
}

// Acknowledge reports that a subscriber has finished processing a bundle.
// It is a no-op for bundles delivered through Publish and only the first
// acknowledgement is observed for bundles delivered through PublishAndWait.
func (b URLBundle) Acknowledge(err error) {
	b.AcknowledgeAfter(err, nil)
}

// RequiresAcknowledgement reports whether this bundle came from the stricter
// startup publication path. It exposes no bundle contents and lets the single
// subscriber keep ordinary later publications outside the startup boundary.
func (b URLBundle) RequiresAcknowledgement() bool {
	return b.ack != nil
}

// AcknowledgeAfter runs beforeAck immediately before acknowledging a
// PublishAndWait bundle. The callback is skipped for ordinary Publish bundles,
// which lets subscribers attach startup-only finalization work without
// changing fire-and-forget publication behavior.
func (b URLBundle) AcknowledgeAfter(err error, beforeAck func() error) {
	if b.ack == nil {
		return
	}
	if err == nil && beforeAck != nil {
		err = beforeAck()
	}
	b.ack.complete(err)
}

// URLRecord describes a single URL plus optional metadata tags.
type URLRecord struct {
	URL            *url.URL
	Description    string
	Tags           []Tag
	UnixSocketPath string

	// DisallowPrivateHostRegistration prevents this record from being admitted
	// as a private-host target or seeding OAuth protected-resource host policy.
	// An exact trusted protected-resource origin may still admit the record.
	DisallowPrivateHostRegistration bool
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

// acknowledgingHostRegistrationBus is an additive capability used by startup
// discovery. Keeping it separate preserves the long-standing publisher
// contract for ordinary host-bus implementations.
type acknowledgingHostRegistrationBus interface {
	PublishAndWait(ctx context.Context, bundle URLBundle) error
}

// SupportsAcknowledgement reports whether bus can provide the stricter
// startup publication barrier. Callers that receive false must retain the
// ordinary Publish path rather than making this additive capability mandatory.
func SupportsAcknowledgement(bus HostRegistrationBus) bool {
	if bus == nil {
		return false
	}
	_, ok := bus.(acknowledgingHostRegistrationBus)
	return ok
}

// hostRegistrationBus is a single-subscriber bus for URL bundles.
type hostRegistrationBus struct {
	subscriber chan URLBundle
	done       chan struct{}
	once       sync.Once
}

type publicationAck struct {
	once   sync.Once
	result chan error
}

func newPublicationAck() *publicationAck {
	return &publicationAck{result: make(chan error, 1)}
}

func (a *publicationAck) complete(err error) {
	if a == nil {
		return
	}
	a.once.Do(func() {
		// Keep the channel buffered so a late subscriber acknowledgement never
		// blocks after the publisher's context has already ended.
		a.result <- err
	})
}

// StartupCatalogState is a one-shot barrier for startup catalog completion.
// It is intentionally separate from readiness state: callers may wait for a
// stricter startup milestone without changing existing health semantics.
type StartupCatalogState struct {
	done chan struct{}
	once sync.Once
	mu   sync.RWMutex
	err  error
}

// NewStartupCatalogState constructs an unset startup catalog barrier.
func NewStartupCatalogState() *StartupCatalogState {
	return &StartupCatalogState{done: make(chan struct{})}
}

// Complete settles the startup catalog barrier. The first result wins.
func (s *StartupCatalogState) Complete(err error) {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
	})
}

// Wait blocks until startup catalog completion or context cancellation.
func (s *StartupCatalogState) Wait(ctx context.Context) error {
	if s == nil {
		return errors.New("hostbus: startup catalog state is required")
	}
	select {
	case <-s.done:
		return s.result()
	default:
	}

	select {
	case <-s.done:
		return s.result()
	case <-ctx.Done():
		// Prefer a completion that raced with context cancellation so callers
		// observe the finalized catalog whenever it is already available.
		select {
		case <-s.done:
			return s.result()
		default:
			return ctx.Err()
		}
	}
}

func (s *StartupCatalogState) result() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

// New constructs a new host registration bus with the provided subscriber channel.
func New(subscriber chan URLBundle) (HostRegistrationBus, error) {
	if subscriber == nil {
		return nil, errors.New("hostbus: subscriber channel is required")
	}
	return &hostRegistrationBus{subscriber: subscriber, done: make(chan struct{})}, nil
}

// PublishAndWait delivers a bundle through an acknowledgement-aware bus and
// waits until its subscriber has completed processing. Ordinary
// HostRegistrationBus implementations remain valid; callers that need the
// stricter startup barrier receive a generic error when the capability is not
// available.
func PublishAndWait(ctx context.Context, bus HostRegistrationBus, bundle URLBundle) error {
	if bus == nil {
		return errors.New("hostbus: host registration bus is required")
	}
	acknowledging, ok := bus.(acknowledgingHostRegistrationBus)
	if !ok {
		return errors.New("hostbus: acknowledgement support is required")
	}
	return acknowledging.PublishAndWait(ctx, bundle)
}

// Publish delivers a bundle to the configured subscriber. It blocks until
// delivered or ctx is canceled.
func (b *hostRegistrationBus) Publish(ctx context.Context, bundle URLBundle) error {
	// Ordinary publication remains fire-and-forget after delivery and must not
	// accidentally carry an acknowledgement from a reused bundle value.
	bundle.ack = nil
	return b.publish(ctx, bundle)
}

// PublishAndWait delivers a bundle and waits until its subscriber acknowledges
// completion, the bus closes, or ctx is canceled.
func (b *hostRegistrationBus) PublishAndWait(ctx context.Context, bundle URLBundle) error {
	ack := newPublicationAck()
	bundle.ack = ack
	if err := b.publish(ctx, bundle); err != nil {
		return err
	}

	select {
	case err := <-ack.result:
		return err
	default:
	}
	select {
	case err := <-ack.result:
		return err
	case <-b.done:
		select {
		case err := <-ack.result:
			return err
		default:
			return errors.New("hostbus: closed")
		}
	case <-ctx.Done():
		select {
		case err := <-ack.result:
			return err
		default:
			return ctx.Err()
		}
	}
}

func (b *hostRegistrationBus) publish(ctx context.Context, bundle URLBundle) error {
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
var _ acknowledgingHostRegistrationBus = (*hostRegistrationBus)(nil)
