package mcpclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/mcpclient/internal"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestChannelTransportFactoryAppliesProxy(t *testing.T) {
	t.Parallel()

	targetCalled := make(chan struct{}, 1)
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalled <- struct{}{}
		http.Error(w, "unexpected direct request", http.StatusBadGateway)
	}))
	t.Cleanup(targetServer.Close)

	proxyCalled := make(chan struct{}, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalled <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxyServer.Close)

	proxyURL := mustParseURLFactoryTest(t, proxyServer.URL)
	binding := config.MCPChannelBinding{
		Channel:         types.DefaultChannel,
		TransportKind:   config.MCPTransportHTTPStreamable,
		ServerURL:       mustParseURLFactoryTest(t, targetServer.URL),
		HTTPProxy:       proxyURL,
		HTTPProxySource: config.ProxySource("mcp.server-url"),
	}
	cfg := &config.MCPConfig{
		ChannelBindings: []config.MCPChannelBinding{binding},
	}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:        cfg,
		Logging:       &config.LoggingConfig{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	client, err := factory.HTTPClientForBinding(binding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding failed: %v", err)
	}
	resp, err := client.Get(targetServer.URL)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case <-proxyCalled:
	default:
		t.Fatalf("expected proxy to receive request")
	}
	select {
	case <-targetCalled:
		t.Fatalf("expected target not to be called directly")
	default:
	}
}

