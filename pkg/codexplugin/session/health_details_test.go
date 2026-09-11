package session

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/healthurl"
)

func TestDiscoverHealthDetailsCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		supported bool
	}{
		{"new", 200, `{"schema_version":1,"component":"mcp","status":"unknown","future_field":true}`, true},
		{"old", 404, "404 page not found", false},
		{"fallback-html", 200, "<html>legacy UI</html>", false},
		{"future-schema", 200, `{"schema_version":2,"component":"mcp"}`, false},
		{"different-component", 200, `{"schema_version":1,"component":"oauth"}`, false},
		{"oversized", 200, `{"schema_version":1,"component":"mcp","padding":"` + strings.Repeat("x", healthstate.MaxComponentBytes) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				require.Equal(t, "/health/mcp", r.URL.Path)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			for _, suffix := range []string{"", "/healthz", "/readyz"} {
				details, mcp := DiscoverHealthDetails(server.URL + suffix)
				if tc.supported {
					require.Equal(t, server.URL+"/health?details=true", details)
					require.Equal(t, server.URL+"/health/mcp", mcp)
				} else {
					require.Empty(t, details)
					require.Empty(t, mcp)
				}
			}
			require.Equal(t, 3, requests)
		})
	}
}

func TestDiscoverHealthDetailsUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix health socket is unavailable on Windows")
	}
	// Keep the socket path short even when the test runner has a long TMPDIR.
	dir, err := os.MkdirTemp("/tmp", "health-details-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	socket := filepath.Join(dir, "health.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/health/mcp", r.URL.Path)
		_, _ = w.Write([]byte(`{"schema_version":1,"component":"mcp"}`))
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { require.NoError(t, server.Close()); <-done }()
	base := healthurl.BuildUnixBaseURL(socket)
	details, mcp := DiscoverHealthDetails(base + "/healthz")
	require.Equal(t, base+"/health?details=true", details)
	require.Equal(t, base+"/health/mcp", mcp)
}

func TestDiscoverHealthDetailsDoesNotFollowRedirectsOrRemoteURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/mcp" {
			t.Error("followed diagnostic redirect")
		}
		w.Header().Set("Location", "/unexpected")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	for _, raw := range []string{server.URL, "http://192.0.2.1", "https://remote.example.invalid", "http://user:secret@localhost:1"} {
		details, mcp := DiscoverHealthDetails(raw)
		require.Empty(t, details)
		require.Empty(t, mcp)
	}
}
