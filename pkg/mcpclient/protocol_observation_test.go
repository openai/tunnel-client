package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

const observedInitializeFixture = `{"protocolVersion":"2025-11-25","serverInfo":{"name":"receipt-fixture","version":"1.2.3"},"capabilities":{"tools":{},"experimental":{"private-key":true}},"instructions":"private instructions"}`

const (
	observedServerDiscoverFixture = `{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{},"experimental":{"private-key":true}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"modern-fixture","version":"1.0.0"}},"instructions":"private instructions"}`
	observedModernProtocolVersion = "2026-07-28"
	observedModernParams          = `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`
)

func newObservationFixture(t *testing.T) (*ProtocolObservation, string) {
	t.Helper()
	o := NewProtocolObservation(&runtimeconfig.MCPConfig{TransportKind: runtimeconfig.MCPTransportStdio}, nil)
	o.now = func() time.Time { return time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC) }
	generation := o.beginChild()
	o.childStarted(generation)
	return o, generation
}

func observationRequest(t *testing.T, method, params string) *jsonrpc.Request {
	t.Helper()
	id, err := jsonrpc.MakeID("discovery")
	require.NoError(t, err)
	return &jsonrpc.Request{ID: id, Method: method, Params: json.RawMessage(params)}
}

func observationInitialize(t *testing.T, o *ProtocolObservation, generation string) {
	t.Helper()
	token := o.requestWritten(generation, observationRequest(t, "initialize", ""), "")
	o.response(token, &jsonrpc.Response{Result: json.RawMessage(observedInitializeFixture)})
	require.True(t, observationDetails(o).Initialize.OK)
}

func observationTools(t *testing.T, o *ProtocolObservation, generation, params, result string) {
	t.Helper()
	token := o.requestWritten(generation, observationRequest(t, "tools/list", params), "")
	o.response(token, &jsonrpc.Response{Result: json.RawMessage(result)})
}

func observationServerDiscover(t *testing.T, o *ProtocolObservation, generation string) {
	t.Helper()
	token := o.requestWritten(generation, observationRequest(t, "server/discover", observedModernParams), observedModernProtocolVersion)
	o.response(token, &jsonrpc.Response{Result: json.RawMessage(observedServerDiscoverFixture)})
	require.NotNil(t, observationDetails(o).ServerDiscover)
}

func observationModernTools(t *testing.T, o *ProtocolObservation, generation, params, result string) {
	t.Helper()
	token := o.requestWritten(generation, observationRequest(t, "tools/list", params), observedModernProtocolVersion)
	o.response(token, &jsonrpc.Response{Result: json.RawMessage(result)})
}

func observationDetails(o *ProtocolObservation) healthstate.MCPDetails {
	return o.Snapshot(time.Time{}).Details.(healthstate.MCPDetails)
}

func TestProtocolObservationLifecycle(t *testing.T) {
	t.Parallel()

	o, generation := newObservationFixture(t)
	require.Len(t, generation, 32)
	require.Equal(t, healthstate.StatusUnknown, o.Snapshot(time.Time{}).Status)
	require.Nil(t, o.Snapshot(time.Time{}).ObservedAt)
	observationTools(t, o, generation, "", `{"tools":[{"name":"too-early"}]}`)
	require.False(t, observationDetails(o).ToolsList.OK)
	observationInitialize(t, o, generation)
	details := observationDetails(o)
	require.Equal(t, uint64(1), details.InitializeEpoch)
	require.Zero(t, details.ServerDiscoverEpoch)
	require.Nil(t, details.ServerDiscover)
	require.Equal(t, "receipt-fixture", details.Initialize.ServerName)
	require.Equal(t, []string{"tools"}, details.Initialize.CapabilityNames)
	observationTools(t, o, generation, "", `{"tools":[{"name":"echo","description":"private description","inputSchema":{"secret":"private schema"}}]}`)
	require.Equal(t, "discovered", o.Snapshot(time.Time{}).State)
	require.True(t, observationDetails(o).ToolsList.Complete)
	encoded, err := json.Marshal(o.Snapshot(time.Time{}))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
	require.NotContains(t, string(encoded), "inputSchema")

	init := o.requestWritten(generation, observationRequest(t, "initialize", ""), "")
	require.Equal(t, uint64(2), observationDetails(o).InitializeEpoch)
	require.False(t, observationDetails(o).Initialize.OK)
	require.False(t, observationDetails(o).ToolsList.OK)
	o.childClosed(generation, "child_exited")
	o.response(init, &jsonrpc.Response{Result: json.RawMessage(observedInitializeFixture)})
	require.Equal(t, "closed", o.Snapshot(time.Time{}).State)
	require.False(t, observationDetails(o).Initialize.OK)
	o.childStarted(generation)
	require.Equal(t, "closed", observationDetails(o).ChildState, "a delayed started callback cannot resurrect an exited child")

	replacement := o.beginChild()
	o.childStarted(replacement)
	require.NotEqual(t, generation, replacement)
	o.childClosed(generation, "child_exited")
	o.response(init, &jsonrpc.Response{Result: json.RawMessage(observedInitializeFixture)})
	require.Equal(t, "running", observationDetails(o).ChildState)
	require.False(t, observationDetails(o).Initialize.OK)
	observationInitialize(t, o, replacement)
}

