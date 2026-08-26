package wiretypes

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestOpenAPIContractSurface(t *testing.T) {
	spec, raw := readOpenAPISpec(t)

	if got := mustString(t, spec["openapi"], "openapi"); got != "3.1.0" {
		t.Fatalf("expected OpenAPI 3.1.0, got %q", got)
	}
	if strings.Contains(string(raw), "x-openai-") {
		t.Fatal("public OpenAPI contract must not contain service-internal extensions")
	}

	paths := mustMap(t, spec["paths"], "paths")
	expected := map[string]string{
		"/v1/tunnels/{tunnel_id}":                    "get",
		"/v1/tunnels/{tunnel_id}/cloudflare/runtime": "get",
		"/v1/tunnels/{tunnel_id}/poll":               "get",
		"/v1/tunnels/{tunnel_id}/response":           "post",
	}
	if len(paths) != len(expected) {
		t.Fatalf("expected only %d public paths, got %d", len(expected), len(paths))
	}
	for path, method := range expected {
		pathItem := mustMap(t, paths[path], "paths."+path)
		if len(pathItem) != 1 {
			t.Fatalf("expected exactly one method for %s, got %v", path, mapKeys(pathItem))
		}
		if _, ok := pathItem[method]; !ok {
			t.Fatalf("expected %s %s, got methods %v", strings.ToUpper(method), path, mapKeys(pathItem))
		}
	}

	assertOperationID(t, spec, "/v1/tunnels/{tunnel_id}", "get", "getTunnelMetadata")
	assertOperationID(t, spec, "/v1/tunnels/{tunnel_id}/cloudflare/runtime", "get", "getCloudflareRuntime")
	assertOperationID(t, spec, "/v1/tunnels/{tunnel_id}/poll", "get", "pollTunnelCommands")
	assertOperationID(t, spec, "/v1/tunnels/{tunnel_id}/response", "post", "postTunnelResponse")
	const wantMCPServerInfoExample = `{"version":1,"channels":[{"name":"main","proc_affinity":true},{"name":"harpoon","proc_affinity":true}]}`
	const wantModernHarpoonExample = `{"version":2,"channels":[{"name":"harpoon","stateless":true,"proc_affinity":true}]}`
	const wantWireProtocolVersion = "2026-08-25"
	for path, method := range map[string]string{
		"/v1/tunnels/{tunnel_id}":                    "get",
		"/v1/tunnels/{tunnel_id}/cloudflare/runtime": "get",
		"/v1/tunnels/{tunnel_id}/poll":               "get",
		"/v1/tunnels/{tunnel_id}/response":           "post",
	} {
		header := headerParameter(t, operation(t, spec, path, method), "X-Tunnel-MCP-Server-Info")
		if header["required"] == true {
			t.Fatalf("%s %s MCP server info header must remain optional", strings.ToUpper(method), path)
		}
		schema := mustMap(t, header["schema"], path+".MCPServerInfo.schema")
		if got := mustString(t, schema["type"], path+".MCPServerInfo.type"); got != "string" {
			t.Fatalf("%s %s MCP server info type = %q, want string", strings.ToUpper(method), path, got)
		}
		if got, ok := schema["maxLength"].(float64); !ok || got != 4096 {
			t.Fatalf("%s %s MCP server info maxLength = %v, want 4096", strings.ToUpper(method), path, schema["maxLength"])
		}
		examples := mustMap(t, header["examples"], path+".MCPServerInfo.examples")
		example := mustMap(t, examples["legacy-stdio-and-harpoon"], path+".MCPServerInfo.examples.legacy-stdio-and-harpoon")
		if got := mustString(t, example["value"], path+".MCPServerInfo.examples.legacy-stdio-and-harpoon.value"); got != wantMCPServerInfoExample {
			t.Fatalf("%s %s MCP server info example = %q, want %q", strings.ToUpper(method), path, got, wantMCPServerInfoExample)
		}
		modernExample := mustMap(t, examples["modern-harpoon"], path+".MCPServerInfo.examples.modern-harpoon")
		if got := mustString(t, modernExample["value"], path+".MCPServerInfo.examples.modern-harpoon.value"); got != wantModernHarpoonExample {
			t.Fatalf("%s %s modern Harpoon example = %q, want %q", strings.ToUpper(method), path, got, wantModernHarpoonExample)
		}

		wireVersion := headerParameter(t, operation(t, spec, path, method), "X-Tunnel-Client-Wire-Protocol-Version")
		if wireVersion["required"] == true {
			t.Fatalf("%s %s tunnel wire protocol header must remain optional", strings.ToUpper(method), path)
		}
		wireVersionSchema := mustMap(t, wireVersion["schema"], path+".WireProtocolVersion.schema")
		if got := mustString(t, wireVersionSchema["type"], path+".WireProtocolVersion.type"); got != "string" {
			t.Fatalf("%s %s tunnel wire protocol type = %q, want string", strings.ToUpper(method), path, got)
		}
		if got := mustString(t, wireVersion["example"], path+".WireProtocolVersion.example"); got != wantWireProtocolVersion {
			t.Fatalf("%s %s tunnel wire protocol example = %q, want %q", strings.ToUpper(method), path, got, wantWireProtocolVersion)
		}
	}
	runtime := operation(t, spec, "/v1/tunnels/{tunnel_id}/cloudflare/runtime", "get")
	runtimeResponses := mustMap(t, runtime["responses"], "runtime.responses")
	for _, statusCode := range []string{"200", "400", "401", "404", "500", "503"} {
		if _, ok := runtimeResponses[statusCode]; !ok {
			t.Fatalf("runtime operation must document %s", statusCode)
		}
	}
	runtimeSuccess := mustMap(t, runtimeResponses["200"], "runtime.responses.200")
	runtimeHeaders := mustMap(t, runtimeSuccess["headers"], "runtime.responses.200.headers")
	for _, headerName := range []string{"Cache-Control", "Pragma"} {
		if _, ok := runtimeHeaders[headerName]; !ok {
			t.Fatalf("runtime success response must document %s", headerName)
		}
	}

	poll := operation(t, spec, "/v1/tunnels/{tunnel_id}/poll", "get")
	responses := mustMap(t, poll["responses"], "poll.responses")
	if _, ok := responses["204"]; !ok {
		t.Fatal("poll operation must document 204 No Content")
	}

	response := operation(t, spec, "/v1/tunnels/{tunnel_id}/response", "post")
	if !hasRequiredHeader(response, "X-Tunnel-Shard-Token") {
		t.Fatal("response operation must require X-Tunnel-Shard-Token")
	}
	for _, path := range []string{
		"/v1/tunnels/{tunnel_id}/poll",
		"/v1/tunnels/{tunnel_id}/response",
	} {
		op := operation(t, spec, path, map[string]string{
			"/v1/tunnels/{tunnel_id}/poll":     "get",
			"/v1/tunnels/{tunnel_id}/response": "post",
		}[path])
		responses := mustMap(t, op["responses"], path+".responses")
		for _, statusCode := range []string{"429", "503"} {
			retryableResponse := mustMap(t, responses[statusCode], path+"."+statusCode)
			headers := mustMap(t, retryableResponse["headers"], path+"."+statusCode+".headers")
			retryAfter := mustMap(t, headers["Retry-After"], path+"."+statusCode+".Retry-After")
			schema := mustMap(t, retryAfter["schema"], path+"."+statusCode+".Retry-After.schema")
			if got := mustString(t, schema["type"], path+"."+statusCode+".Retry-After.type"); got != "string" {
				t.Fatalf("%s %s Retry-After type = %q, want string", path, statusCode, got)
			}
		}
	}

	components := mustMap(t, spec["components"], "components")
	componentSchemas := mustMap(t, components["schemas"], "components.schemas")
	runtimeResponse := mustMap(
		t,
		componentSchemas["ManagedCloudflareRuntimeResponse"],
		"components.schemas.ManagedCloudflareRuntimeResponse",
	)
	runtimeProperties := mustMap(
		t,
		runtimeResponse["properties"],
		"ManagedCloudflareRuntimeResponse.properties",
	)
	runtimeToken := mustMap(
		t,
		runtimeProperties["runtime_token"],
		"ManagedCloudflareRuntimeResponse.runtime_token",
	)
	if got := mustString(t, runtimeToken["format"], "runtime_token.format"); got != "password" {
		t.Fatalf("runtime token format = %q, want password", got)
	}
	if _, ok := runtimeToken["writeOnly"]; ok {
		t.Fatal("runtime token is returned by this GET response and must not be writeOnly")
	}
	requiredRuntimeFields := stringSlice(
		t,
		runtimeResponse["required"],
		"ManagedCloudflareRuntimeResponse.required",
	)
	for _, requiredField := range []string{"cloudflare_tunnel", "runtime_token"} {
		if !hasString(requiredRuntimeFields, requiredField) {
			t.Fatalf("managed Cloudflare runtime response must require %q", requiredField)
		}
	}
	for _, schemaName := range []string{"JsonRpcPolledCommand", "SessionTerminationPolledCommand"} {
		command := mustMap(t, componentSchemas[schemaName], "components.schemas."+schemaName)
		properties := mustMap(t, command["properties"], schemaName+".properties")
		responseTimeout := mustMap(t, properties["response_timeout"], schemaName+".response_timeout")
		for _, value := range []string{"30s", "4500ms", "1ns", "1us", "1ms", "2m", "3h", "0s", "0h"} {
			requireValidAgainstSchema(t, spec, responseTimeout, value, schemaName+" response_timeout")
		}
		requireValidAgainstSchema(t, spec, responseTimeout, nil, schemaName+" null response_timeout")

		for _, value := range []string{
			"-1s",
			"+1s",
			"4.5s",
			"1m30s",
			" 1s",
			"1s ",
			"1s\n",
			"1e3s",
			"1d",
			"1S",
			"1µs",
		} {
			if err := validateAgainstSchema(spec, responseTimeout, value, "$.response_timeout"); err == nil {
				t.Fatalf("%s response_timeout must reject %q", schemaName, value)
			}
		}
		for _, value := range []any{float64(30), true, map[string]any{}, []any{}} {
			if err := validateAgainstSchema(spec, responseTimeout, value, "$.response_timeout"); err == nil {
				t.Fatalf("%s response_timeout must reject JSON value %#v", schemaName, value)
			}
		}

		if additional, ok := command["additionalProperties"].(bool); !ok || !additional {
			t.Fatalf("%s must allow additive fields for forward compatibility", schemaName)
		}
		commandType := "jsonrpc"
		if schemaName == "SessionTerminationPolledCommand" {
			commandType = "session_termination"
		}
		commandWithFutureField := map[string]any{
			"request_id":   "req-future",
			"shard_token":  "shard-future",
			"created_at":   "2025-01-01T00:00:00Z",
			"command_type": commandType,
			"future_field": map[string]any{"value": true},
		}
		if schemaName == "JsonRpcPolledCommand" {
			commandWithFutureField["jsonrpc"] = map[string]any{"jsonrpc": "2.0", "id": "rpc-1", "method": "ping"}
		}
		requireValidAgainstSchema(t, spec, command, commandWithFutureField, schemaName+" with additive field")
		for _, required := range stringSlice(t, command["required"], schemaName+".required") {
			if required == "response_timeout" {
				t.Fatalf("%s response_timeout must remain optional", schemaName)
			}
		}
	}
	assertTunnelFailureContract(t, spec, componentSchemas)

	securitySchemes := mustMap(t, components["securitySchemes"], "components.securitySchemes")
	bearer := mustMap(t, securitySchemes["BearerAuth"], "components.securitySchemes.BearerAuth")
	if got := mustString(t, bearer["scheme"], "BearerAuth.scheme"); got != "bearer" {
		t.Fatalf("expected bearer auth scheme, got %q", got)
	}
}

