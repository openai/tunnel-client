package hostbus

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestBusPublishDelivers(t *testing.T) {
	ch := make(chan URLBundle, 1)
	bus, err := New(ch)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	parsed := mustParseURL(t, "https://example.com/resource")
	bundle := URLBundle{
		FetchedAt: time.Unix(123, 0).UTC(),
		URLs: []URLRecord{{
			URL:         parsed,
			Description: "Example resource",
			Tags:        []Tag{{Key: TagKeySource, Value: "oauth"}},
		}},
	}

	if err := bus.Publish(context.Background(), bundle); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed")
		}
		if got.FetchedAt != bundle.FetchedAt {
			t.Fatalf("fetched_at mismatch: got %v want %v", got.FetchedAt, bundle.FetchedAt)
		}
		if len(got.URLs) != 1 {
			t.Fatalf("unexpected URL count: got %d", len(got.URLs))
		}
		if got.URLs[0].URL.String() != parsed.String() {
			t.Fatalf("url mismatch: got %q want %q", got.URLs[0].URL.String(), parsed.String())
		}
		if got.URLs[0].Description != "Example resource" {
			t.Fatalf("description mismatch: got %q", got.URLs[0].Description)
		}
		if len(got.URLs[0].Tags) != 1 || got.URLs[0].Tags[0].Value != "oauth" {
			t.Fatalf("tag mismatch: got %#v", got.URLs[0].Tags)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for bundle")
	}
}

func TestBusPublishRequiresSubscriber(t *testing.T) {
	bus, err := New(nil)
	if err == nil || bus != nil {
		t.Fatal("expected error for nil subscriber")
	}

	bus = &hostRegistrationBus{}
	if err := bus.Publish(context.Background(), URLBundle{}); err == nil {
		t.Fatal("expected error for missing subscriber")
	}
}

func TestBusPublishRespectsContext(t *testing.T) {
	bus, err := New(make(chan URLBundle))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err = bus.Publish(ctx, URLBundle{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestBusPublishAfterClose(t *testing.T) {
	bus, err := New(make(chan URLBundle))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := bus.Publish(context.Background(), URLBundle{}); err == nil {
		t.Fatal("expected error after close")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed
}