func TestProtocolObservationCatalogTraversal(t *testing.T) {
	t.Parallel()

	o, generation := newObservationFixture(t)
	observationInitialize(t, o, generation)
	observationTools(t, o, generation, "", `{"tools":[{"name":"z"},{"name":"a"},{"name":"a"}],"nextCursor":"private-cursor"}`)
	require.Equal(t, []string{"a", "z"}, observationDetails(o).ToolsList.ToolNames)
	require.True(t, observationDetails(o).ToolsList.Partial)
	require.False(t, observationDetails(o).ToolsList.Complete)
	observationTools(t, o, generation, `{"cursor":"private-cursor"}`, `{"tools":[{"name":"b"}]}`)
	require.Equal(t, []string{"a", "b", "z"}, observationDetails(o).ToolsList.ToolNames)
	require.True(t, observationDetails(o).ToolsList.Complete)
	encoded, err := json.Marshal(o.Snapshot(time.Time{}))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-cursor")

	observationTools(t, o, generation, "", `{"tools":[]}`)
	require.True(t, observationDetails(o).ToolsList.OK)
	require.True(t, observationDetails(o).ToolsList.Complete)
	require.Empty(t, observationDetails(o).ToolsList.ToolNames)

	observationTools(t, o, generation, `{"cursor":"standalone"}`, `{"tools":[{"name":"unattributed"}]}`)
	require.False(t, observationDetails(o).ToolsList.Complete)
	require.Empty(t, observationDetails(o).ToolsList.ToolNames)
	require.Equal(t, "tools_cursor_mismatch", o.Snapshot(time.Time{}).ReasonCode)

	observationTools(t, o, generation, "", `{"tools":[{"name":"first"}],"nextCursor":"one"}`)
	observationTools(t, o, generation, `{"cursor":"one"}`, `{"tools":[{"name":"second"}],"nextCursor":"two"}`)
	observationTools(t, o, generation, `{"cursor":"two"}`, `{"tools":[{"name":"third"}],"nextCursor":"one"}`)
	require.Equal(t, "tools_cursor_cycle", o.Snapshot(time.Time{}).ReasonCode)
	require.False(t, observationDetails(o).ToolsList.Complete)
}

func TestProtocolObservationServerDiscoverFailuresAndRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		response   *jsonrpc.Response
		reason     string
		limited    bool
		wantStatus healthstate.Status
		wantState  string
	}{
		{
			name: "error", response: &jsonrpc.Response{Error: errors.New("private discovery failure")},
			reason: "mcp_discovery_error", wantStatus: healthstate.StatusDegraded, wantState: "failed",
		},
		{
			name: "malformed", response: &jsonrpc.Response{Result: json.RawMessage(`{"supportedVersions":["2026-07-28"]}`)},
			reason: "invalid_server_discover_result", wantStatus: healthstate.StatusDegraded, wantState: "failed",
		},
		{
			name: "null_result_type", response: &jsonrpc.Response{Result: json.RawMessage(`{"resultType":null,"supportedVersions":["2026-07-28"],"capabilities":{}}`)},
			reason: "invalid_server_discover_result", wantStatus: healthstate.StatusDegraded, wantState: "failed",
		},
		{
			name: "oversized", response: &jsonrpc.Response{Result: append(
				[]byte(observedServerDiscoverFixture), bytes.Repeat([]byte{' '}, maxObservationResultBytes+1-len(observedServerDiscoverFixture))...,
			)},
			reason: "observation_result_limit", limited: true, wantStatus: healthstate.StatusUnknown, wantState: "not_observed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			o, generation := newObservationFixture(t)
			observationServerDiscover(t, o, generation)
			observationModernTools(t, o, generation, observedModernParams, `{"tools":[{"name":"old"}]}`)
			require.True(t, observationDetails(o).ToolsList.Complete)
			token := o.requestWritten(
				generation,
				observationRequest(t, "server/discover", observedModernParams),
				observedModernProtocolVersion,
			)
			require.Nil(t, observationDetails(o).ServerDiscover)
			require.False(t, observationDetails(o).ToolsList.OK)
			o.response(token, test.response)
			snapshot := o.Snapshot(time.Time{})
			details := observationDetails(o)
			require.Equal(t, test.wantStatus, snapshot.Status)
			require.Equal(t, test.wantState, snapshot.State)
			require.Equal(t, test.reason, snapshot.ReasonCode)
			require.Equal(t, test.limited, snapshot.Limited)
			require.Zero(t, details.InitializeEpoch)
			require.False(t, details.Initialize.OK)
			require.Equal(t, uint64(2), details.ServerDiscoverEpoch)
			require.NotNil(t, details.ServerDiscover)
			require.False(t, details.ServerDiscover.OK)
			require.NotContains(t, details.ToolsList.ToolNames, "old")
			encoded, err := json.Marshal(snapshot)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "private")

			observationServerDiscover(t, o, generation)
			require.Equal(t, healthstate.StatusOK, o.Snapshot(time.Time{}).Status)
			require.Equal(t, "server_discovered", o.Snapshot(time.Time{}).State)
			require.Equal(t, uint64(3), observationDetails(o).ServerDiscoverEpoch)
		})
	}
}

func TestProtocolObservationServerDiscoverNegotiatesVersion(t *testing.T) {
	t.Parallel()

	o, generation := newObservationFixture(t)
	futureVersion := "2027-01-01"
	futureParams := strings.Replace(observedModernParams, observedModernProtocolVersion, futureVersion, 1)
	token := o.requestWritten(generation, observationRequest(t, "server/discover", futureParams), futureVersion)
	o.response(token, &jsonrpc.Response{Result: json.RawMessage(observedServerDiscoverFixture)})
	snapshot := o.Snapshot(time.Time{})
	details := observationDetails(o)
	require.Equal(t, healthstate.StatusOK, snapshot.Status)
	require.Equal(t, "server_discovered", snapshot.State)
	require.False(t, details.Initialize.OK)
	require.NotNil(t, details.ServerDiscover)
	require.Equal(t, []string{observedModernProtocolVersion}, details.ServerDiscover.SupportedVersions)

	observationModernTools(t, o, generation, observedModernParams, `{"tools":[{"name":"negotiated"}]}`)
	require.Equal(t, "discovered", o.Snapshot(time.Time{}).State)
	require.Equal(t, []string{"negotiated"}, observationDetails(o).ToolsList.ToolNames)
}

