package tlsconfig

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrustReportWithBundle(t *testing.T) {
	bundlePath := writeTestBundle(t)
	bundle, err := LoadBundle(bundlePath)
	if err != nil {
		t.Fatalf("LoadBundle returned error: %v", err)
	}
	report := BuildTrustReport(bundle)
	if report.ExtraBundle == nil {
		t.Fatalf("expected extra bundle summary")
	}
	if report.ExtraBundle.Path != bundlePath {
		t.Fatalf("unexpected bundle path: %s", report.ExtraBundle.Path)
	}
	if report.ExtraBundle.CertCount != 1 {
		t.Fatalf("expected 1 cert, got %d", report.ExtraBundle.CertCount)
	}
	if len(report.ExtraBundle.Certificates) != 1 {
		t.Fatalf("expected 1 certificate metadata")
	}
	if report.ExtraBundle.Certificates[0].ParseStatus != "ok" {
		t.Fatalf("expected parse status ok")
	}
}

func TestTrustReportSystemTrust(t *testing.T) {
	report := BuildTrustReport(nil)
	if report.SystemTrust.Source == "" {
		t.Fatalf("expected system trust source")
	}
}

func writeTestBundle(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "test-ca",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if certPEM == nil {
		t.Fatalf("failed to encode cert")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.pem")
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return path
}
