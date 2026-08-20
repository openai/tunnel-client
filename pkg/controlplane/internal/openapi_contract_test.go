package internal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
)

func TestControlPlaneEndpointsMatchOpenAPIContract(t *testing.T) {
	spec := readEndpointContract(t)
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI paths is %T, want object", spec["paths"])
	}

	cases := []struct {
		name       string
		pathFormat string
		method     string
	}{
		{name: "metadata", pathFormat: metadataPathFormat, method: http.MethodGet},
		{name: "managed_cloudflare_runtime", pathFormat: managedCloudflarePathFormat, method: http.MethodGet},
		{name: "poll", pathFormat: pollPathFormat, method: http.MethodGet},
		{name: "response", pathFormat: responsePathFormat, method: http.MethodPost},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			path := fmt.Sprintf(testCase.pathFormat, "{tunnel_id}")
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
