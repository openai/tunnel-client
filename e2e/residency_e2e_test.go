package e2e_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/openai/tunnel-client/testsupport/mockproxy"
)

// Exercise real shipped binaries, TLS verification, CONNECT proxy routing,
// client-certificate authentication, and complete MCP request/response delivery.
func TestRuntimeResidencyEndpoints(t *testing.T) {
	runtimeSkipUnixSignals(t)
	t.Parallel()
	subjects := runtimeSubjectsWithBinaries(t, runtimeFullSubject(), runtimeCustomerSubject(), runtimeCloudflaredSubject())
	for _, tc := range []struct {
		name, residency, host string
		mtls                  bool
	}{
		{"eu-tls", "eu", "eu.api.openai.com", false},
		{"eu-mtls", "eu", "mtls-eu.api.openai.com", true},
		{"us-mtls", "us", "mtls-us.api.openai.com", true},
		{"global-mtls", "global", "mtls.api.openai.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runRuntimeScenario(t, subjects, func(t *testing.T) runtimeScenario {
				profile, healthFile, pidFile := writeRuntimeCompatibilityProfile(t)
				var mu sync.Mutex
				paths := map[string]bool{}
				return runtimeScenario{
					name: tc.name, profilePath: profile, healthURLFile: healthFile, pidFile: pidFile,
					options: runtimeRunOptions{
						configure: func(t *testing.T, run *runtimeSubjectRun) {
							certificate, certFile, keyFile := newResidencyTLSIdentity(t, tc.host)
							roots := x509.NewCertPool()
							leaf, err := x509.ParseCertificate(certificate.Certificate[0])
							require.NoError(t, err)
							roots.AddCert(leaf)
							forward := httputil.NewSingleHostReverseProxy(run.fixture.controlPlane.BaseURL())
							bridge := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
								if r.Host != tc.host || r.TLS == nil || r.TLS.ServerName != tc.host {
									http.Error(w, "wrong regional destination", http.StatusBadRequest)
									return
								}
								if tc.mtls && len(r.TLS.PeerCertificates) == 0 {
									http.Error(w, "client identity missing", http.StatusUnauthorized)
									return
								}
								mu.Lock()
								paths[r.URL.Path] = true
								mu.Unlock()
								forward.ServeHTTP(w, r)
							}))
							bridge.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
							if tc.mtls {
								bridge.TLS.ClientAuth = tls.RequireAndVerifyClientCert
								bridge.TLS.ClientCAs = roots
							}
							bridge.StartTLS()
							t.Cleanup(bridge.Close)
							// The proxy rejects all unregistered hosts, including global
							// fallback destinations; no public network is used.
							bridgeURL, err := url.Parse(bridge.URL)
							require.NoError(t, err)
							proxy := mockproxy.New(mockproxy.WithRoute(tc.host+":443", bridgeURL))
							proxy.Start()
							t.Cleanup(proxy.Close)
							run.fixture.proxy = proxy
							data, err := os.ReadFile(profile)
							require.NoError(t, err)
							var document map[string]any
							require.NoError(t, yaml.Unmarshal(data, &document))
							control := document["control_plane"].(map[string]any)
							delete(control, "base_url")
							control["residency"] = "eu"
							control["http_proxy"] = proxy.URL()
							if tc.mtls {
								control["client_cert"] = "file:" + certFile
								control["client_key"] = "file:" + keyFile
								// The flag overrides this environment value and YAML.
								run.env["CONTROL_PLANE_RESIDENCY"] = "jp"
								run.args = append(run.args, "--control-plane.residency="+tc.residency)
							}
							document["ca_bundle"] = certFile
							data, err = yaml.Marshal(document)
							require.NoError(t, err)
							require.NoError(t, os.WriteFile(profile, data, 0o600))
						},
						afterShutdown: func(t *testing.T, run *runtimeSubjectRun) {
							mu.Lock()
							defer mu.Unlock()
							for _, suffix := range []string{"", "/poll", "/response"} {
								require.True(t, paths["/v1/tunnels/"+runtimeArtifactTunnelID+suffix], "missing regional operation %s", suffix)
							}
							records := run.fixture.proxy.Records()
							require.NotEmpty(t, records)
							for _, record := range records {
								require.Equal(t, http.MethodConnect, record.Method)
								require.Equal(t, tc.host+":443", record.Host)
							}
						},
					},
				}
			})
		})
	}
}

func newResidencyTLSIdentity(t *testing.T, host string) (tls.Certificate, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "residency-fixture"},
		DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key")
	require.NoError(t, os.WriteFile(certFile, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, keyPEM, 0o600))
	return certificate, certFile, keyFile
}
