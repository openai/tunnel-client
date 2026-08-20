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

func TestLoadClientCertificate(t *testing.T) {
	t.Run("empty returns nil", func(t *testing.T) {
		got, err := LoadClientCertificate("", "")
		if err != nil {
			t.Fatalf("LoadClientCertificate returned error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil client certificate, got %+v", got)
		}
	})

	t.Run("missing certificate path", func(t *testing.T) {
		_, err := LoadClientCertificate("", "/tmp/key.pem")
		if err == nil {
			t.Fatalf("expected error for missing certificate path")
		}
	})

	t.Run("missing key path", func(t *testing.T) {
		_, err := LoadClientCertificate("/tmp/cert.pem", "")
		if err == nil {
			t.Fatalf("expected error for missing key path")
		}
	})

	t.Run("invalid paths", func(t *testing.T) {
		_, err := LoadClientCertificate("/tmp/no-cert.pem", "/tmp/no-key.pem")
		if err == nil {
			t.Fatalf("expected error for invalid certificate paths")
		}
	})

	t.Run("valid certificate", func(t *testing.T) {
		certPath, keyPath := writeClientCertPair(t)
		got, err := LoadClientCertificate(certPath, keyPath)
		if err != nil {
			t.Fatalf("LoadClientCertificate returned error: %v", err)
		}
		if got == nil {
			t.Fatalf("expected client certificate, got nil")
			return
		}
		if got.CertPath != certPath {
			t.Fatalf("expected cert path %q, got %q", certPath, got.CertPath)
		}
		if got.KeyPath != keyPath {
			t.Fatalf("expected key path %q, got %q", keyPath, got.KeyPath)
		}
		if len(got.Certificate.Certificate) == 0 {
			t.Fatalf("expected parsed certificate chain")
		}
	})
}

func writeClientCertPair(t *testing.T) (string, string) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	certTemplate := x509.Certificate{
		SerialNumber: big.NewInt(100),
		Subject: pkix.Name{
			CommonName: "test-client",
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &certTemplate, &certTemplate, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if certPEM == nil {
		t.Fatalf("encode cert PEM")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	if keyPEM == nil {
		t.Fatalf("encode key PEM")
	}

	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	return certPath, keyPath
}