func assertTunnelFailureContract(t *testing.T, spec map[string]any, componentSchemas map[string]any) {
	t.Helper()
	responsePayload := mustMap(t, componentSchemas["TunnelResponsePayload"], "TunnelResponsePayload")
	responseProperties := mustMap(t, responsePayload["properties"], "TunnelResponsePayload.properties")
	responseJSON := mustMap(t, responseProperties["resp_json"], "TunnelResponsePayload.resp_json")
	failureSchema := mustMap(t, responseJSON["x-tunnel-failure-schema"], "resp_json.x-tunnel-failure-schema")
	failureProperties := mustMap(t, failureSchema["properties"], "tunnel_failure.properties")

	if got := mustString(t, failureSchema["$schema"], "tunnel_failure.$schema"); got != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("unexpected tunnel failure schema dialect %q", got)
	}
	if additional, ok := failureSchema["additionalProperties"].(bool); !ok || !additional {
		t.Fatal("tunnel failure schema must tolerate additive future fields")
	}
	version := mustMap(t, failureProperties["version"], "tunnel_failure.version")
	if current, ok := version["x-current-version"].(float64); !ok || current != 1 {
		t.Fatalf("expected tunnel failure current version 1, got %v", version["x-current-version"])
	}
	source := mustMap(t, failureProperties["source"], "tunnel_failure.source")
	if _, restrictive := source["enum"]; restrictive {
		t.Fatal("tunnel failure source must not reject unknown future values")
	}
	wantSources := []string{
		"target_http",
		"dns",
		"tls",
		"connect",
		"transport_closed",
		"timeout",
		"protocol",
		"client_internal",
	}
	if got := stringSlice(t, source["x-known-values"], "tunnel_failure.source.x-known-values"); !reflect.DeepEqual(got, wantSources) {
		t.Fatalf("unexpected known tunnel failure sources: got %v want %v", got, wantSources)
	}
	transportErrorKind := mustMap(t, failureProperties["transport_error_kind"], "tunnel_failure.transport_error_kind")
	if got := mustString(t, transportErrorKind["type"], "tunnel_failure.transport_error_kind.type"); got != "string" {
		t.Fatalf("expected transport error kind to be a string, got %q", got)
	}
	if maxLength, ok := transportErrorKind["maxLength"].(float64); !ok || maxLength != 64 {
		t.Fatalf("expected transport error kind maxLength 64, got %v", transportErrorKind["maxLength"])
	}
	wantTransportErrorKinds := []string{
		"closed_pipe",
		"connection_aborted",
		"connection_closed",
		"connection_refused",
		"connection_reset",
		"dial",
		"dns",
		"eof",
		"host_unreachable",
		"http_status",
		"invalid_mcp_error",
		"invalid_protocol_response",
		"malformed_json",
		"network_unreachable",
		"non_protocol_response",
		"response_body_missing",
		"response_body_too_large",
		"response_body_unreadable",
		"timeout",
		"tls",
		"unexpected_eof",
		"unknown",
	}
	if got := stringSlice(t, transportErrorKind["x-known-values"], "tunnel_failure.transport_error_kind.x-known-values"); !reflect.DeepEqual(got, wantTransportErrorKinds) {
		t.Fatalf("unexpected known transport error kinds: got %v want %v", got, wantTransportErrorKinds)
	}
	for _, required := range stringSlice(t, failureSchema["required"], "tunnel_failure.required") {
		if required == "transport_error_kind" {
			t.Fatal("transport_error_kind must remain optional for legacy clients")
		}
	}

	valid := []map[string]any{
		{
			"version":                    float64(1),
			"source":                     "transport_closed",
			"transport_error_kind":       "closed_pipe",
			"upstream_response_received": false,
		},
		{
			"version":                    float64(1),
			"source":                     "target_http",
			"upstream_response_received": true,
			"upstream_status":            float64(502),
		},
		{
			"version":                    float64(37),
			"source":                     "future_source",
			"upstream_response_received": false,
			"future_field":               map[string]any{"ignored": true},
		},
	}
	for index, value := range valid {
		requireValidAgainstSchema(t, spec, failureSchema, value, fmt.Sprintf("valid tunnel failure %d", index))
	}

	invalid := []map[string]any{
		{
			"version":                    float64(1),
			"source":                     "target_http",
			"upstream_response_received": false,
			"upstream_status":            float64(502),
		},
		{
			"version":                    float64(1),
			"source":                     "target_http",
			"upstream_response_received": true,
		},
		{
			"version":                    float64(1),
			"source":                     "transport_closed",
			"upstream_response_received": true,
		},
		{
			"version":                    float64(1),
			"source":                     "dns",
			"upstream_response_received": false,
			"upstream_status":            float64(502),
		},
		{
			"version":                    float64(1),
			"source":                     "dns",
			"transport_error_kind":       strings.Repeat("x", 65),
			"upstream_response_received": false,
		},
	}
	for index, value := range invalid {
		if err := validateAgainstSchema(spec, failureSchema, value, "$.tunnel_failure"); err == nil {
			t.Fatalf("invalid tunnel failure %d unexpectedly matched the schema: %#v", index, value)
		}
	}
}