func TestProtocolObservationServerDiscoverStaleResponses(t *testing.T) {
	t.Parallel()

	t.Run("newer_discover", func(t *testing.T) {
		t.Parallel()
		o, generation := newObservationFixture(t)
		old := o.requestWritten(generation, observationRequest(t, "server/discover", observedModernParams), observedModernProtocolVersion)
		current := o.requestWritten(generation, observationRequest(t, "server/discover", observedModernParams), observedModernProtocolVersion)
		o.response(current, &jsonrpc.Response{Result: json.RawMessage(observedServerDiscoverFixture)})
		o.response(old, &jsonrpc.Response{Error: errors.New("private stale failure")})
		details := observationDetails(o)
		require.Equal(t, uint64(2), details.ServerDiscoverEpoch)
		require.NotNil(t, details.ServerDiscover)
		require.True(t, details.ServerDiscover.OK)
		require.Equal(t, "server_discovered", o.Snapshot(time.Time{}).State)
	})

	t.Run("legacy_initialize", func(t *testing.T) {
		t.Parallel()
		o, generation := newObservationFixture(t)
		stale := o.requestWritten(generation, observationRequest(t, "server/discover", observedModernParams), observedModernProtocolVersion)
		observationInitialize(t, o, generation)
		o.response(stale, &jsonrpc.Response{Result: json.RawMessage(observedServerDiscoverFixture)})
		details := observationDetails(o)
		require.True(t, details.Initialize.OK)
		require.Nil(t, details.ServerDiscover)
		require.Equal(t, "initialized", o.Snapshot(time.Time{}).State)
	})

	t.Run("replacement_child", func(t *testing.T) {
		t.Parallel()
		o, generation := newObservationFixture(t)
		stale := o.requestWritten(generation, observationRequest(t, "server/discover", observedModernParams), observedModernProtocolVersion)
		replacement := o.beginChild()
		o.childStarted(replacement)
		o.response(stale, &jsonrpc.Response{Result: json.RawMessage(observedServerDiscoverFixture)})
		details := observationDetails(o)
		require.Equal(t, replacement, details.ChildGeneration)
		require.Zero(t, details.ServerDiscoverEpoch)
		require.Nil(t, details.ServerDiscover)
		require.Equal(t, "not_observed", o.Snapshot(time.Time{}).State)
	})

	t.Run("completed_evidence_replacement_child", func(t *testing.T) {
		t.Parallel()
		o, generation := newObservationFixture(t)
		observationServerDiscover(t, o, generation)
		observationModernTools(t, o, generation, observedModernParams, `{"tools":[{"name":"old"}]}`)
		require.Equal(t, "discovered", o.Snapshot(time.Time{}).State)

		replacement := o.beginChild()
		o.childStarted(replacement)
		details := observationDetails(o)
		require.Equal(t, replacement, details.ChildGeneration)
		require.Zero(t, details.ServerDiscoverEpoch)
		require.Nil(t, details.ServerDiscover)
		require.False(t, details.ToolsList.OK)
		require.Empty(t, details.ToolsList.ToolNames)
		require.Equal(t, "not_observed", o.Snapshot(time.Time{}).State)
	})
}

func TestProtocolObservationModernCatalogPaginationAndInvalidation(t *testing.T) {
	t.Parallel()

	o, generation := newObservationFixture(t)
	observationServerDiscover(t, o, generation)
	observationModernTools(t, o, generation, observedModernParams, `{"tools":[{"name":"a"}],"nextCursor":"private-cursor"}`)
	require.True(t, observationDetails(o).ToolsList.Partial)
	require.False(t, observationDetails(o).ToolsList.Complete)
	observationModernTools(t, o, generation, `{"cursor":"private-cursor","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`, `{"tools":[{"name":"b"}]}`)
	require.Equal(t, []string{"a", "b"}, observationDetails(o).ToolsList.ToolNames)
	require.True(t, observationDetails(o).ToolsList.Complete)

	stale := o.requestWritten(
		generation,
		observationRequest(t, "tools/list", observedModernParams),
		observedModernProtocolVersion,
	)
	o.listChanged(generation)
	o.response(stale, &jsonrpc.Response{Result: json.RawMessage(`{"tools":[{"name":"stale"}]}`)})
	snapshot := o.Snapshot(time.Time{})
	details := observationDetails(o)
	require.Equal(t, "server_discovered", snapshot.State)
	require.Equal(t, "tools_list_changed", snapshot.ReasonCode)
	require.True(t, details.ToolsList.Partial)
	require.NotContains(t, details.ToolsList.ToolNames, "stale")
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-cursor")

	observationModernTools(t, o, generation, observedModernParams, `{"tools":[{"name":"recovered"}]}`)
	require.Equal(t, []string{"recovered"}, observationDetails(o).ToolsList.ToolNames)
	require.Equal(t, "discovered", o.Snapshot(time.Time{}).State)

	futureVersion := "2027-01-01"
	futureParams := strings.Replace(observedModernParams, observedModernProtocolVersion, futureVersion, 1)
	future := o.requestWritten(generation, observationRequest(t, "tools/list", futureParams), futureVersion)
	require.True(t, future.acceptPage, "a bounded discovery receipt must not become an authority gate")
	require.False(t, observationDetails(o).ToolsList.OK)
	require.NotContains(t, observationDetails(o).ToolsList.ToolNames, "recovered")
	o.response(future, &jsonrpc.Response{Error: errors.New("private future-version error")})
	require.Equal(t, "failed", o.Snapshot(time.Time{}).State)
	require.Equal(t, "mcp_discovery_error", o.Snapshot(time.Time{}).ReasonCode)
	encoded, err = json.Marshal(o.Snapshot(time.Time{}))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")

	future = o.requestWritten(generation, observationRequest(t, "tools/list", futureParams), futureVersion)
	o.response(future, &jsonrpc.Response{Result: json.RawMessage(`{"tools":[{"name":"future"}]}`)})
	require.Equal(t, []string{"future"}, observationDetails(o).ToolsList.ToolNames)
	require.Equal(t, "discovered", o.Snapshot(time.Time{}).State)
}

