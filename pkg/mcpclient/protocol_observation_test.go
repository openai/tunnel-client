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
	token := o.requestWritten(generation, observationRequest(t, "initialize", ""))
	o.response(token, &jsonrpc.Response{Result: json.RawMessage(observedInitializeFixture)})
	require.True(t, observationDetails(o).Initialize.OK)
}

func observationTools(t *testing.T, o *ProtocolObservation, generation, params, result string) {
	t.Helper()
	token := o.requestWritten(generation, observationRequest(t, "tools/list", params))
	o.response(token, &jsonrpc.Response{Result: json.RawMessage(result)})
}

func observationDetails(o *ProtocolObservation) healthstate.MCPDetails {
	return o.Snapshot(time.Time{}).Details.(healthstate.MCPDetails)
}

func TestProtocolObservationLifecycle(t *testing.T) {
	o, generation := newObservationFixture(t)
	require.Len(t, generation, 32)
	require.Equal(t, healthstate.StatusUnknown, o.Snapshot(time.Time{}).Status)
	require.Nil(t, o.Snapshot(time.Time{}).ObservedAt)
	observationTools(t, o, generation, "", `{"tools":[{"name":"too-early"}]}`)
	require.False(t, observationDetails(o).ToolsList.OK)
	observationInitialize(t, o, generation)
	details := observationDetails(o)
	require.Equal(t, uint64(1), details.InitializeEpoch)
	require.Equal(t, "receipt-fixture", details.Initialize.ServerName)
	require.Equal(t, []string{"tools"}, details.Initialize.CapabilityNames)
	observationTools(t, o, generation, "", `{"tools":[{"name":"echo","description":"private description","inputSchema":{"secret":"private schema"}}]}`)
	require.Equal(t, "discovered", o.Snapshot(time.Time{}).State)
	require.True(t, observationDetails(o).ToolsList.Complete)
	encoded, err := json.Marshal(o.Snapshot(time.Time{}))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private")
	require.NotContains(t, string(encoded), "inputSchema")

	init := o.requestWritten(generation, observationRequest(t, "initialize", ""))
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

func TestProtocolObservationStaleCatalogPublication(t *testing.T) {
	for _, invalidate := range []string{"first_page", "list_changed", "bad_cursor", "reinitialize", "replacement_child"} {
		t.Run(invalidate, func(t *testing.T) {
			o, generation := newObservationFixture(t)
			observationInitialize(t, o, generation)
			old := o.requestWritten(generation, observationRequest(t, "tools/list", ""))
			page, valid := parseObservedTools(json.RawMessage(`{"tools":[{"name":"stale"}]}`))
			require.True(t, valid)
			switch invalidate {
			case "first_page":
				observationTools(t, o, generation, "", `{"tools":[{"name":"current"}]}`)
			case "list_changed":
				o.listChanged(generation)
			case "bad_cursor":
				o.requestWritten(generation, observationRequest(t, "tools/list", `{"cursor":"unknown"}`))
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
	for _, result := range []string{"null", `{}`, `{"protocolVersion":42}`, strings.Replace(observedInitializeFixture, `"tools":{}`, `"tools":true`, 1)} {
		t.Run(result, func(t *testing.T) {
			o, generation := newObservationFixture(t)
			token := o.requestWritten(generation, observationRequest(t, "initialize", ""))
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
		token := o.requestWritten(generation, observationRequest(t, "tools/list", ""))
		o.response(token, response)
		require.False(t, observationDetails(o).ToolsList.Complete)
		require.Equal(t, healthstate.StatusDegraded, o.Snapshot(time.Time{}).Status)
		require.NotContains(t, o.Snapshot(time.Time{}).ReasonCode, "private")
		observationTools(t, o, generation, "", `{"tools":[{"name":"recovered"}]}`)
		require.Equal(t, healthstate.StatusOK, o.Snapshot(time.Time{}).Status)
	}
}

func TestProtocolObservationIdentityBounds(t *testing.T) {
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
	for _, size := range []int{maxObservationResultBytes, maxObservationResultBytes + 1} {
		o, generation := newObservationFixture(t)
		token := o.requestWritten(generation, observationRequest(t, "initialize", ""))
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
	o.requestWritten(generation, observationRequest(t, "initialize", ""))
	require.Equal(t, uint64(math.MaxUint64), observationDetails(o).InitializeEpoch)
	require.False(t, observationDetails(o).Initialize.OK)
	require.True(t, o.Snapshot(time.Time{}).Limited)
}

func TestProtocolObservationConcurrentInvalidationAndSnapshot(t *testing.T) {
	o, generation := newObservationFixture(t)
	observationInitialize(t, o, generation)
	token := o.requestWritten(generation, observationRequest(t, "tools/list", ""))
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
	for _, initializedShim := range []bool{false, true} {
		t.Run(fmt.Sprint(initializedShim), func(t *testing.T) {
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

func TestStdioProtocolObservationInitializeCapabilityWhitespace(t *testing.T) {
	for _, initializedShim := range []bool{false, true} {
		for name, whitespace := range map[string]string{
			"spaces": "   ", "tabs": "\t\t", "pretty_printed": " \t\r\n  ",
		} {
			t.Run(fmt.Sprintf("initialized_shim_%t/%s", initializedShim, name), func(t *testing.T) {
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
	for _, rawID := range []any{"reused", float64(123)} {
		t.Run(fmt.Sprint(rawID), func(t *testing.T) {
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