func TestOpenAPIExamplesMatchWireTypes(t *testing.T) {
	spec, _ := readOpenAPISpec(t)

	poll := operation(t, spec, "/v1/tunnels/{tunnel_id}/poll", "get")
	pollContent := responseContent(t, poll, "200")
	pollExample := pollContent["example"]
	pollSchema := mustMap(t, pollContent["schema"], "poll.200.schema")
	requireValidAgainstSchema(t, spec, pollSchema, pollExample, "poll example")

	pollJSON, err := json.Marshal(pollExample)
	if err != nil {
		t.Fatalf("marshal poll example: %v", err)
	}
	var envelope PolledCommandEnvelope
	if err := json.Unmarshal(pollJSON, &envelope); err != nil {
		t.Fatalf("decode poll example with Go envelope: %v", err)
	}
	if len(envelope.Commands) != 2 {
		t.Fatalf("expected two documented poll commands, got %d", len(envelope.Commands))
	}

	var rpc RawJSONRPCPolledCommand
	if err := json.Unmarshal(envelope.Commands[0], &rpc); err != nil {
		t.Fatalf("decode documented jsonrpc command: %v", err)
	}
	if rpc.CommandType != CommandTypeJSONRPC || len(rpc.JSONRPC) == 0 {
		t.Fatalf("unexpected documented jsonrpc command: %#v", rpc)
	}
	assertResponseTimeoutValue(t, rpc.ResponseTimeout, 30*time.Second, "documented jsonrpc command")

	var termination RawSessionTerminationPolledCommand
	if err := json.Unmarshal(envelope.Commands[1], &termination); err != nil {
		t.Fatalf("decode documented session termination command: %v", err)
	}
	if termination.CommandType != CommandTypeSessionTermination {
		t.Fatalf("unexpected documented session command type %q", termination.CommandType)
	}
	assertResponseTimeoutValue(t, termination.ResponseTimeout, 30*time.Second, "documented session command")

	response := operation(t, spec, "/v1/tunnels/{tunnel_id}/response", "post")
	requestContent := requestBodyContent(t, response)
	responseExample := requestContent["example"]
	responseSchema := mustMap(t, requestContent["schema"], "response.requestBody.schema")
	requireValidAgainstSchema(t, spec, responseSchema, responseExample, "response example")

	responseJSON, err := json.Marshal(responseExample)
	if err != nil {
		t.Fatalf("marshal response example: %v", err)
	}
	var payload TunnelResponsePayload
	if err := json.Unmarshal(responseJSON, &payload); err != nil {
		t.Fatalf("decode response example with Go payload: %v", err)
	}
	if payload.ResponseType != ResponsePayloadJSONRPC {
		t.Fatalf("expected documented response type %q, got %q", ResponsePayloadJSONRPC, payload.ResponseType)
	}
}

