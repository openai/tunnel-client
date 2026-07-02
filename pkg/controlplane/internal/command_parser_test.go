package internal

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	wiretypes "go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
)

func TestBuildBase_ValidatesRequiredFields(t *testing.T) {
	t.Parallel()

	polledAt := time.Now()

	// missing request_id
	_, _, err := buildBase(wiretypes.BaseRawPolledCommand{ShardToken: "sh"}, polledAt)
	if err == nil {
		t.Fatalf("expected error for missing request_id")
	}

	// missing shard_token
	_, _, err = buildBase(wiretypes.BaseRawPolledCommand{RequestID: "id"}, polledAt)
	if err == nil {
		t.Fatalf("expected error for missing shard_token")
	}
}

func TestBuildBase_ClonesHeadersAndExtractsSession(t *testing.T) {
	t.Parallel()

	hdr := http.Header{
		"Mcp-Session-Id": {"abc-123"},
		"X-Test":         {"v1"},
	}
	raw := wiretypes.BaseRawPolledCommand{
		RequestID:  "req-1",
		ShardToken: "sh-1",
		CreatedAt:  time.Unix(1732844889, 0).UTC(),
		Headers:    hdr,
	}
	base, headers, err := buildBase(raw, time.Unix(1732844890, 0).UTC())
	if err != nil {
		t.Fatalf("buildBase unexpected error: %v", err)
	}
	if headers == nil || headers.Get("X-Test") != "v1" {
		t.Fatalf("expected cloned headers with X-Test=v1, got %v", headers)
	}
	// mutate original and ensure clone not affected
	hdr.Set("X-Test", "mutated")
	if headers.Get("X-Test") != "v1" {
		t.Fatalf("headers not cloned; got %v", headers)
	}
	if id, ok := base.SessionID(); !ok || id != "abc-123" {
		t.Fatalf("expected session id abc-123, got %q ok=%v", id, ok)
	}
}

func TestConvertRawCommand_SuccessAndErrors(t *testing.T) {
	t.Parallel()

	// success path
	req := &jsonrpc.Request{Method: "initialize"}
	enc, _ := jsonrpc.EncodeMessage(req)
	raw := wiretypes.RawJSONRPCPolledCommand{
		BaseRawPolledCommand: wiretypes.BaseRawPolledCommand{
			RequestID:  "r1",
			ShardToken: "shard-1",
			CreatedAt:  time.Unix(1732844889, 0).UTC(),
		},
		JSONRPC: json.RawMessage(enc),
	}
	cmd, err := convertRawCommand(raw, time.Now())
	if err != nil {
		t.Fatalf("convertRawCommand unexpected error: %v", err)
	}
	if _, ok := cmd.Message().(*jsonrpc.Request); !ok {
		t.Fatalf("expected jsonrpc.Request message")
	}

	// missing payload
	raw.JSONRPC = nil
	if _, err := convertRawCommand(raw, time.Now()); err == nil {
		t.Fatalf("expected error for missing jsonrpc payload")
	}
}

func TestConvertRawOauthDiscoveryCommand_Success(t *testing.T) {
	t.Parallel()

	raw := wiretypes.RawOauthDiscoveryPolledCommand{
		BaseRawPolledCommand: wiretypes.BaseRawPolledCommand{
			RequestID:  "oauth-1",
			ShardToken: "sh-oauth",
			CreatedAt:  time.Unix(1732844889, 0).UTC(),
			Headers:    http.Header{"X": {"y"}},
		},
	}
	cmd, err := convertRawOauthDiscoveryCommand(raw, time.Unix(1732844890, 0).UTC())
	if err != nil {
		t.Fatalf("convertRawOauthDiscoveryCommand unexpected error: %v", err)
	}
	if cmd.RequestID().String() != "oauth-1" {
		t.Fatalf("unexpected request id: %s", cmd.RequestID())
	}
	if cmd.ShardToken() != "sh-oauth" {
		t.Fatalf("unexpected shard token: %s", cmd.ShardToken())
	}
	if cmd.Headers().Get("X") != "y" {
		t.Fatalf("unexpected headers: %v", cmd.Headers())
	}
}

func TestConvertRawSessionTerminationCommand_RequiresSessionHeader(t *testing.T) {
	t.Parallel()

	raw := wiretypes.RawSessionTerminationPolledCommand{
		BaseRawPolledCommand: wiretypes.BaseRawPolledCommand{
			RequestID:  "terminate-1",
			ShardToken: "sh-terminate",
			CreatedAt:  time.Unix(1732844889, 0).UTC(),
			Headers:    http.Header{"mcp-session-id": {"session-123"}},
		},
	}
	cmd, err := convertRawSessionTerminationCommand(raw, time.Unix(1732844890, 0).UTC())
	if err != nil {
		t.Fatalf("convertRawSessionTerminationCommand unexpected error: %v", err)
	}
	if !cmd.IsSessionTermination() {
		t.Fatal("expected session termination marker")
	}
	if sessionID, ok := cmd.SessionID(); !ok || sessionID != "session-123" {
		t.Fatalf("unexpected session id: %q ok=%v", sessionID, ok)
	}

	raw.Headers = nil
	if _, err := convertRawSessionTerminationCommand(raw, time.Now()); err == nil {
		t.Fatal("expected error for missing session header")
	}
}