func TestProtocolObservationStaleCatalogPublication(t *testing.T) {
	t.Parallel()

	for _, invalidate := range []string{"first_page", "list_changed", "bad_cursor", "reinitialize", "replacement_child"} {
		t.Run(invalidate, func(t *testing.T) {
			t.Parallel()
			o, generation := newObservationFixture(t)
			observationInitialize(t, o, generation)
			old := o.requestWritten(generation, observationRequest(t, "tools/list", ""), "")
			page, valid := parseObservedTools(json.RawMessage(`{"tools":[{"name":"stale"}]}`))
			require.True(t, valid)
			switch invalidate {
			case "first_page":
				observationTools(t, o, generation, "", `{"tools":[{"name":"current"}]}`)
			case "list_changed":
				o.listChanged(generation)
			case "bad_cursor":
				o.requestWritten(generation, observationRequest(t, "tools/list", `{"cursor":"unknown"}`), "")
			case "reinitialize":
				observationInitialize(t, o, generation)
			case "replacement_child":
				o.childStarted(o.beginChild())
			}
			o.publishTools(old, page)
			require.NotContains(t, observationDetails(o).ToolsList.ToolNames, "stale")
			if invalidate == "first_page" {
				require.Equal(t, []string{"current"}, observationDetails(o).ToolsList.ToolNames)
			}
		})
	}
}

func TestProtocolObservationFailuresAndRecovery(t *testing.T) {
	t.Parallel()

	for _, result := range []string{"null", `{}`, `{"protocolVersion":42}`, strings.Replace(observedInitializeFixture, `"tools":{}`, `"tools":true`, 1)} {
		t.Run(result, func(t *testing.T) {
			t.Parallel()
			o, generation := newObservationFixture(t)
			token := o.requestWritten(generation, observationRequest(t, "initialize", ""), "")
			o.response(token, &jsonrpc.Response{Result: json.RawMessage(result)})
			require.False(t, observationDetails(o).Initialize.OK)
			require.Equal(t, healthstate.StatusDegraded, o.Snapshot(time.Time{}).Status)
			observationInitialize(t, o, generation)
			require.Equal(t, healthstate.StatusOK, o.Snapshot(time.Time{}).Status)
		})
	}
	o, generation := newObservationFixture(t)
	observationInitialize(t, o, generation)
	for _, response := range []*jsonrpc.Response{
		{Error: errors.New("private failure detail")},
		{Result: json.RawMessage(`{"tools":null}`)},
		{Result: json.RawMessage(`{"tools":[{}]}`)},
		{Result: json.RawMessage(`{"tools":[{"name":1}]}`)},
	} {
		token := o.requestWritten(generation, observationRequest(t, "tools/list", ""), "")
		o.response(token, response)
		require.False(t, observationDetails(o).ToolsList.Complete)
		require.Equal(t, healthstate.StatusDegraded, o.Snapshot(time.Time{}).Status)
		require.NotContains(t, o.Snapshot(time.Time{}).ReasonCode, "private")
		observationTools(t, o, generation, "", `{"tools":[{"name":"recovered"}]}`)
		require.Equal(t, healthstate.StatusOK, o.Snapshot(time.Time{}).Status)
	}
}

func TestProtocolObservationIdentityBounds(t *testing.T) {
	t.Parallel()

	for _, size := range []int{maxObservationIdentity, maxObservationIdentity + 1} {
		name := strings.Repeat("a", size)
		raw := strings.Replace(observedInitializeFixture, "receipt-fixture", name, 1)
		result, valid := parseObservedInitialize(json.RawMessage(raw))
		require.True(t, valid)
		require.Equal(t, size <= maxObservationIdentity, result.IdentityComplete)
		require.Equal(t, size > maxObservationIdentity, result.Limited)
		if size <= maxObservationIdentity {
			require.Equal(t, name, result.ServerName)
		} else {
			require.Empty(t, result.ServerName, "oversized identities are omitted, never shortened")
		}
	}
	unicodeName := strings.Repeat("界", 85) + "a"
	result, valid := parseObservedInitialize(json.RawMessage(strings.Replace(observedInitializeFixture, "receipt-fixture", unicodeName, 1)))
	require.True(t, valid)
	require.Equal(t, unicodeName, result.ServerName)
	result, valid = parseObservedInitialize(json.RawMessage(strings.Replace(observedInitializeFixture, "receipt-fixture", unicodeName+"b", 1)))
	require.True(t, valid)
	require.Empty(t, result.ServerName)
	_, valid = parseObservedInitialize(bytes.ReplaceAll([]byte(observedInitializeFixture), []byte("receipt-fixture"), []byte{0xff}))
	require.False(t, valid)
	result, valid = parseObservedInitialize(json.RawMessage(strings.Replace(observedInitializeFixture, `"tools":{},`, "", 1)))
	require.True(t, valid)
	require.Empty(t, result.CapabilityNames)
}

func TestProtocolObservationRejectsLossyUnicode(t *testing.T) {
	t.Parallel()

	for _, name := range []string{`\ud800`, `\udc00`, `\ud800x`, `\ud800\u0061`} {
		_, valid := parseObservedInitialize(json.RawMessage(strings.Replace(observedInitializeFixture, "receipt-fixture", name, 1)))
		require.False(t, valid, name)
		_, valid = parseObservedTools(json.RawMessage(`{"tools":[{"name":"` + name + `"}]}`))
		require.False(t, valid, name)
		_, _, valid, _ = observationCursor(json.RawMessage(`{"cursor":"` + name + `"}`))
		require.False(t, valid, name)
	}
	for raw, expected := range map[string]string{`\ud83d\ude00`: "😀", `\ufffd`: "�", `\\ud800`: `\ud800`} {
		page, valid := parseObservedTools(json.RawMessage(`{"tools":[{"name":"` + raw + `"}]}`))
		require.True(t, valid, raw)
		require.Equal(t, []string{expected}, page.names)
	}
}