func assertResponseTimeoutValue(t *testing.T, value *ResponseTimeoutDuration, want time.Duration, label string) {
	t.Helper()
	if value == nil {
		t.Fatalf("%s response_timeout is absent", label)
		return
	}
	got, ok := value.Value()
	if !ok || got != want {
		t.Fatalf("%s response_timeout = %v, %v; want %v, true", label, got, ok, want)
	}
}

func TestGoResponsePayloadsMatchOpenAPI(t *testing.T) {
	spec, _ := readOpenAPISpec(t)
	response := operation(t, spec, "/v1/tunnels/{tunnel_id}/response", "post")
	requestContent := requestBodyContent(t, response)
	responseSchema := mustMap(t, requestContent["schema"], "response.requestBody.schema")

	payloads := []TunnelResponsePayload{
		{
			RequestID:    "req-jsonrpc",
			Channel:      "main",
			JSONResponse: json.RawMessage(`{"jsonrpc":"2.0","id":"rpc-1","result":{}}`),
			ResponseCode: http.StatusOK,
			ResponseType: ResponsePayloadJSONRPC,
		},
		{
			RequestID:    "req-notify",
			Channel:      "main",
			JSONResponse: json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`),
			ResponseCode: http.StatusOK,
			ResponseType: ResponsePayloadJSONRPCNotify,
		},
		{
			RequestID:    "req-ack",
			Channel:      "main",
			ResponseCode: http.StatusAccepted,
			ResponseType: ResponsePayloadNotifyAck,
		},
		{
			RequestID:    "req-termination",
			Channel:      "main",
			ResponseCode: http.StatusNoContent,
			ResponseType: ResponsePayloadSessionTermination,
		},
	}

	for _, payload := range payloads {
		payload := payload
		t.Run(string(payload.ResponseType), func(t *testing.T) {
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal Go response payload: %v", err)
			}
			var value any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatalf("decode marshaled Go response payload: %v", err)
			}
			requireValidAgainstSchema(t, spec, responseSchema, value, "Go response payload")
		})
	}
}

func TestSynthesizedFailureResponseMatchesTunnelServiceOpenAPI(t *testing.T) {
	spec, _ := readOpenAPISpec(t)
	response := operation(t, spec, "/v1/tunnels/{tunnel_id}/response", "post")
	requestContent := requestBodyContent(t, response)
	responseSchema := mustMap(t, requestContent["schema"], "response.requestBody.schema")

	payload := TunnelResponsePayload{
		RequestID: "cmd-target-http-failure",
		Channel:   "main",
		JSONResponse: json.RawMessage(`{
			"jsonrpc":"2.0",
			"id":"rpc-target-http-failure",
			"error":{
				"code":-32603,
				"message":"Service Unavailable",
				"data":{
					"tunnel_failure":{
						"version":1,
						"source":"target_http",
						"transport_error_kind":"http_status",
						"upstream_response_received":true,
						"upstream_status":503
					}
				}
			}
		}`),
		ResponseCode: http.StatusServiceUnavailable,
		ResponseType: ResponsePayloadJSONRPC,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal synthesized failure response: %v", err)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode synthesized failure response: %v", err)
	}
	requireValidAgainstSchema(t, spec, responseSchema, value, "synthesized tunnel failure response")
}

func TestGoDiscriminatorsCoverPublishedOpenAPI(t *testing.T) {
	spec, _ := readOpenAPISpec(t)
	schemas := schemas(t, spec)

	responseType := mustMap(t, schemas["ResponsePayloadType"], "ResponsePayloadType")
	gotResponseTypes := stringSlice(t, responseType["enum"], "ResponsePayloadType.enum")
	wantResponseTypes := []string{
		string(ResponsePayloadJSONRPC),
		string(ResponsePayloadJSONRPCNotify),
		string(ResponsePayloadNotifyAck),
		string(ResponsePayloadSessionTermination),
	}
	if !reflect.DeepEqual(gotResponseTypes, wantResponseTypes) {
		t.Fatalf("published response discriminators do not match Go support: got %v want %v", gotResponseTypes, wantResponseTypes)
	}

	envelope := mustMap(t, schemas["PolledCommandList"], "PolledCommandList")
	properties := mustMap(t, envelope["properties"], "PolledCommandList.properties")
	commands := mustMap(t, properties["commands"], "PolledCommandList.properties.commands")
	items := mustMap(t, commands["items"], "PolledCommandList.properties.commands.items")
	discriminator := mustMap(t, items["discriminator"], "PolledCommandList.discriminator")
	mapping := mustMap(t, discriminator["mapping"], "PolledCommandList.discriminator.mapping")
	wantCommands := []string{
		string(CommandTypeJSONRPC),
		string(CommandTypeSessionTermination),
	}
	if got := mapKeys(mapping); !reflect.DeepEqual(got, wantCommands) {
		t.Fatalf("published command discriminators do not match Go support: got %v want %v", got, wantCommands)
	}
}

func readOpenAPISpec(t *testing.T) (map[string]any, []byte) {
	t.Helper()
	paths := []string{
		"api/tunnel-client/docs/openapi.json",
		"../../../docs/openapi.json",
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var spec map[string]any
		if err := json.Unmarshal(raw, &spec); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return spec, raw
	}
	t.Fatalf("openapi.json not found in %v", paths)
	return nil, nil
}

func operation(t *testing.T, spec map[string]any, path string, method string) map[string]any {
	t.Helper()
	paths := mustMap(t, spec["paths"], "paths")
	pathItem := mustMap(t, paths[path], "paths."+path)
	return mustMap(t, pathItem[method], method+" "+path)
}

func assertOperationID(t *testing.T, spec map[string]any, path string, method string, want string) {
	t.Helper()
	if got := mustString(t, operation(t, spec, path, method)["operationId"], method+" "+path+".operationId"); got != want {
		t.Fatalf("expected %s %s operationId %q, got %q", strings.ToUpper(method), path, want, got)
	}
}

func responseContent(t *testing.T, operation map[string]any, status string) map[string]any {
	t.Helper()
	responses := mustMap(t, operation["responses"], "responses")
	response := mustMap(t, responses[status], "responses."+status)
	content := mustMap(t, response["content"], "responses."+status+".content")
	return mustMap(t, content["application/json"], "responses."+status+".content.application/json")
}

func requestBodyContent(t *testing.T, operation map[string]any) map[string]any {
	t.Helper()
	requestBody := mustMap(t, operation["requestBody"], "requestBody")
	content := mustMap(t, requestBody["content"], "requestBody.content")
	return mustMap(t, content["application/json"], "requestBody.content.application/json")
}

func hasRequiredHeader(operation map[string]any, name string) bool {
	parameters, ok := operation["parameters"].([]any)
	if !ok {
		return false
	}
	for _, item := range parameters {
		parameter, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if parameter["name"] == name && parameter["in"] == "header" && parameter["required"] == true {
			return true
		}
	}
	return false
}

func headerParameter(t *testing.T, operation map[string]any, name string) map[string]any {
	t.Helper()

	parameters, ok := operation["parameters"].([]any)
	if !ok {
		t.Fatalf("operation parameters is %T, want array", operation["parameters"])
	}
	for _, item := range parameters {
		parameter, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if parameter["name"] == name && parameter["in"] == "header" {
			return parameter
		}
	}
	t.Fatalf("operation is missing optional header %q", name)
	return nil
}

func schemas(t *testing.T, spec map[string]any) map[string]any {
	t.Helper()
	components := mustMap(t, spec["components"], "components")
	return mustMap(t, components["schemas"], "components.schemas")
}

func requireValidAgainstSchema(t *testing.T, spec map[string]any, schema map[string]any, value any, label string) {
	t.Helper()
	if err := validateAgainstSchema(spec, schema, value, "$"); err != nil {
		t.Fatalf("%s does not match OpenAPI: %v", label, err)
	}
}

func validateAgainstSchema(spec map[string]any, schema map[string]any, value any, path string) error {
	if ref, ok := schema["$ref"].(string); ok {
		resolved, err := resolveSchemaRef(spec, ref)
		if err != nil {
			return err
		}
		return validateAgainstSchema(spec, resolved, value, path)
	}
	if variants, ok := schema["allOf"].([]any); ok {
		for _, variant := range variants {
			variantSchema, ok := variant.(map[string]any)
			if !ok {
				continue
			}
			if err := validateAgainstSchema(spec, variantSchema, value, path); err != nil {
				return err
			}
		}
	}
	if condition, ok := schema["if"].(map[string]any); ok {
		if validateAgainstSchema(spec, condition, value, path) == nil {
			if consequence, ok := schema["then"].(map[string]any); ok {
				if err := validateAgainstSchema(spec, consequence, value, path); err != nil {
					return err
				}
			}
		} else if alternative, ok := schema["else"].(map[string]any); ok {
			if err := validateAgainstSchema(spec, alternative, value, path); err != nil {
				return err
			}
		}
	}
	if variants, ok := schema["anyOf"].([]any); ok {
		return validateVariant(spec, variants, value, path, false)
	}
	if variants, ok := schema["oneOf"].([]any); ok {
		return validateVariant(spec, variants, value, path, true)
	}

	if constant, ok := schema["const"]; ok && !reflect.DeepEqual(constant, value) {
		return fmt.Errorf("%s: value %v does not equal const %v", path, value, constant)
	}
	if enumValues, ok := schema["enum"].([]any); ok {
		matched := false
		for _, enumValue := range enumValues {
			if reflect.DeepEqual(enumValue, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value %v is not in enum %v", path, value, enumValues)
		}
	}

	schemaType, _ := schema["type"].(string)
	if schemaType == "" {
		if _, hasProperties := schema["properties"]; hasProperties {
			schemaType = "object"
		} else if _, hasRequired := schema["required"]; hasRequired {
			schemaType = "object"
		}
	}
	switch schemaType {
	case "":
		return nil
	case "null":
		if value != nil {
			return fmt.Errorf("%s: expected null, got %T", path, value)
		}
	case "string":
		stringValue, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s: expected string, got %T", path, value)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("%s: invalid schema pattern %q: %w", path, pattern, err)
			}
			if !compiled.MatchString(stringValue) {
				return fmt.Errorf("%s: string %q does not match pattern %q", path, stringValue, pattern)
			}
		}
		if minimum, ok := schema["minLength"].(float64); ok && float64(len(stringValue)) < minimum {
			return fmt.Errorf("%s: string length %d is below minimum %v", path, len(stringValue), minimum)
		}
		if maximum, ok := schema["maxLength"].(float64); ok && float64(len(stringValue)) > maximum {
			return fmt.Errorf("%s: string length %d exceeds maximum %v", path, len(stringValue), maximum)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number {
			return fmt.Errorf("%s: expected integer, got %v", path, value)
		}
		if minimum, ok := schema["minimum"].(float64); ok && number < minimum {
			return fmt.Errorf("%s: integer %v is below minimum %v", path, number, minimum)
		}
		if maximum, ok := schema["maximum"].(float64); ok && number > maximum {
			return fmt.Errorf("%s: integer %v exceeds maximum %v", path, number, maximum)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s: expected number, got %T", path, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: expected boolean, got %T", path, value)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array, got %T", path, value)
		}
		itemSchema, hasItems := schema["items"].(map[string]any)
		if !hasItems {
			return nil
		}
		for index, item := range items {
			if err := validateAgainstSchema(spec, itemSchema, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object, got %T", path, value)
		}
		required, _ := schema["required"].([]any)
		for _, requiredValue := range required {
			name, ok := requiredValue.(string)
			if !ok {
				continue
			}
			if _, present := object[name]; !present {
				return fmt.Errorf("%s: missing required property %q", path, name)
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, propertyValue := range object {
			propertySchemaValue, known := properties[name]
			if known {
				propertySchema, ok := propertySchemaValue.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.%s: invalid property schema", path, name)
				}
				if err := validateAgainstSchema(spec, propertySchema, propertyValue, path+"."+name); err != nil {
					return err
				}
				continue
			}
			switch additional := schema["additionalProperties"].(type) {
			case bool:
				if !additional {
					return fmt.Errorf("%s: additional property %q is forbidden", path, name)
				}
			case map[string]any:
				if err := validateAgainstSchema(spec, additional, propertyValue, path+"."+name); err != nil {
					return err
				}
			}
		}
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, schemaType)
	}
	return nil
}

func validateVariant(spec map[string]any, variants []any, value any, path string, exactlyOne bool) error {
	matches := 0
	var lastErr error
	for _, variant := range variants {
		variantSchema, ok := variant.(map[string]any)
		if !ok {
			continue
		}
		if err := validateAgainstSchema(spec, variantSchema, value, path); err != nil {
			lastErr = err
			continue
		}
		matches++
	}
	if matches == 0 {
		return fmt.Errorf("%s: no schema variant matched: %v", path, lastErr)
	}
	if exactlyOne && matches != 1 {
		return fmt.Errorf("%s: expected one schema variant, matched %d", path, matches)
	}
	return nil
}

func resolveSchemaRef(spec map[string]any, ref string) (map[string]any, error) {
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return nil, fmt.Errorf("unsupported OpenAPI reference %q", ref)
	}
	components, ok := spec["components"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("components is not an object")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("components.schemas is not an object")
	}
	schema, ok := schemas[strings.TrimPrefix(ref, prefix)].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing schema for %q", ref)
	}
	return schema, nil
}

func mustMap(t *testing.T, value any, path string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s: expected object, got %T", path, value)
	}
	return result
}

func mustString(t *testing.T, value any, path string) string {
	t.Helper()
	result, ok := value.(string)
	if !ok {
		t.Fatalf("%s: expected string, got %T", path, value)
	}
	return result
}

func stringSlice(t *testing.T, value any, path string) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s: expected array, got %T", path, value)
	}
	result := make([]string, 0, len(items))
	for index, item := range items {
		result = append(result, mustString(t, item, fmt.Sprintf("%s[%d]", path, index)))
	}
	return result
}

func mapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
