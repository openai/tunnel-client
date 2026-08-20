package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
		"--health.listen-addr", "127.0.0.1:0",
	)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "CHECK config_source")
	require.Contains(t, stdout, "CHECK tunnel_id")
	require.Contains(t, stdout, "CHECK mcp_server_reachable")
	require.Contains(t, stdout, "CHECK oauth_metadata")
	require.Contains(t, stdout, "CHECK ui")
	require.Contains(t, stdout, "RESULT ok")
	require.Contains(t, stdout, "NEXT   tunnel-client run")
	require.Contains(t, stdout, canonicalTunnelsManagementURL)
	require.Contains(t, stdout, canonicalRuntimeAPIKeysURL)
	require.Contains(t, stdout, canonicalAdminAPIKeysURL)
	require.Contains(t, stdout, canonicalChatGPTConnectorSettingsURL)
}

func TestDoctorOAuthMetadataCheckCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		pathStatus           int
		rootStatus           int
		pathBody             string
		emptyErrorBodies     bool
		advertiseMetadataURL bool
		wantStatus           doctorStatus
		wantSummaryPart      string
		wantMetadataPaths    []string
	}{
		{
			name:              "AllNotFoundIsOptional",
			pathStatus:        http.StatusNotFound,
			rootStatus:        http.StatusNotFound,
			emptyErrorBodies:  true,
			wantStatus:        doctorStatusPass,
			wantSummaryPart:   "all candidates returned HTTP 404",
			wantMetadataPaths: []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"},
		},
		{
			name:              "RootCandidateSucceeds",
			pathStatus:        http.StatusNotFound,
			rootStatus:        http.StatusOK,
			wantStatus:        doctorStatusPass,
			wantSummaryPart:   "HTTP 200 from",
			wantMetadataPaths: []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"},
		},
		{
			name:              "UnexpectedRootStatusFails",
			pathStatus:        http.StatusNotFound,
			rootStatus:        http.StatusInternalServerError,
			wantStatus:        doctorStatusFail,
			wantSummaryPart:   "invalid metadata",
			wantMetadataPaths: []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"},
		},
		{
			name:              "ServerErrorFallsBackToRootCandidate",
			pathStatus:        http.StatusInternalServerError,
			rootStatus:        http.StatusOK,
			wantStatus:        doctorStatusPass,
			wantSummaryPart:   "HTTP 200 from",
			wantMetadataPaths: []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"},
		},
		{
			name:              "MalformedSuccessFails",
			pathStatus:        http.StatusOK,
			rootStatus:        http.StatusOK,
			pathBody:          "not-json",
			wantStatus:        doctorStatusFail,
			wantSummaryPart:   "invalid metadata",
			wantMetadataPaths: []string{"/.well-known/oauth-protected-resource/mcp"},
		},
		{
			name:                 "AdvertisedMetadataNotFoundIsNotOptional",
			pathStatus:           http.StatusNotFound,
			rootStatus:           http.StatusNotFound,
			advertiseMetadataURL: true,
			wantStatus:           doctorStatusFail,
			wantSummaryPart:      "oauth discovery",
			wantMetadataPaths: []string{
				"/advertised-oauth-metadata",
				"/.well-known/oauth-protected-resource/mcp",
				"/.well-known/oauth-protected-resource",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			requestedMetadataPaths := make([]string, 0, 3)
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/.well-known/") || r.URL.Path == "/advertised-oauth-metadata" {
					mu.Lock()
					requestedMetadataPaths = append(requestedMetadataPaths, r.URL.Path)
					mu.Unlock()
				}
				switch r.URL.Path {
				case "/mcp":
					if tt.advertiseMetadataURL {
						w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/advertised-oauth-metadata"`)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					w.WriteHeader(http.StatusOK)
				case "/advertised-oauth-metadata":
					http.NotFound(w, r)
				case "/.well-known/oauth-protected-resource/mcp":
					w.WriteHeader(tt.pathStatus)
					if tt.pathBody != "" {
						_, _ = w.Write([]byte(tt.pathBody))
					} else if tt.pathStatus == http.StatusOK {
						_, _ = w.Write([]byte(`{"resource":"` + server.URL + `/mcp"}`))
					} else if !tt.emptyErrorBodies {
						_, _ = w.Write([]byte(http.StatusText(tt.pathStatus)))
					}
				case "/.well-known/oauth-protected-resource":
					w.WriteHeader(tt.rootStatus)
					if tt.rootStatus == http.StatusOK {
						_, _ = w.Write([]byte(`{"resource":"` + server.URL + `/mcp"}`))
					} else if !tt.emptyErrorBodies {
						_, _ = w.Write([]byte(http.StatusText(tt.rootStatus)))
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			serverURL, err := url.Parse(server.URL + "/mcp")
			require.NoError(t, err)
			check := doctorOAuthMetadataCheck(serverURL)

			require.Equal(t, tt.wantStatus, check.Status)
			require.Contains(t, check.Summary, tt.wantSummaryPart)
			mu.Lock()
			gotPaths := append([]string(nil), requestedMetadataPaths...)
			mu.Unlock()
			require.Equal(t, tt.wantMetadataPaths, gotPaths)
		})
	}
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
	require.Contains(t, stdout, canonicalRuntimeAPIKeysURL)
	require.Contains(t, stdout, canonicalAdminAPIKeysURL)
}

func TestDoctorMissingTunnelIDExplainIncludesConnectorRuntimeNote(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--mcp.command", testExecutableCommand(),
		"--explain",
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Equal(t, 2, exitCode(err))
	require.Contains(t, stdout, "FAILED_CHECKS tunnel_id")
	require.Contains(t, stdout, canonicalTunnelsManagementURL)
	require.Contains(t, stdout, canonicalAdminAPIKeysURL)
	require.Contains(t, stdout, canonicalChatGPTConnectorSettingsURL)
	require.Contains(t, stdout, "tunnel-client init --sample sample_mcp_with_dcr --profile sample_mcp_with_dcr")
	require.Contains(t, stdout, "Create or verify the connector in https://chatgpt.com/#settings/Connectors only while `tunnel-client run` is running.")
	require.Contains(t, stdout, "Keep the daemon up for connector discovery and every MCP call from ChatGPT.")
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
		"--health.listen-addr", "127.0.0.1:0",
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
		HealthListenAddr: "127.0.0.1:0",
		MCPCommand:       testExecutableCommand(),
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

func TestDoctorReadsProfileFile(t *testing.T) {
	t.Parallel()

	profileDir := t.TempDir()
	path := filepath.Join(profileDir, "sample-profile-file.yaml")
	data, err := generateProfileSample("sample_mcp_with_dcr", sampleProfileRequest{
		TunnelID:         "tunnel_0123456789abcdef0123456789abcdef",
		APIKeyRef:        "env:CONTROL_PLANE_API_KEY",
		HealthListenAddr: "127.0.0.1:0",
		MCPCommand:       testExecutableCommand(),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor", "--profile-file", path)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "profile file: "+path)
	require.Contains(t, stdout, "NEXT   tunnel-client run --profile-file "+path)
}

func TestDoctorUsesEphemeralUIHintForPortZero(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.command", testExecutableCommand(),
		"--health.listen-addr", "127.0.0.1:0",
	)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "CHECK mcp_command_executable")
	require.Contains(t, stdout, "CHECK health_listener")
	require.Contains(t, stdout, "ephemeral bind ok")
	require.Contains(t, stdout, "CHECK ui")
	require.Contains(t, stdout, "inspect startup summary or HEALTH_URL_FILE")
}

func TestDoctorUsesUnixSocketHealthListener(t *testing.T) {
	t.Parallel()

	socketPath := shortSocketPath(t, "tunnel-client-doctor-health-*.sock")

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.command", testExecutableCommand(),
		"--health.unix-socket", socketPath,
	)

	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "CHECK health_listener")
	require.Contains(t, stdout, "will bind unix socket "+socketPath)
	require.Contains(t, stdout, "CHECK ui")
	require.Contains(t, stdout, "inspect startup summary or HEALTH_URL_FILE for the Unix-socket admin URL")
	_, statErr := os.Stat(socketPath)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestDoctorFailsWhenStdioExecutableMissing(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing-mcp-command")

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.command", missing,
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Equal(t, 2, exitCode(err))
	require.Contains(t, stdout, "CHECK mcp_command_executable")
	require.Contains(t, stdout, "stdio MCP executable")
	require.Contains(t, stdout, "FAILED_CHECKS mcp_command_executable")
}

func TestDoctorSTDIO0305FailsWhenDirectScriptLacksExecuteBit(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("execute-bit preflight is Unix-specific")
	}

	script := filepath.Join(t.TempDir(), "stdio_server.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"), 0o600))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.command", script,
		"--explain",
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Equal(t, 2, exitCode(err))
	require.Contains(t, stdout, "CHECK mcp_command_executable")
	require.Contains(t, stdout, "chmod +x")
	require.Contains(t, stdout, "FAILED_CHECKS mcp_command_executable")
}

func TestDoctorSTDIO0305FailsWhenDirectScriptInterpreterIsMissing(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shebang preflight is Unix-specific")
	}

	dir := t.TempDir()
	missingInterpreter := filepath.Join(dir, "missing-interpreter")
	script := filepath.Join(dir, "stdio_server.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!"+missingInterpreter+"\n"), 0o700))

	stdout, stderr, err := executeCommand(t, map[string]string{
		"HOME":                  t.TempDir(),
		"CONTROL_PLANE_API_KEY": "test-api-key",
	}, "doctor",
		"--control-plane.tunnel-id", "tunnel_0123456789abcdef0123456789abcdef",
		"--mcp.command", script,
		"--explain",
	)

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Equal(t, 2, exitCode(err))
	require.Contains(t, stdout, "CHECK mcp_command_executable")
	require.Contains(t, stdout, "uses an unavailable interpreter")
	require.Contains(t, stdout, "update the shebang")
	require.Contains(t, stdout, "FAILED_CHECKS mcp_command_executable")
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

func testExecutableCommand() string {
	return os.Args[0]
}
