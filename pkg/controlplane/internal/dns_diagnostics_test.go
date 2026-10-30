package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDNSDiagnosticRedactsWrappedErrorsAndPreservesTransportResult(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	dnsErr := &net.DNSError{
		Err:    "refused with secret-error-detail",
		Name:   "secret-gateway.example",
		Server: "[::1]:53",
	}
	wrapped := fmt.Errorf("secret-wrapper: %w", &url.Error{
		Op: "Get", URL: "https://secret-user:secret-password@secret-gateway.example/?token=secret-query", Err: dnsErr,
	})
	response := &http.Response{StatusCode: http.StatusServiceUnavailable}
	rt := newControlPlaneRoundTripper(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return response, wrapped
	}), staticBearerToken("secret-api-key"), "ua", "", nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)))

	// The same wrapper serves metadata, polling and response requests. Its
	// diagnostic must neither depend on the URL nor repeat for each operation.
	for _, path := range []string{"", "/poll", "/response"} {
		req, err := http.NewRequest(http.MethodGet, "https://secret-gateway.example/v1/tunnels/secret-tunnel"+path, nil)
		require.NoError(t, err)
		gotResponse, gotErr := rt.RoundTrip(req)
		require.Same(t, response, gotResponse)
		require.Same(t, wrapped, gotErr)
	}

	require.Equal(t, 1, strings.Count(logs.String(), "\n"))
	assert.NotContains(t, logs.String(), "secret-")
	assert.NotContains(t, logs.String(), "[::1]")
	var record map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &record))
	assert.Equal(t, "control-plane DNS resolution failed", record["msg"])
	assert.Equal(t, "WARN", record["level"])
	assert.Equal(t, "other", record["dns_failure"])
	assert.Equal(t, "loopback", record["dns_server"])
	assert.Contains(t, []string{"enabled", "disabled", "unknown"}, record["cgo_enabled"])
	assert.NotEmpty(t, record["build_goos"])
	assert.NotEmpty(t, record["build_goarch"])
	assert.NotEmpty(t, record["hint"])
	category, status := healthFailure(wrapped)
	assert.Equal(t, "network_error", category)
	assert.Zero(t, status)
	assert.Equal(t, "controlplane client: poll transport failed (network_error)", (&pollingTransportError{cause: wrapped}).Error())
}

func TestDNSDiagnosticIgnoresOtherFailuresWithoutConsumingFirstWarning(t *testing.T) {
	t.Parallel()

	for name, firstError := range map[string]error{
		"success":         nil,
		"non DNS error":   errors.New("secret-error"),
		"request timeout": context.DeadlineExceeded,
		"canceled DNS":    errors.Join(context.Canceled, &net.DNSError{Err: "secret-error"}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			rt := &controlPlaneRoundTripper{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			rt.logDNSFailure(context.Background(), firstError)
			require.Empty(t, logs.String())
			rt.logDNSFailure(context.Background(), &net.DNSError{Err: "secret-error", IsTimeout: true})
			require.Contains(t, logs.String(), `"dns_failure":"timeout"`)
			assert.NotContains(t, logs.String(), "secret-error")
		})
	}
}

func TestDNSDiagnosticOnceIsSafeForConcurrentRequests(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	rt := &controlPlaneRoundTripper{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	var requests sync.WaitGroup
	for range 32 {
		requests.Go(func() {
			rt.logDNSFailure(context.Background(), &net.DNSError{Err: "secret-error", IsTemporary: true})
		})
	}
	requests.Wait()
	require.Equal(t, 1, strings.Count(logs.String(), "\n"))
	assert.Contains(t, logs.String(), `"dns_failure":"temporary"`)
}

func TestDNSEnvironmentInspection(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		goos        string
		cgo         string
		propertyErr error
		configErr   error
		want        dnsEnvironment
	}{
		{"linux binary on Android", "linux", "0", nil, fs.ErrNotExist, dnsEnvironment{"linux", "arm64", "disabled", "missing", true}},
		{"native Android", "android", "1", fs.ErrPermission, fs.ErrNotExist, dnsEnvironment{"android", "arm64", "enabled", "missing", true}},
		{"ordinary Linux", "linux", "1", fs.ErrNotExist, nil, dnsEnvironment{"linux", "arm64", "enabled", "present", false}},
		{"missing config is not Android", "linux", "0", fs.ErrNotExist, fs.ErrNotExist, dnsEnvironment{"linux", "arm64", "disabled", "missing", false}},
		{"inaccessible config", "linux", "", fs.ErrPermission, fs.ErrPermission, dnsEnvironment{"linux", "arm64", "unknown", "unavailable", false}},
		{"other platform", "darwin", "1", nil, nil, dnsEnvironment{"darwin", "arm64", "enabled", "not_applicable", false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			build := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "CGO_ENABLED", Value: tc.cgo}}}
			stat := func(path string) (fs.FileInfo, error) {
				switch path {
				case "/dev/__properties__":
					return nil, tc.propertyErr
				case "/etc/resolv.conf":
					return nil, tc.configErr
				default:
					t.Fatalf("unexpected filesystem probe: %s", path)
					return nil, fs.ErrNotExist
				}
			}
			assert.Equal(t, tc.want, inspectDNSEnvironment(tc.goos, "arm64", build, stat))
		})
	}
}

func TestDNSDiagnosticHintsDoNotClaimActiveResolver(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		environment dnsEnvironment
		wantHint    string
	}{
		{"Linux on Android", dnsEnvironment{"linux", "arm64", "disabled", "missing", true}, "use the experimental Android/arm64 runtime built with cgo"},
		{"Android without cgo", dnsEnvironment{"android", "arm64", "disabled", "missing", true}, "use the experimental Android/arm64 runtime built with cgo"},
		{"native Android", dnsEnvironment{"android", "arm64", "enabled", "missing", true}, "Check Android's active network"},
		{"unknown environment", dnsEnvironment{"linux", "arm64", "disabled", "missing", false}, "otherwise configure the system resolver"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dnsErr := &net.DNSError{Name: "secret-host", Server: "[::1]:53", Err: "secret-error", IsNotFound: true}
			attrs := dnsDiagnosticAttrs(dnsErr, tc.environment)
			values := make(map[string]string)
			for _, attr := range attrs {
				values[attr.Key] = attr.Value.String()
			}
			assert.Equal(t, "not_found", values["dns_failure"])
			assert.Contains(t, values["hint"], tc.wantHint)
			assert.NotContains(t, values, "resolver")
			assert.NotContains(t, fmt.Sprint(values), "secret-")
		})
	}
}
