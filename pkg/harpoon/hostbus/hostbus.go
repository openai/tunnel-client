package hostbus

import (
	"context"
	"errors"

	runtimehostbus "github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
)

// Full-client Harpoon keeps these aliases for source compatibility while the
// runtime-owned bus lives in runtimeharpoon.
type URLBundle = runtimehostbus.URLBundle
type URLRecord = runtimehostbus.URLRecord
type TagKey = runtimehostbus.TagKey
type Tag = runtimehostbus.Tag
type HostRegistrationBus = runtimehostbus.HostRegistrationBus
type StartupCatalogState = runtimehostbus.StartupCatalogState

const (
	TagKeyUnspecified = runtimehostbus.TagKeyUnspecified
	TagKeySource      = runtimehostbus.TagKeySource
	TagKeyRole        = runtimehostbus.TagKeyRole
	TagKeyIndex       = runtimehostbus.TagKeyIndex
	TagKeyGroup       = runtimehostbus.TagKeyGroup
)

// hostRegistrationBus is a thin full-client adapter around the runtime-owned
// implementation. Keeping the wrapper preserves package-private zero-value
// behavior relied on by existing full-client tests without duplicating the
// runtime bus implementation.
type hostRegistrationBus struct {
	inner HostRegistrationBus
}

// New constructs the full-client compatibility adapter.
func New(subscriber chan URLBundle) (HostRegistrationBus, error) {
	inner, err := runtimehostbus.New(subscriber)
	if err != nil {
		return nil, err
	}
	return &hostRegistrationBus{inner: inner}, nil
}

// NewStartupCatalogState constructs the shared one-shot startup catalog
// completion barrier.
func NewStartupCatalogState() *StartupCatalogState {
	return runtimehostbus.NewStartupCatalogState()
}

// PublishAndWait forwards the runtime-owned acknowledgement-aware startup
// publication helper while preserving HostRegistrationBus as the ordinary
// fire-and-forget publisher interface.
func PublishAndWait(ctx context.Context, bus HostRegistrationBus, bundle URLBundle) error {
	return runtimehostbus.PublishAndWait(ctx, bus, bundle)
}

// SupportsAcknowledgement reports whether bus supports the additive startup
// registration barrier.
func SupportsAcknowledgement(bus HostRegistrationBus) bool {
	return runtimehostbus.SupportsAcknowledgement(bus)
}

func (b *hostRegistrationBus) Publish(ctx context.Context, bundle URLBundle) error {
	if b == nil || b.inner == nil {
		return errors.New("hostbus: subscriber channel is required")
	}
	return b.inner.Publish(ctx, bundle)
}

func (b *hostRegistrationBus) PublishAndWait(ctx context.Context, bundle URLBundle) error {
	if b == nil || b.inner == nil {
		return errors.New("hostbus: subscriber channel is required")
	}
	return runtimehostbus.PublishAndWait(ctx, b.inner, bundle)
}

func (b *hostRegistrationBus) Close() error {
	if b == nil || b.inner == nil {
		return nil
	}
	return b.inner.Close()
}

var _ HostRegistrationBus = (*hostRegistrationBus)(nil)