func TestProtocolObservationToolBoundsAndDeterminism(t *testing.T) {
	t.Parallel()

	for _, count := range []int{maxObservationToolNames, maxObservationToolNames + 1} {
		names := make([]string, count)
		for i := range names {
			names[i] = fmt.Sprintf("tool-%03d", i)
		}
		reversed := slices.Clone(names)
		slices.Reverse(reversed)
		forward, limited := boundedObservationNames(names)
		backward, otherLimited := boundedObservationNames(reversed)
		require.Equal(t, forward, backward)
		require.Equal(t, limited, otherLimited)
		require.Equal(t, count > maxObservationToolNames, limited)
		require.Len(t, forward, min(count, maxObservationToolNames))
	}
	for _, size := range []int{maxObservationToolBytes, maxObservationToolBytes + 1} {
		raw := `{"tools":[{"name":"` + strings.Repeat("a", size) + `"}]}`
		page, valid := parseObservedTools(json.RawMessage(raw))
		require.True(t, valid)
		require.Equal(t, size > maxObservationToolBytes, page.limited)
		require.Equal(t, size <= maxObservationToolBytes, len(page.names) == 1)
	}
	names := make([]string, 0, 126)
	for i := range 125 {
		names = append(names, fmt.Sprintf("a%03d", i)+strings.Repeat("x", 124))
	}
	names = append(names, "zzzzz")
	bounded, limited := boundedObservationNames(slices.Clone(names))
	require.False(t, limited)
	encoded, err := json.Marshal(bounded)
	require.NoError(t, err)
	require.Len(t, encoded, maxObservationNamesJSON)
	names[len(names)-1] += "z"
	bounded, limited = boundedObservationNames(names)
	require.True(t, limited)
	require.Len(t, bounded, 125)

	for i := range names {
		names[i] = fmt.Sprintf("%03d", i) + strings.Repeat("\x01", 125)
	}
	bounded, limited = boundedObservationNames(names)
	require.True(t, limited)
	encoded, err = json.Marshal(bounded)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), maxObservationNamesJSON)
}

func TestProtocolObservationInputAndPagingBounds(t *testing.T) {
	t.Parallel()

	for _, size := range []int{maxObservationResultBytes, maxObservationResultBytes + 1} {
		o, generation := newObservationFixture(t)
		token := o.requestWritten(generation, observationRequest(t, "initialize", ""), "")
		raw := append([]byte(observedInitializeFixture), bytes.Repeat([]byte{' '}, size-len(observedInitializeFixture))...)
		o.response(token, &jsonrpc.Response{Result: raw})
		require.Equal(t, size <= maxObservationResultBytes, observationDetails(o).Initialize.OK)
		require.Equal(t, size > maxObservationResultBytes, o.Snapshot(time.Time{}).Limited)
	}
	for _, size := range []int{maxObservationParamsBytes, maxObservationParamsBytes + 1} {
		params := append([]byte(`{}`), bytes.Repeat([]byte{' '}, size-2)...)
		_, _, valid, limited := observationCursor(params)
		require.Equal(t, size <= maxObservationParamsBytes, valid)
		require.Equal(t, size > maxObservationParamsBytes, limited)
	}
	for _, size := range []int{maxObservationCursorBytes, maxObservationCursorBytes + 1} {
		_, _, valid, limited := observationCursor(json.RawMessage(`{"cursor":"` + strings.Repeat("c", size) + `"}`))
		require.Equal(t, size <= maxObservationCursorBytes, valid)
		require.Equal(t, size > maxObservationCursorBytes, limited)
	}
	versions := make([]string, maxObservationVersions+1)
	for i := range versions {
		versions[i] = fmt.Sprintf("%04d-07-28", 2026+i)
	}
	discoveryResult, err := json.Marshal(map[string]any{
		"resultType":        "complete",
		"supportedVersions": versions,
		"capabilities":      map[string]any{"tools": map[string]any{}},
	})
	require.NoError(t, err)
	discovery, valid := parseObservedServerDiscover(discoveryResult, observedModernProtocolVersion)
	require.True(t, valid)
	require.True(t, discovery.Limited)
	require.Len(t, discovery.SupportedVersions, maxObservationVersions)
	require.LessOrEqual(t, cap(discovery.SupportedVersions), maxObservationVersions)
	require.Contains(t, discovery.SupportedVersions, observedModernProtocolVersion)
	omittedResultType := strings.Replace(observedServerDiscoverFixture, `"resultType":"complete",`, "", 1)
	_, valid = parseObservedServerDiscover(json.RawMessage(omittedResultType), observedModernProtocolVersion)
	require.True(t, valid)
	o, generation := newObservationFixture(t)
	observationInitialize(t, o, generation)
	for i := range maxObservationPages {
		params := ""
		if i > 0 {
			params = fmt.Sprintf(`{"cursor":"%d"}`, i)
		}
		observationTools(t, o, generation, params, fmt.Sprintf(`{"tools":[{"name":"tool-%02d"}],"nextCursor":"%d"}`, i, i+1))
	}
	require.Len(t, o.seenCursors, maxObservationPages)
	observationTools(t, o, generation, fmt.Sprintf(`{"cursor":"%d"}`, maxObservationPages), `{"tools":[{"name":"over-limit"}]}`)
	require.True(t, observationDetails(o).ToolsList.Limited)
	require.NotContains(t, observationDetails(o).ToolsList.ToolNames, "over-limit")
	require.False(t, observationDetails(o).ToolsList.Complete)
	require.Equal(t, "tools_page_limit", o.Snapshot(time.Time{}).ReasonCode)
}

