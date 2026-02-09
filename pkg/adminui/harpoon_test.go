package adminui

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon"
)

func TestBuildHarpoonStatusDisabledWithoutTargets(t *testing.T) {
	registry, err := harpoon.NewRegistry(newHarpoonTestLogger(), true, nil)
	require.NoError(t, err)

	out := buildHarpoonStatus(registry, &config.HarpoonConfig{}, nil)
	require.False(t, out.Enabled)
	require.Equal(t, "no targets configured", out.Reason)
	require.False(t, out.CapturePayloads)
	require.False(t, out.AllowPlaintextHTTP)
	require.Zero(t, out.MaxResponseBytes)
	require.Zero(t, out.MaxRedirects)
}

func TestBuildHarpoonStatusIncludesPolicy(t *testing.T) {
	registry, err := harpoon.NewRegistry(newHarpoonTestLogger(), true, []harpoon.Target{{
		Label:   "auth",
		BaseURL: mustParseURL(t, "http://example.com/base"),
	}})
	require.NoError(t, err)

	cfg := &config.HarpoonConfig{
		CapturePayloads:    true,
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   123,
		MaxRedirects:       4,
	}

	out := buildHarpoonStatus(registry, cfg, nil)
	require.True(t, out.Enabled)
	require.True(t, out.CapturePayloads)
	require.True(t, out.AllowPlaintextHTTP)
	require.Equal(t, 123, out.MaxResponseBytes)
	require.Equal(t, 4, out.MaxRedirects)
}

func TestBuildHarpoonTargetsIncludesTargetFields(t *testing.T) {
	registry, err := harpoon.NewRegistry(newHarpoonTestLogger(), true, []harpoon.Target{{
		Label:           "auth",
		Description:     "Auth service",
		Category:        "config",
		Source:          "config",
		Tags:            []string{"auth-server-metadata", "issuer"},
		InclusionReason: "suffix:.internal",
		BaseURL:         mustParseURL(t, "http://example.com/base"),
	}})
	require.NoError(t, err)

	out := buildHarpoonTargets(registry)
	require.Len(t, out.Targets, 1)
	require.Equal(t, "auth", out.Targets[0].Label)
	require.Equal(t, "http://example.com/base", out.Targets[0].URL)
	require.Equal(t, "config", out.Targets[0].Category)
	require.Equal(t, "config", out.Targets[0].Source)
	require.Equal(t, []string{"auth-server-metadata", "issuer"}, out.Targets[0].Tags)
	require.Equal(t, "suffix:.internal", out.Targets[0].InclusionReason)
}

func TestBuildHarpoonCallsIncludesPayloadsWhenEnabled(t *testing.T) {
	buffer := harpoon.NewCallBuffer()
	buffer.RecordCall(harpoon.CallEntry{
		Timestamp:    time.Unix(10, 0).UTC(),
		Label:        "auth",
		URL:          "https://example.com/token",
		Method:       "POST",
		Status:       200,
		LatencyMS:    30,
		ReqBytes:     10,
		RespBytes:    20,
		RequestBody:  `{"a":1}`,
		ResponseBody: `{"ok":true}`,
		BodyIsBase64: false,
	})

	out := buildHarpoonCalls(buffer, &config.HarpoonConfig{CapturePayloads: true}, "auth", 10)
	require.Len(t, out.Calls, 1)
	require.NotNil(t, out.Calls[0].RequestBody)
	require.Equal(t, `{"a":1}`, *out.Calls[0].RequestBody)
	require.NotNil(t, out.Calls[0].BodyIsBase64)
	require.False(t, *out.Calls[0].BodyIsBase64)
}

func TestBuildHarpoonCallsOmitsPayloadsWhenDisabled(t *testing.T) {
	buffer := harpoon.NewCallBuffer()
	buffer.RecordCall(harpoon.CallEntry{
		Timestamp: time.Unix(10, 0).UTC(),
		Label:     "auth",
		URL:       "https://example.com/token",
		Method:    "POST",
		Status:    200,
	})

	out := buildHarpoonCalls(buffer, &config.HarpoonConfig{CapturePayloads: false}, "", 10)
	require.Len(t, out.Calls, 1)
	require.Nil(t, out.Calls[0].RequestBody)
	require.Nil(t, out.Calls[0].ResponseBody)
	require.Nil(t, out.Calls[0].BodyIsBase64)
}

func newHarpoonTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
