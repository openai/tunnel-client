package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDoctorSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resource":"` + serverURLWithoutTrailingSlash(r) + `/mcp","authorization_servers":["https://auth.example.com"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.server-url", "url="+server.URL+"/mcp,channel=main",
		"--health.listen-addr", "127.0.0.1:7777",
	)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "CHECK config_source")
	require.Contains(t, stdout, "CHECK tunnel_id")
	require.Contains(t, stdout, "CHECK mcp_server_reachable")
	require.Contains(t, stdout, "CHECK oauth_metadata")
	require.Contains(t, stdout, "CHECK ui")
	require.Contains(t, stdout, "RESULT ok")
	require.Contains(t, stdout, "NEXT   tunnel-client run")
}

func TestDoctorFailureExplain(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME": t.TempDir(),
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.server-url", "url=http://127.0.0.1:65534/mcp,channel=main",
		"--explain",
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Equal(t, 2, exitCode(err))
	require.Contains(t, stdout, "RESULT fail")
	require.Contains(t, stdout, "FAILED_CHECKS control_plane_api_key")
	require.Contains(t, stdout, "Why this matters:")
	require.Contains(t, stdout, "What to do next:")
}

func TestDoctorDetectsHealthListenerBindConflictByDefault(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, listener.Close())
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resource":"` + serverURLWithoutTrailingSlash(r) + `/mcp","authorization_servers":["https://auth.example.com"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.server-url", "url="+server.URL+"/mcp,channel=main",
		"--health.listen-addr", listener.Addr().String(),
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Equal(t, 2, exitCode(err))
	require.Contains(t, stdout, "CHECK health_listener")
	require.Contains(t, stdout, "address already in use")
	require.Contains(t, stdout, "CHECK ui")
	require.Contains(t, stdout, "blocked by health listener check")
	require.Contains(t, stdout, "FAILED_CHECKS health_listener")
}

func TestDoctorJSONOutput(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resource":"` + serverURLWithoutTrailingSlash(r) + `/mcp","authorization_servers":["https://auth.example.com"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.server-url", "url="+server.URL+"/mcp,channel=main",
		"--json",
	)

	require.NoError(t, err, stderr)
	var report doctorReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &report))
	require.Equal(t, "ok", report.Result)
	require.NotEmpty(t, report.Checks)
}

func TestDoctorReadsProfile(t *testing.T) {
	t.Parallel()

	profileDir := t.TempDir()
	path := filepath.Join(profileDir, "sample.yaml")
	data, err := generateProfileSample("sample_mcp_with_dcr", sampleProfileRequest{
		TunnelID:         "tunnel_0123456789abcdef0123456789abcdef",
		APIKeyRef:        "env:CONTROL_PLANE_API_KEY",
		HealthListenAddr: "127.0.0.1:7777",
		MCPCommand:       "python -m http.server",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor", "--config", path)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "CHECK profile_load")
	require.Contains(t, stdout, path)
}

func TestDoctorUsesEphemeralUIHintForPortZero(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.command", "python server.py",
		"--health.listen-addr", "127.0.0.1:0",
	)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "CHECK health_listener")
	require.Contains(t, stdout, "ephemeral bind ok")
	require.Contains(t, stdout, "CHECK ui")
	require.Contains(t, stdout, "inspect startup summary or HEALTH_URL_FILE")
}

func TestDoctorBaseURLUsesLoopbackForWildcardAndInvalidListenAddrs(t *testing.T) {
	t.Parallel()

	require.Equal(t, "http://127.0.0.1:8080", doctorBaseURL(":8080"))
	require.Equal(t, "http://127.0.0.1:8080", doctorBaseURL("bad-listen-addr"))
}

func executeCommand(t *testing.T, env map[string]string, args ...string) (string, string, error) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root := newRootCommand(func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}, &stdout, &stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func exitCode(err error) int {
	type exitCoder interface {
		ExitCode() int
	}
	if err == nil {
		return 0
	}
	var codeErr exitCoder
	if errors.As(err, &codeErr) {
		return codeErr.ExitCode()
	}
	return 1
}

func serverURLWithoutTrailingSlash(r *http.Request) string {
	return strings.TrimSuffix("http://"+r.Host, "/")
}