func TestProtocolObservationNeverReusesOverflowedTokens(t *testing.T) {
	t.Parallel()

	o, generation := newObservationFixture(t)
	observationInitialize(t, o, generation)
	o.catalogRevision = math.MaxUint64
	observationTools(t, o, generation, "", `{"tools":[{"name":"unrecordable"}]}`)
	require.Equal(t, uint64(math.MaxUint64), o.catalogRevision)
	require.True(t, o.Snapshot(time.Time{}).Limited)
	require.Empty(t, observationDetails(o).ToolsList.ToolNames)
	observationInitialize(t, o, generation)
	observationTools(t, o, generation, "", `{"tools":[{"name":"still-unrecordable"}]}`)
	require.Empty(t, observationDetails(o).ToolsList.ToolNames)
	o.details.InitializeEpoch = math.MaxUint64
	o.requestWritten(generation, observationRequest(t, "initialize", ""), "")
	require.Equal(t, uint64(math.MaxUint64), observationDetails(o).InitializeEpoch)
	require.False(t, observationDetails(o).Initialize.OK)
	require.True(t, o.Snapshot(time.Time{}).Limited)

	modern, modernGeneration := newObservationFixture(t)
	modern.details.ServerDiscoverEpoch = math.MaxUint64
	modern.requestWritten(
		modernGeneration,
		observationRequest(t, "server/discover", observedModernParams),
		observedModernProtocolVersion,
	)
	modernDetails := observationDetails(modern)
	require.Equal(t, uint64(math.MaxUint64), modernDetails.ServerDiscoverEpoch)
	require.NotNil(t, modernDetails.ServerDiscover)
	require.False(t, modernDetails.ServerDiscover.OK)
	require.True(t, modernDetails.ServerDiscover.Limited)
	require.True(t, modern.Snapshot(time.Time{}).Limited)
	require.Equal(t, "server_discover_epoch_limit", modern.Snapshot(time.Time{}).ReasonCode)
}

func TestProtocolObservationConcurrentInvalidationAndSnapshot(t *testing.T) {
	t.Parallel()

	o, generation := newObservationFixture(t)
	observationInitialize(t, o, generation)
	token := o.requestWritten(generation, observationRequest(t, "tools/list", ""), "")
	page, valid := parseObservedTools(json.RawMessage(`{"tools":[{"name":"stale"}]}`))
	require.True(t, valid)
	parsed := make(chan struct{})
	resume := make(chan struct{})
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		close(parsed)
		<-resume
		o.publishTools(token, page)
	}()
	go func() {
		defer group.Done()
		<-parsed
		for range 100 {
			snapshot := observationDetails(o)
			if snapshot.Initialize.ObservedAt != nil {
				*snapshot.Initialize.ObservedAt = time.Time{}
			}
			if len(snapshot.Initialize.CapabilityNames) > 0 {
				snapshot.Initialize.CapabilityNames[0] = "mutated"
			}
		}
	}()
	<-parsed
	o.childClosed(generation, "child_exited")
	close(resume)
	group.Wait()
	require.Equal(t, "closed", o.Snapshot(time.Time{}).State)
	require.False(t, observationDetails(o).ToolsList.OK)
}

func TestProtocolObservationUnsupportedAndDisabled(t *testing.T) {
	t.Parallel()

	probe := NewProbeState()
	o := NewProtocolObservation(&runtimeconfig.MCPConfig{}, probe)
	require.Equal(t, "unsupported_transport", observationDetails(o).Evidence)
	require.Equal(t, "pending", observationDetails(o).StartupProbe.State)
	probe.Set(nil)
	require.Equal(t, "succeeded", observationDetails(o).StartupProbe.State)
	require.False(t, observationDetails(o).Initialize.OK)
	require.Equal(t, healthstate.StatusUnknown, o.Snapshot(time.Time{}).Status)
	o = NewProtocolObservation(&runtimeconfig.MCPConfig{AllowNoMain: true}, probe)
	require.Equal(t, healthstate.StatusDisabled, o.Snapshot(time.Time{}).Status)
}

func TestStdioProtocolObservationMatchesForwardedExchanges(t *testing.T) {
	t.Parallel()

	for _, initializedShim := range []bool{false, true} {
		t.Run(fmt.Sprint(initializedShim), func(t *testing.T) {
			t.Parallel()
			o, _ := newObservationFixture(t)
			base := newStubSerializedForwardingConnection()
			transport := NewStdioDeadlineRetiringForwardingTransport(&stubSerializedForwardingTransport{conn: base})
			if initializedShim {
				transport = NewStdioForwardingTransport(&stubSerializedForwardingTransport{conn: base})
			}
			transport = ObserveStdioForwardingTransport(transport, o)
			for _, method := range []string{"initialize", "tools/list"} {
				conn, err := transport.Connect(context.Background())
				require.NoError(t, err)
				req := observationRequest(t, method, "")
				_, err = conn.Write(context.Background(), nil, req)
				require.NoError(t, err)
				result := observedInitializeFixture
				if method == "tools/list" {
					result = `{"tools":[{"name":"echo"}]}`
				}
				response := &jsonrpc.Response{ID: req.ID, Result: json.RawMessage(result)}
				base.enqueueRead(response, nil)
				message, err := conn.Read(context.Background())
				require.NoError(t, err)
				require.Same(t, response, message)
			}
			require.True(t, observationDetails(o).ToolsList.Complete)
			methods := []string{"initialize", "tools/list"}
			if initializedShim {
				methods = []string{"initialize", initializedNotificationMethod, "tools/list"}
			}
			for range 10 {
				o.Snapshot(time.Time{})
			}
			require.Equal(t, methods, base.writtenMethods())
			conn, err := transport.Connect(context.Background())
			require.NoError(t, err)
			_, err = conn.Write(context.Background(), nil, observationRequest(t, "initialize", ""))
			require.NoError(t, err)
			require.False(t, observationDetails(o).Initialize.OK, "reset also applies to the default legacy wrapper")
			require.NoError(t, conn.Close())
			require.Equal(t, "closed", o.Snapshot(time.Time{}).State)
		})
	}
}

