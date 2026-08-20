package mcpclient

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stubTransport struct{}

func (stubTransport) Connect(context.Context) (mcp.Connection, error) { return nil, nil }

func TestNewForwardingTransportNilBaseReturnsNil(t *testing.T) {
	t.Helper()

	if got := NewForwardingTransport(nil); got != nil {
		t.Fatalf("expected nil transport wrapper for nil base, got %T", got)
	}
}

func TestNewForwardingTransportWrapsBase(t *testing.T) {
	t.Helper()

	if got := NewForwardingTransport(stubTransport{}); got == nil {
		t.Fatal("expected non-nil transport wrapper for non-nil base")
	}
}
