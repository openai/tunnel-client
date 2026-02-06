package adminui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon"
)

func TestHarpoonAdminUIEndpointsPolling(t *testing.T) {
	t.Parallel()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: false,
		MaxResponseBytes:   2048,
		MaxRedirects:       2,
		CapturePayloads:    true,
	}

	registry, err := harpoon.NewRegistry(newHarpoonTestLogger(), false, []harpoon.Target{{
		Label:       "auth",
		Description: "Auth server",
		BaseURL:     mustParseURL(t, "https://auth.example.com"),
	}, {
		Label:       "billing",
		Description: "Billing API",
		BaseURL:     mustParseURL(t, "https://billing.example.com"),
	}})
	require.NoError(t, err)

	buffer := harpoon.NewCallBuffer()
	buffer.RecordCall(harpoon.CallEntry{
		Timestamp:    time.Unix(1, 0),
		Label:        "auth",
		URL:          "https://auth.example.com/v1/ping",
		Method:       http.MethodGet,
		Status:       200,
		LatencyMS:    12,
		ReqBytes:     0,
		RespBytes:    21,
		RequestBody:  "",
		ResponseBody: "{\"ok\":true}",
		BodyIsBase64: false,
	})
	buffer.RecordCall(harpoon.CallEntry{
		Timestamp:    time.Unix(2, 0),
		Label:        "billing",
		URL:          "https://billing.example.com/v1/balance",
		Method:       http.MethodPost,
		Status:       503,
		LatencyMS:    45,
		ReqBytes:     12,
		RespBytes:    0,
		Error:        "upstream error",
		RequestBody:  "{\"user\":\"abc\"}",
		ResponseBody: "",
		BodyIsBase64: false,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/harpoon/status", handleHarpoonStatus(registry, cfg, nil))
	mux.HandleFunc("/api/harpoon/targets", handleHarpoonTargets(registry))
	mux.HandleFunc("/api/harpoon/calls", handleHarpoonCalls(buffer, cfg))

	server := httptest.NewServer(mux)
	defer server.Close()

	var status harpoonStatusResponse
	fetchJSON(t, server.URL+"/api/harpoon/status", &status)
	require.True(t, status.Enabled)
	require.True(t, status.CapturePayloads)
	require.Equal(t, cfg.AllowPlaintextHTTP, status.AllowPlaintextHTTP)
	require.Equal(t, cfg.MaxResponseBytes, status.MaxResponseBytes)
	require.Equal(t, cfg.MaxRedirects, status.MaxRedirects)

	var targets harpoonTargetsResponse
	fetchJSON(t, server.URL+"/api/harpoon/targets", &targets)
	require.Len(t, targets.Targets, 2)
	authTarget := findTarget(t, targets.Targets, "auth")
	require.Equal(t, "https://auth.example.com", authTarget.URL)

	var authCalls harpoonCallsResponse
	fetchJSON(t, server.URL+"/api/harpoon/calls?label=auth", &authCalls)
	require.Len(t, authCalls.Calls, 1)
	require.Equal(t, "auth", authCalls.Calls[0].Label)
	require.NotNil(t, authCalls.Calls[0].ResponseBody)

	buffer.RecordCall(harpoon.CallEntry{
		Timestamp:    time.Unix(3, 0),
		Label:        "auth",
		URL:          "https://auth.example.com/v1/token",
		Method:       http.MethodPost,
		Status:       201,
		LatencyMS:    8,
		ReqBytes:     14,
		RespBytes:    8,
		RequestBody:  "{\"x\":1}",
		ResponseBody: "{\"ok\":1}",
		BodyIsBase64: false,
	})

	fetchJSON(t, server.URL+"/api/harpoon/calls?label=auth", &authCalls)
	require.Len(t, authCalls.Calls, 2)
	require.Equal(t, http.MethodPost, authCalls.Calls[0].Method)

	var limited harpoonCallsResponse
	fetchJSON(t, server.URL+"/api/harpoon/calls?limit=1", &limited)
	require.Len(t, limited.Calls, 1)
}

func fetchJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	dec := json.NewDecoder(resp.Body)
	require.NoError(t, dec.Decode(out))
}

func findTarget(t *testing.T, targets []harpoonTargetResponse, label string) harpoonTargetResponse {
	t.Helper()
	for _, target := range targets {
		if target.Label == label {
			return target
		}
	}
	t.Fatalf("expected target %q", label)
	return harpoonTargetResponse{}
}
