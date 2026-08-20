package utils

import (
	"testing"

	"github.com/google/uuid"
)

func TestDeriveStableUUID(t *testing.T) {
	id, err := DeriveStableUUID(
		func() string { return "cli-host" },
		func() string { return "cli-user" },
	)
	if err != nil {
		t.Fatalf("unexpected error deriving UUID: %v", err)
	}

	expected := uuid.NewSHA1(nsUUID, []byte("cli-host:cli-user"))
	if id != expected {
		t.Fatalf("expected UUID %s, got %s", expected, id)
	}
}

func TestDeriveStableUUIDHandlesErrors(t *testing.T) {
	if _, err := DeriveStableUUID(
		nil,
		func() string { return "host" },
	); err == nil {
		t.Fatalf("expected error when seed provider nil")
	}
	if _, err := DeriveStableUUID(
		func() string { return "   " },
		func() string { return "host" },
	); err == nil {
		t.Fatalf("expected error when seed empty")
	}
	if _, err := DeriveStableUUID(
		func() string { return "user" },
		nil,
	); err == nil {
		t.Fatalf("expected error when seed provider nil")
	}
}

func TestDeriveStableUUIDRequiresProviders(t *testing.T) {
	if _, err := DeriveStableUUID(); err == nil {
		t.Fatalf("expected error when no providers supplied")
	}
}

func TestDeriveStableUUIDWithOSProviders(t *testing.T) {
	id, err := DeriveStableUUID(OsHost, OsUser)
	if err != nil {
		t.Skipf("skipping OS provider assertion: %v", err)
	}
	if id == uuid.Nil {
		t.Fatalf("expected non-zero UUID from OS providers")
	}
}
