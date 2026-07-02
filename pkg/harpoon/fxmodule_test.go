package harpoon

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/healthurl"
)

func TestBuildHarpoonHTTPEndpointSupportsHealthTransports(t *testing.T) {
	t.Parallel()

	t.Run("HTTP", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/harpoon/mcp", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(server.Close)

		endpoint := buildHarpoonHTTPEndpoint(
			&config.HealthConfig{ListenAddr: server.Listener.Addr().String()},
			fakeHealthService{addr: server.Listener.Addr().String()},
			time.Second,
		)
		require.Equal(t, server.URL+"/harpoon/mcp", endpoint)

		resp, err := server.Client().Get(endpoint)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("UnixSocket", func(t *testing.T) {
		t.Parallel()

		socketPath := shortSocketPath(t, "harpoon-health-*.sock")
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Skipf("unix socket unavailable: %v", err)
		}

		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/harpoon/mcp", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		})}
		t.Cleanup(func() {
			require.NoError(t, server.Close())
		})
		go func() {
			_ = server.Serve(listener)
		}()

		endpoint := buildHarpoonHTTPEndpoint(
			&config.HealthConfig{UnixSocket: socketPath},
			fakeHealthService{addr: socketPath},
			time.Second,
		)
		require.Equal(t, healthurl.BuildUnixBaseURL(socketPath)+"/harpoon/mcp", endpoint)

		target, err := healthurl.Parse(strings.TrimSuffix(endpoint, "/harpoon/mcp"))
		require.NoError(t, err)
		client, err := target.HTTPClient(time.Second)
		require.NoError(t, err)
		resp, err := client.Get(target.RequestURL("/harpoon/mcp"))
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	})
}

type fakeHealthService struct {
	addr string
}

func (s fakeHealthService) Addr(time.Duration) (string, error) {
	return s.addr, nil
}

func shortSocketPath(t *testing.T, pattern string) string {
	t.Helper()

	file, err := os.CreateTemp("", pattern)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	socketPath := file.Name()
	require.NoError(t, os.Remove(socketPath))
	require.Less(t, len(filepath.Clean(socketPath)), 104)
	return socketPath
}