func TestStdioProtocolObservationModernDiscoveryWithoutInitialize(t *testing.T) {
	t.Parallel()

	o, _ := newObservationFixture(t)
	base := newStubSerializedForwardingConnection()
	transport := ObserveStdioForwardingTransport(
		NewStdioForwardingTransportWithOptions(
			&stubSerializedForwardingTransport{conn: base},
			StdioForwardingOptions{},
		),
		o,
	)
	roundTrip := func(method, result string) {
		t.Helper()
		conn, err := transport.Connect(t.Context())
		require.NoError(t, err)
		request := observationRequest(t, method, observedModernParams)
		_, err = conn.Write(t.Context(), nil, request)
		require.NoError(t, err)
		response := &jsonrpc.Response{ID: request.ID, Result: json.RawMessage(result)}
		base.enqueueRead(response, nil)
		message, err := conn.Read(t.Context())
		require.NoError(t, err)
		require.Same(t, response, message)
	}
	roundTrip("server/discover", observedServerDiscoverFixture)

	snapshot := o.Snapshot(time.Time{})
	details := observationDetails(o)
	require.Equal(t, healthstate.StatusOK, snapshot.Status)
	require.Equal(t, "server_discovered", snapshot.State)
	require.Zero(t, details.InitializeEpoch)
	require.False(t, details.Initialize.OK, "modern discovery must not fabricate legacy initialization")
	require.Equal(t, uint64(1), details.ServerDiscoverEpoch)
	require.NotNil(t, details.ServerDiscover)
	require.True(t, details.ServerDiscover.OK)
	require.Equal(t, []string{observedModernProtocolVersion}, details.ServerDiscover.SupportedVersions)
	require.Equal(t, []string{"tools"}, details.ServerDiscover.CapabilityNames)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
	details.ServerDiscover.SupportedVersions[0] = "mutated"
	details.ServerDiscover.CapabilityNames[0] = "mutated"
	stable := observationDetails(o)
	require.Equal(t, []string{observedModernProtocolVersion}, stable.ServerDiscover.SupportedVersions)
	require.Equal(t, []string{"tools"}, stable.ServerDiscover.CapabilityNames)

	roundTrip("tools/list", `{"tools":[{"name":"echo"}]}`)

	snapshot = o.Snapshot(time.Time{})
	details = observationDetails(o)
	require.Equal(t, healthstate.StatusOK, snapshot.Status)
	require.Equal(t, "discovered", snapshot.State)
	require.Zero(t, details.InitializeEpoch)
	require.False(t, details.Initialize.OK, "modern discovery must not fabricate legacy initialization")
	require.True(t, details.ToolsList.Complete)
	require.Equal(t, []string{"echo"}, details.ToolsList.ToolNames)

	conn, err := transport.Connect(t.Context())
	require.NoError(t, err)
	legacy := observationRequest(t, "tools/call", `{"name":"echo","arguments":{}}`)
	result, err := conn.Write(t.Context(), nil, legacy)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, result.StatusCode, "modern discovery must not open the legacy initialization gate")
	require.NotNil(t, result.PreservedError)
	require.Equal(t, []string{"server/discover", "tools/list"}, base.writtenMethods())
}

func TestStdioProtocolObservationServerDiscoverResponseDeadline(t *testing.T) {
	t.Parallel()

	o, _ := newObservationFixture(t)
	base := newStubSerializedForwardingConnection()
	transport := ObserveStdioForwardingTransport(
		NewStdioForwardingTransportWithOptions(&stubSerializedForwardingTransport{conn: base}, StdioForwardingOptions{}),
		o,
	)
	conn, err := transport.Connect(t.Context())
	require.NoError(t, err)
	request := observationRequest(t, "server/discover", observedModernParams)
	_, err = conn.Write(t.Context(), nil, request)
	require.NoError(t, err)
	serialized := conn.(*serializedForwardingConnection)
	serialized.stateMu.Lock()
	stale := serialized.observationToken
	serialized.stateMu.Unlock()
	require.True(t, serialized.RetireResponseDeadline())
	snapshot := o.Snapshot(time.Time{})
	require.Equal(t, healthstate.StatusDegraded, snapshot.Status)
	require.Equal(t, "failed", snapshot.State)
	require.Equal(t, "discovery_response_deadline", snapshot.ReasonCode)
	require.Equal(t, "running", observationDetails(o).ChildState)

	o.response(stale, &jsonrpc.Response{Result: json.RawMessage(observedServerDiscoverFixture)})
	require.Equal(t, "failed", o.Snapshot(time.Time{}).State, "a retired late response cannot restore discovery evidence")
	require.False(t, observationDetails(o).ServerDiscover.OK)
}