func TestChannelTransportFactoryDialsUnixSocket(t *testing.T) {
	t.Parallel()

	socketFile, err := os.CreateTemp("/tmp", "mcp-client-*.sock")
	if err != nil {
		t.Fatalf("create unix socket temp file: %v", err)
	}
	socketPath := socketFile.Name()
	if err := socketFile.Close(); err != nil {
		t.Fatalf("close unix socket temp file: %v", err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatalf("remove unix socket temp file: %v", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on unix socket: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(socketPath)
	})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("unexpected unix socket request path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	binding := config.MCPChannelBinding{
		Channel:        types.DefaultChannel,
		TransportKind:  config.MCPTransportHTTPStreamable,
		ServerURL:      mustParseURLFactoryTest(t, "http://localhost/mcp"),
		UnixSocketPath: socketPath,
	}
	cfg := &config.MCPConfig{ChannelBindings: []config.MCPChannelBinding{binding}}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:        cfg,
		Logging:       &config.LoggingConfig{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	client, err := factory.HTTPClientForBinding(binding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding failed: %v", err)
	}
	resp, err := client.Get(binding.ServerURL.String())
	if err != nil {
		t.Fatalf("unix socket request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected unix socket status %d", resp.StatusCode)
	}
}

func TestChannelTransportFactoryScopesStaticAuthorizationPerBinding(t *testing.T) {
	t.Parallel()

	serverAAuth := make(chan string, 2)
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverAAuth <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(serverA.Close)

	serverBAuth := make(chan string, 2)
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverBAuth <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(serverB.Close)

	defaultBinding := config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     mustParseURLFactoryTest(t, serverA.URL+"/mcp"),
	}
	connectorBinding := config.MCPChannelBinding{
		Channel:       types.Channel("connector-b"),
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     mustParseURLFactoryTest(t, serverB.URL+"/mcp"),
	}
	cfg := &config.MCPConfig{
		ChannelBindings: []config.MCPChannelBinding{defaultBinding, connectorBinding},
		ExtraHeaders:    map[string]string{"Authorization": "Bearer static-mcp-token"},
	}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:        cfg,
		Logging:       &config.LoggingConfig{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	defaultClient, err := factory.HTTPClientForBinding(defaultBinding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding(default) failed: %v", err)
	}
	resp, err := defaultClient.Get(serverA.URL + "/mcp")
	if err != nil {
		t.Fatalf("default client request to default server failed: %v", err)
	}
	_ = resp.Body.Close()
	requireHeaderValue(t, serverAAuth, "Bearer static-mcp-token")

	resp, err = defaultClient.Get(serverB.URL + "/mcp")
	if err != nil {
		t.Fatalf("default client request to connector server failed: %v", err)
	}
	_ = resp.Body.Close()
	requireHeaderValue(t, serverBAuth, "")

	connectorClient, err := factory.HTTPClientForBinding(connectorBinding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding(connector) failed: %v", err)
	}
	resp, err = connectorClient.Get(serverB.URL + "/mcp")
	if err != nil {
		t.Fatalf("connector client request to connector server failed: %v", err)
	}
	_ = resp.Body.Close()
	requireHeaderValue(t, serverBAuth, "Bearer static-mcp-token")
}

func TestChannelTransportFactoryConnectorAuthorizationOverridesStaticHeader(t *testing.T) {
	t.Parallel()

	seenHeaders := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders <- r.Header.Clone()
		w.Header().Set(HeaderSessionID, "session-from-mcp")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	binding := config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     mustParseURLFactoryTest(t, server.URL+"/mcp"),
	}
	cfg := &config.MCPConfig{
		ChannelBindings:       []config.MCPChannelBinding{binding},
		ExtraHeaders:          map[string]string{"Authorization": "Bearer static-mcp-token"},
		DiscoveryExtraHeaders: map[string]string{"X-Discovery-Auth": "discovery-only"},
	}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:        cfg,
		Logging:       &config.LoggingConfig{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	client, err := factory.HTTPClientForBinding(binding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding failed: %v", err)
	}

	ctx, carrier, err := internal.ContextWithHeaders(context.Background(), http.Header{
		"Authorization": {"Bearer connector-user-token"},
	})
	if err != nil {
		t.Fatalf("ContextWithHeaders failed: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("connector-authorized request failed: %v", err)
	}
	_ = resp.Body.Close()

	gotHeaders := <-seenHeaders
	if got := gotHeaders.Get("Authorization"); got != "Bearer connector-user-token" {
		t.Fatalf("Authorization header = %q, want connector token to override static token", got)
	}
	if got := gotHeaders.Get("X-Discovery-Auth"); got != "" {
		t.Fatalf("runtime request unexpectedly received discovery header %q", got)
	}
	status, responseHeaders := carrier.ResponseStatusAndHeaders()
	if status != http.StatusAccepted {
		t.Fatalf("captured response status = %d, want %d", status, http.StatusAccepted)
	}
	if got := responseHeaders.Get(HeaderSessionID); got != "session-from-mcp" {
		t.Fatalf("captured %s = %q, want session-from-mcp", HeaderSessionID, got)
	}
}

func requireHeaderValue(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("Authorization header = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for request with Authorization header %q", want)
	}
}

func TestChannelTransportFactoryMTLS(t *testing.T) {
	t.Parallel()

	material := newMTLSTestMaterial(t)

	hit := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{material.serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    material.caPool,
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	bundle := &tlsconfig.Bundle{RootCAs: material.caPool}

	t.Run("request without client certificate fails", func(t *testing.T) {
		binding := config.MCPChannelBinding{
			Channel:       types.DefaultChannel,
			TransportKind: config.MCPTransportHTTPStreamable,
			ServerURL:     mustParseURLFactoryTest(t, server.URL),
		}
		cfg := &config.MCPConfig{ChannelBindings: []config.MCPChannelBinding{binding}}

		factory, err := newChannelTransportFactory(channelTransportFactoryParams{
			Config:        cfg,
			Logging:       &config.LoggingConfig{},
			Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			MeterProvider: sdkmetric.NewMeterProvider(),
			TLSBundle:     bundle,
		})
		if err != nil {
			t.Fatalf("newChannelTransportFactory failed: %v", err)
		}

		client, err := factory.HTTPClientForBinding(binding)
		if err != nil {
			t.Fatalf("HTTPClientForBinding failed: %v", err)
		}
		resp, err := client.Get(server.URL)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("expected request to fail without client certificate")
		}
	})

	t.Run("request with client certificate succeeds", func(t *testing.T) {
		binding := config.MCPChannelBinding{
			Channel:           types.DefaultChannel,
			TransportKind:     config.MCPTransportHTTPStreamable,
			ServerURL:         mustParseURLFactoryTest(t, server.URL),
			ClientCertificate: material.clientCertificate,
			HTTPProxy:         nil,
			HTTPProxySource:   config.ProxySourceNone,
		}
		cfg := &config.MCPConfig{ChannelBindings: []config.MCPChannelBinding{binding}}

		factory, err := newChannelTransportFactory(channelTransportFactoryParams{
			Config:        cfg,
			Logging:       &config.LoggingConfig{},
			Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			MeterProvider: sdkmetric.NewMeterProvider(),
			TLSBundle:     bundle,
		})
		if err != nil {
			t.Fatalf("newChannelTransportFactory failed: %v", err)
		}

		client, err := factory.HTTPClientForBinding(binding)
		if err != nil {
			t.Fatalf("HTTPClientForBinding failed: %v", err)
		}
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		select {
		case <-hit:
		default:
			t.Fatalf("expected server handler to be called")
		}
	})
}

type blockingTransportProvider struct {
	started chan struct{}
	release chan struct{}
	count   atomic.Int32
}

func (p *blockingTransportProvider) Kind() config.MCPTransportKind {
	return config.MCPTransportHTTPStreamable
}

func (p *blockingTransportProvider) Build(TransportBuildParams) (mcp.Transport, error) {
	p.count.Add(1)
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-p.release
	return &stubTransport{}, nil
}

func TestChannelTransportFactoryBuildSingleInstanceUnderConcurrency(t *testing.T) {
	t.Parallel()

	binding := config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     mustParseURLFactoryTest(t, "https://example.com"),
	}
	cfg := &config.MCPConfig{
		ChannelBindings: []config.MCPChannelBinding{binding},
	}
	provider := &blockingTransportProvider{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:             cfg,
		Logging:            &config.LoggingConfig{},
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider:      sdkmetric.NewMeterProvider(),
		TransportProviders: []TransportProvider{provider},
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	const callers = 8
	results := make([]mcp.Transport, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		index := i
		go func() {
			defer wg.Done()
			transport, err := factory.Build(binding)
			if err != nil {
				t.Errorf("Build failed: %v", err)
				return
			}
			results[index] = transport
		}()
	}

	<-provider.started
	close(provider.release)
	wg.Wait()

	if got := provider.count.Load(); got != 1 {
		t.Fatalf("expected provider to build once, got %d", got)
	}
	if results[0] == nil {
		t.Fatal("expected transport result, got nil")
	}
	if _, ok := results[0].(*stubTransport); !ok {
		t.Fatalf("expected *stubTransport, got %T", results[0])
	}
	for i := 1; i < callers; i++ {
		if results[i] == nil {
			t.Fatalf("expected transport result at %d, got nil", i)
		}
		if results[i] != results[0] {
			t.Fatalf("expected shared transport instance, index %d differed", i)
		}
	}
}

type mtlsTestMaterial struct {
	caPool            *x509.CertPool
	serverCertificate tls.Certificate
	clientCertificate *tlsconfig.ClientCertificate
}

func newMTLSTestMaterial(t *testing.T) mtlsTestMaterial {
	t.Helper()

	caCert, caKey, caPEM := generateCA(t)
	serverCert := generateSignedLeaf(t, caCert, caKey, "mcp-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert, clientCertPath, clientKeyPath := generateSignedClientCertificate(t, caCert, caKey)

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(caPEM); !ok {
		t.Fatalf("failed to append CA cert to pool")
	}

	return mtlsTestMaterial{
		caPool:            pool,
		serverCertificate: serverCert,
		clientCertificate: &tlsconfig.ClientCertificate{
			CertPath:    clientCertPath,
			KeyPath:     clientKeyPath,
			Certificate: clientCert,
		},
	}
}

func generateCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(11),
		Subject: pkix.Name{
			CommonName: "test-ca",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(2 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if caPEM == nil {
		t.Fatalf("encode CA certificate PEM")
	}
	return caCert, caKey, caPEM
}

func generateSignedLeaf(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey, commonName string, extKeyUsage []x509.ExtKeyUsage) tls.Certificate {
	t.Helper()

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(12),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(2 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  extKeyUsage,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	if leafPEM == nil {
		t.Fatalf("encode leaf certificate PEM")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	if keyPEM == nil {
		t.Fatalf("encode leaf key PEM")
	}
	pair, err := tls.X509KeyPair(leafPEM, keyPEM)
	if err != nil {
		t.Fatalf("load leaf key pair: %v", err)
	}
	return pair
}

func generateSignedClientCertificate(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey) (tls.Certificate, string, string) {
	t.Helper()

	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(13),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(2 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client certificate: %v", err)
	}
	clientPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	if clientPEM == nil {
		t.Fatalf("encode client certificate PEM")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientKey)})
	if keyPEM == nil {
		t.Fatalf("encode client key PEM")
	}
	clientPair, err := tls.X509KeyPair(clientPEM, keyPEM)
	if err != nil {
		t.Fatalf("load client key pair: %v", err)
	}
	dir := t.TempDir()
	clientCertPath := filepath.Join(dir, "client.crt")
	clientKeyPath := filepath.Join(dir, "client.key")
	if err := os.WriteFile(clientCertPath, clientPEM, 0o600); err != nil {
		t.Fatalf("write client cert: %v", err)
	}
	if err := os.WriteFile(clientKeyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}
	return clientPair, clientCertPath, clientKeyPath
}

func mustParseURLFactoryTest(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed
}
