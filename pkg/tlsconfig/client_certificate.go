package tlsconfig

import (
	"crypto/tls"
	"fmt"
	"strings"
)

// ClientCertificate contains a loaded client certificate/key pair used for
// outbound mTLS.
type ClientCertificate struct {
	CertPath    string
	KeyPath     string
	Certificate tls.Certificate
}

// LoadClientCertificate loads and validates an mTLS client certificate/key
// pair. Both paths are required when either is set.
func LoadClientCertificate(certPath, keyPath string) (*ClientCertificate, error) {
	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)

	switch {
	case certPath == "" && keyPath == "":
		return nil, nil
	case certPath == "":
		return nil, fmt.Errorf("client certificate path is required when key path is set")
	case keyPath == "":
		return nil, fmt.Errorf("client key path is required when certificate path is set")
	}

	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load client certificate/key pair: %w", err)
	}

	return &ClientCertificate{
		CertPath:    certPath,
		KeyPath:     keyPath,
		Certificate: pair,
	}, nil
}
