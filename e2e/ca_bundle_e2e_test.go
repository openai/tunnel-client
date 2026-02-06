package e2e_test

import (
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.openai.org/api/tunnel-client/pkg/config"
	tctransport "go.openai.org/api/tunnel-client/pkg/transport"
)

//go:embed testdata/server_cert.pem testdata/server_key.pem testdata/ca_bundle.pem
var tlsFixtures embed.FS

func TestCABundleControlsTLSHandshake(t *testing.T) {
	certPEM := mustReadFixture(t, "testdata/server_cert.pem")
	keyPEM := mustReadFixture(t, "testdata/server_key.pem")
	caPEM := mustReadFixture(t, "testdata/ca_bundle.pem")

	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load server key pair: %v", err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}}
	server.StartTLS()
	defer server.Close()

	clientNoBundle := newHTTPClient(t, "")
	_, err = clientNoBundle.Get(server.URL)
	if err == nil {
		t.Fatalf("expected TLS handshake to fail without ca-bundle")
	}
	if !strings.Contains(err.Error(), "certificate signed by unknown authority") {
		t.Fatalf("expected unknown authority error, got %v", err)
	}

	caPath := writeTempFile(t, "ca-bundle.pem", caPEM)
	clientWithBundle := newHTTPClient(t, caPath)
	resp, err := clientWithBundle.Get(server.URL)
	if err != nil {
		t.Fatalf("expected TLS handshake to succeed with ca-bundle: %v", err)
	}
	_ = resp.Body.Close()
}

func TestCABundleParsesMultipleCertificates(t *testing.T) {
	caPEM := mustReadFixture(t, "testdata/ca_bundle.pem")
	certs := mustParsePEMCerts(t, caPEM)
	if len(certs) < 2 {
		t.Fatalf("expected multiple certificates in CA bundle, found %d", len(certs))
	}

	caPath := writeTempFile(t, "ca-bundle.pem", caPEM)
	cfg := loadConfigWithBundle(t, caPath)
	if cfg.TLS == nil || cfg.TLS.RootCAs == nil {
		t.Fatal("expected TLS bundle with RootCAs")
	}
	subjects := cfg.TLS.RootCAs.Subjects()
	for _, cert := range certs {
		if !subjectsContain(subjects, cert.RawSubject) {
			t.Fatalf("expected bundle to include certificate subject %q", cert.Subject.String())
		}
	}
}

func newHTTPClient(t *testing.T, bundlePath string) *http.Client {
	t.Helper()
	cfg := loadConfigWithBundle(t, bundlePath)
	transport, err := tctransport.CloneDefaultWithBundle(cfg.TLS)
	if err != nil {
		t.Fatalf("build transport: %v", err)
	}
	return &http.Client{Transport: transport, Timeout: time.Second}
}

func loadConfigWithBundle(t *testing.T, bundlePath string) *config.Config {
	t.Helper()
	args := []string{
		"--control-plane.tunnel-id", "tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--mcp.server-url", "https://mcp.example",
	}
	if bundlePath != "" {
		args = append(args, "--ca-bundle", bundlePath)
	}
	cfg, err := config.Load(args, func(key string) (string, bool) {
		if key == "CONTROL_PLANE_API_KEY" {
			return "control-key", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := tlsFixtures.ReadFile(name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func mustParsePEMCerts(t *testing.T, data []byte) []*x509.Certificate {
	t.Helper()
	var certs []*x509.Certificate
	for len(data) > 0 {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatalf("parse certificate: %v", err)
			}
			certs = append(certs, cert)
		}
		data = rest
	}
	if len(certs) == 0 {
		t.Fatal("no certificates parsed from bundle")
	}
	return certs
}

func subjectsContain(subjects [][]byte, subject []byte) bool {
	for _, existing := range subjects {
		if string(existing) == string(subject) {
			return true
		}
	}
	return false
}
