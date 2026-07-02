package transport

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/tlsconfig"
)

func TestApplyClientCertificateConfiguresExplicitClientCertificateCallback(t *testing.T) {
	t.Parallel()

	certificate := tls.Certificate{
		Certificate: [][]byte{{1, 2, 3}},
	}
	base := http.DefaultTransport.(*http.Transport).Clone()

	roundTripper, err := ApplyClientCertificate(base, &tlsconfig.ClientCertificate{
		CertPath:    "/tmp/client.pem",
		KeyPath:     "/tmp/client-key.pem",
		Certificate: certificate,
	})
	require.NoError(t, err)

	transport, ok := roundTripper.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	require.Len(t, transport.TLSClientConfig.Certificates, 1)
	require.NotNil(t, transport.TLSClientConfig.GetClientCertificate)

	selected, err := transport.TLSClientConfig.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.NoError(t, err)
	require.Equal(t, certificate.Certificate, selected.Certificate)
}

func TestApplyUnixSocketPathDialsUnixListener(t *testing.T) {
	t.Parallel()

	socketFile, err := os.CreateTemp("/tmp", "transport-*.sock")
	require.NoError(t, err)
	socketPath := socketFile.Name()
	require.NoError(t, socketFile.Close())
	require.NoError(t, os.Remove(socketPath))

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/healthz", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	roundTripper, err := ApplyUnixSocketPath(http.DefaultTransport.(*http.Transport).Clone(), socketPath)
	require.NoError(t, err)

	client := &http.Client{Transport: roundTripper}
	response, err := client.Get("http://localhost/healthz")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, response.Body.Close())
	}()
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.StatusCode)
}
