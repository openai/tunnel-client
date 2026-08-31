package hostbus

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPublishAndWaitPropagatesSubscriberAcknowledgement(t *testing.T) {
	t.Parallel()

	subscriber := make(chan URLBundle)
	bus, err := New(subscriber)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		result <- PublishAndWait(context.Background(), bus, URLBundle{})
	}()

	bundle := receiveBundle(t, subscriber)
	wantErr := errors.New("registration failed")
	bundle.Acknowledge(wantErr)

	if got := receiveResult(t, result); !errors.Is(got, wantErr) {
		t.Fatalf("publish and wait error = %v, want %v", got, wantErr)
	}
}

func TestPublishAndWaitRespectsContextAfterDelivery(t *testing.T) {
	t.Parallel()

	subscriber := make(chan URLBundle)
	bus, err := New(subscriber)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- PublishAndWait(ctx, bus, URLBundle{})
	}()

	bundle := receiveBundle(t, subscriber)
	cancel()

	if got := receiveResult(t, result); !errors.Is(got, context.Canceled) {
		t.Fatalf("publish and wait error = %v, want context canceled", got)
	}

	// A late acknowledgement must remain non-blocking after the publisher has
	// already stopped waiting.
	bundle.Acknowledge(nil)
}

func TestPublishAndWaitReturnsClosedAfterDelivery(t *testing.T) {
	t.Parallel()

	subscriber := make(chan URLBundle)
	bus, err := New(subscriber)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		result <- PublishAndWait(context.Background(), bus, URLBundle{})
	}()

	bundle := receiveBundle(t, subscriber)
	if err := bus.Close(); err != nil {
		t.Fatalf("close bus: %v", err)
	}

	if got := receiveResult(t, result); got == nil || got.Error() != "hostbus: closed" {
		t.Fatalf("publish and wait error = %v, want hostbus closed", got)
	}
	bundle.Acknowledge(nil)
}

func TestPublishAndWaitDoesNotChangeOrdinaryBusContract(t *testing.T) {
	t.Parallel()

	bus := legacyHostRegistrationBus{}
	if SupportsAcknowledgement(bus) {
		t.Fatal("legacy bus unexpectedly reports acknowledgement support")
	}
	if err := bus.Publish(context.Background(), URLBundle{}); err != nil {
		t.Fatalf("legacy publish: %v", err)
	}
	if err := PublishAndWait(context.Background(), bus, URLBundle{}); err == nil || err.Error() != "hostbus: acknowledgement support is required" {
		t.Fatalf("publish and wait error = %v, want acknowledgement support error", err)
	}
}

func TestSupportsAcknowledgementForRuntimeBus(t *testing.T) {
	t.Parallel()

	bus, err := New(make(chan URLBundle))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if !SupportsAcknowledgement(bus) {
		t.Fatal("runtime bus does not report acknowledgement support")
	}
}

func TestStartupCatalogStateCompletesOnce(t *testing.T) {
	t.Parallel()

	state := NewStartupCatalogState()
	wantErr := errors.New("startup registration failed")
	state.Complete(wantErr)
	state.Complete(nil)

	if got := state.Wait(context.Background()); !errors.Is(got, wantErr) {
		t.Fatalf("startup catalog wait error = %v, want %v", got, wantErr)
	}
}

func TestStartupCatalogStateWaitRespectsContext(t *testing.T) {
	t.Parallel()

	state := NewStartupCatalogState()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := state.Wait(ctx); !errors.Is(got, context.Canceled) {
		t.Fatalf("startup catalog wait error = %v, want context canceled", got)
	}

	state.Complete(nil)
	if got := state.Wait(context.Background()); got != nil {
		t.Fatalf("completed startup catalog wait error = %v, want nil", got)
	}
}

func receiveBundle(t *testing.T, subscriber <-chan URLBundle) URLBundle {
	t.Helper()
	select {
	case bundle := <-subscriber:
		return bundle
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bundle")
		return URLBundle{}
	}
}

func receiveResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for publish result")
		return nil
	}
}

type legacyHostRegistrationBus struct{}

func (legacyHostRegistrationBus) Publish(context.Context, URLBundle) error { return nil }
func (legacyHostRegistrationBus) Close() error                             { return nil }
