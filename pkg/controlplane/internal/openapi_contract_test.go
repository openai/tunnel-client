package internal

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestControlPlaneEndpointsMatchOpenAPIContract(t *testing.T) {
	t.Parallel()

	spec := readEndpointContract(t)
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI paths is %T, want object", spec["paths"])
	}

	const tunnelID = "example-tunnel"
	client, err := NewTunnelServiceClient(t.Context(), &runtimeconfig.ControlPlaneConfig{
		BaseURL: mustParseURL(t, "https://example.com"), TunnelID: tunnelID, APIKey: "test-api-key",
	}, nil, newDiscardLogger(), &runtimeconfig.LoggingConfig{}, testMeterProvider)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		path   string
		method string
	}{
		{name: "metadata", path: client.metadataEndpoint.Path, method: http.MethodGet},
		{name: "managed_cloudflare_runtime", path: client.managedCloudflareEndpoint.Path, method: http.MethodGet},
		{name: "poll", path: client.pollEndpoint.Path, method: http.MethodGet},
		{name: "response", path: client.responseEndpoint.Path, method: http.MethodPost},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := strings.Replace(testCase.path, tunnelID, "{tunnel_id}", 1)
			pathItem, ok := paths[path].(map[string]any)
			if !ok {
				t.Fatalf("client path %q is absent from docs/openapi.json", path)
			}
			method := httpMethodKey(testCase.method)
			if _, ok := pathItem[method]; !ok {
				t.Fatalf("client method %s is absent for OpenAPI path %q", testCase.method, path)
			}
		})
	}
}

func readEndpointContract(t *testing.T) map[string]any {
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
		return spec
	}
	t.Fatalf("openapi.json not found in %v", paths)
	return nil
}

func httpMethodKey(method string) string {
	switch method {
	case http.MethodGet:
		return "get"
	case http.MethodPost:
		return "post"
	default:
		return ""
	}
}