func TestStdioProtocolObservationInitializeCapabilityWhitespace(t *testing.T) {
	t.Parallel()

	for _, initializedShim := range []bool{false, true} {
		for name, whitespace := range map[string]string{
			"spaces": "   ", "tabs": "\t\t", "pretty_printed": " \t\r\n  ",
		} {
			t.Run(fmt.Sprintf("initialized_shim_%t/%s", initializedShim, name), func(t *testing.T) {
				t.Parallel()
				o, _ := newObservationFixture(t)
				base := newStubSerializedForwardingConnection()
				transport := NewStdioDeadlineRetiringForwardingTransport(&stubSerializedForwardingTransport{conn: base})
				if initializedShim {
					transport = NewStdioForwardingTransport(&stubSerializedForwardingTransport{conn: base})
				}
				conn, err := ObserveStdioForwardingTransport(transport, o).Connect(t.Context())
				require.NoError(t, err)
				request := observationRequest(t, "initialize", "")
				_, err = conn.Write(t.Context(), nil, request)
				require.NoError(t, err)
				result := json.RawMessage(`{"protocolVersion":"2025-11-25","serverInfo":{"name":"whitespace-fixture","version":"1.2.3"},"capabilities":{"tools":` + whitespace + `{` + whitespace + `"listChanged":true` + whitespace + `}` + whitespace + `,"prompts":` + whitespace + `{}` + whitespace + `}}`)
				original := bytes.Clone(result)
				response := &jsonrpc.Response{ID: request.ID, Result: result}
				base.enqueueRead(response, nil)
				message, err := conn.Read(t.Context())
				require.NoError(t, err)
				require.Same(t, response, message)
				require.Equal(t, original, []byte(response.Result), "observation must preserve forwarded result bytes")
				snapshot := o.Snapshot(time.Time{})
				require.Equal(t, healthstate.StatusOK, snapshot.Status)
				details := snapshot.Details.(healthstate.MCPDetails)
				require.True(t, details.Initialize.OK)
				require.True(t, details.Initialize.IdentityComplete)
				require.Equal(t, "whitespace-fixture", details.Initialize.ServerName)
				require.Equal(t, []string{"prompts", "tools"}, details.Initialize.CapabilityNames)
			})
		}
	}
}

func TestStdioProtocolObservationDropsRetiredAndMismatchedResponses(t *testing.T) {
	t.Parallel()

	for _, rawID := range []any{"reused", float64(123)} {
		t.Run(fmt.Sprint(rawID), func(t *testing.T) {
			t.Parallel()
			o, generation := newObservationFixture(t)
			observationInitialize(t, o, generation)
			base := newStubSerializedForwardingConnection()
			transport := ObserveStdioForwardingTransport(NewStdioDeadlineRetiringForwardingTransport(&stubSerializedForwardingTransport{conn: base}), o)
			old, err := transport.Connect(context.Background())
			require.NoError(t, err)
			id, err := jsonrpc.MakeID(rawID)
			require.NoError(t, err)
			request := &jsonrpc.Request{ID: id, Method: "tools/list"}
			_, err = old.Write(context.Background(), nil, request)
			require.NoError(t, err)
			require.True(t, old.(*serializedForwardingConnection).RetireResponseDeadline())
			require.True(t, observationDetails(o).Initialize.OK)
			require.NoError(t, old.Close())
			current, err := transport.Connect(context.Background())
			require.NoError(t, err)
			_, err = current.Write(context.Background(), nil, request)
			require.NoError(t, err)
			wireID := current.(*serializedForwardingConnection).expectedID
			require.NotEqual(t, id, wireID)
			base.enqueueRead(&jsonrpc.Response{ID: id, Result: json.RawMessage(`{"tools":[{"name":"stale"}]}`)}, nil)
			base.enqueueRead(&jsonrpc.Response{ID: wireID, Result: json.RawMessage(`{"tools":[{"name":"current"}]}`)}, nil)
			message, err := current.Read(context.Background())
			require.NoError(t, err)
			require.Equal(t, id, message.(*jsonrpc.Response).ID)
			require.Equal(t, []string{"current"}, observationDetails(o).ToolsList.ToolNames)

			next, err := transport.Connect(context.Background())
			require.NoError(t, err)
			_, err = next.Write(context.Background(), nil, observationRequest(t, "initialize", ""))
			require.NoError(t, err)
			base.enqueueRead(&jsonrpc.Response{ID: wireID, Result: json.RawMessage(observedInitializeFixture)}, nil)
			_, err = next.Read(context.Background())
			require.NoError(t, err)
			require.False(t, observationDetails(o).Initialize.OK)
		})
	}
}

type observationWriteBarrier struct {
	ForwardingConnection
	writeEntered chan struct{}
	writeReturn  chan struct{}
	readReturned chan struct{}
}

func (c *observationWriteBarrier) Write(ctx context.Context, headers http.Header, message jsonrpc.Message) (ForwardingWriteResult, error) {
	close(c.writeEntered)
	select {
	case <-c.writeReturn:
		return c.ForwardingConnection.Write(ctx, headers, message)
	case <-ctx.Done():
		return ForwardingWriteResult{}, ctx.Err()
	}
}

func (c *observationWriteBarrier) Read(ctx context.Context) (jsonrpc.Message, error) {
	message, err := c.ForwardingConnection.Read(ctx)
	close(c.readReturned)
	return message, err
}

func TestStdioProtocolObservationConcurrentReadBeforeWriteReturns(t *testing.T) {
	t.Parallel()

	o, _ := newObservationFixture(t)
	base := newStubSerializedForwardingConnection()
	barrier := &observationWriteBarrier{ForwardingConnection: base, writeEntered: make(chan struct{}), writeReturn: make(chan struct{}), readReturned: make(chan struct{})}
	transport := ObserveStdioForwardingTransport(NewStdioDeadlineRetiringForwardingTransport(&stubSerializedForwardingTransport{conn: barrier}), o)
	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	request := observationRequest(t, "initialize", "")
	base.enqueueRead(&jsonrpc.Response{ID: request.ID, Result: json.RawMessage(observedInitializeFixture)}, nil)
	writeDone, readDone := make(chan error, 1), make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_, err := conn.Write(ctx, nil, request)
		writeDone <- err
	}()
	waitForSerializedSignal(t, barrier.writeEntered, "physical write")
	go func() {
		_, err := conn.Read(ctx)
		readDone <- err
	}()
	waitForSerializedSignal(t, barrier.readReturned, "physical response before write return")
	require.False(t, observationDetails(o).Initialize.OK)
	close(barrier.writeReturn)
	require.NoError(t, waitForSerializedError(t, writeDone, "completed write"))
	require.NoError(t, waitForSerializedError(t, readDone, "completed read"))
	require.True(t, observationDetails(o).Initialize.OK)
	require.Equal(t, uint64(1), observationDetails(o).InitializeEpoch)
}
