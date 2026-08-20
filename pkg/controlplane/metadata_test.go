package controlplane

import (
	"testing"
	"time"
)

func TestMetadataStateSetAndWait(t *testing.T) {
	t.Parallel()

	state := NewMetadataState()
	state.Set(&TunnelMetadata{ID: "tunnel_123", Name: "Demo", Description: "desc"}, nil)

	metadata, err, ok := state.Wait(10 * time.Millisecond)
	if !ok {
		t.Fatalf("expected metadata to be available")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metadata == nil {
		t.Fatalf("expected metadata")
		return
	}
	if metadata.Name != "Demo" || metadata.Description != "desc" || metadata.ID != "tunnel_123" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestMetadataStateWaitTimeout(t *testing.T) {
	t.Parallel()

	state := NewMetadataState()
	metadata, err, ok := state.Wait(10 * time.Millisecond)
	if ok {
		t.Fatalf("expected timeout without metadata")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metadata != nil {
		t.Fatalf("expected nil metadata")
	}
}
