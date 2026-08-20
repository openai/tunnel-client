package healthurl

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseUnixBaseURL(t *testing.T) {
	target, err := Parse(BuildUnixBaseURL("/tmp/tunnel-client-health.sock"))
	require.NoError(t, err)
	require.Equal(t, "http://localhost", target.RequestBaseURL)
	require.Equal(t, "/tmp/tunnel-client-health.sock", target.UnixSocketPath)
	require.Equal(t, target.BaseURL+"/healthz", target.URL("/healthz"))
	require.Equal(t, "http://localhost/healthz", target.RequestURL("/healthz"))
}

func TestParseTCPBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	target, err := Parse(server.URL + "/healthz")
	require.NoError(t, err)
	client, err := target.HTTPClient(time.Second)
	require.NoError(t, err)

	resp, err := client.Get(target.RequestURL("/healthz"))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}
