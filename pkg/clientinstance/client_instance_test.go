package clientinstance

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestIDIsStableForCurrentProcess(t *testing.T) {
	t.Parallel()

	first := ID()
	second := ID()
	if first == "" {
		t.Fatal("expected non-empty client instance ID")
	}
	if first != second {
		t.Fatalf("expected process-scoped ID to be stable: got %q then %q", first, second)
	}
	if len(first) != randomByteLength*2 {
		t.Fatalf("unexpected client instance ID length: got %d want %d", len(first), randomByteLength*2)
	}
}

func TestNewIDEncodesRandomBytes(t *testing.T) {
	t.Parallel()

	raw := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	id, err := newID(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("newID returned error: %v", err)
	}
	if id != "000102030405060708090a0b0c0d0e0f" {
		t.Fatalf("unexpected client instance ID: got %q", id)
	}
}

func TestNewIDReturnsEntropyError(t *testing.T) {
	t.Parallel()

	_, err := newID(strings.NewReader(""))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected entropy read error, got %v", err)
	}
}
