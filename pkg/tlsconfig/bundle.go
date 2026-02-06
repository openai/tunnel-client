package tlsconfig

import (
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Bundle contains a loaded PEM CA bundle and the derived RootCAs pool.
type Bundle struct {
	Path        string
	RootCAs     *x509.CertPool
	SystemTrust SystemTrustSummary
	ExtraBundle *ExtraBundleSummary
}

var logOnce sync.Once

// LoadBundle reads the provided PEM file and returns a certificate pool that
// extends the system trust roots with the provided certificates.
func LoadBundle(path string) (*Bundle, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle %q: %w", path, err)
	}
	systemTrust := systemTrustSummary()
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if ok := pool.AppendCertsFromPEM(contents); !ok {
		return nil, errors.New("PEM bundle contained no certificates")
	}
	return &Bundle{
		Path:        path,
		RootCAs:     pool,
		SystemTrust: systemTrust,
		ExtraBundle: parseExtraBundle(path, contents),
	}, nil
}

// LogBundleUsage logs that a custom CA bundle is in use. It logs at most once.
func LogBundleUsage(logger *slog.Logger, bundle *Bundle) {
	if logger == nil || bundle == nil {
		return
	}
	logOnce.Do(func() {
		logger.Info("custom CA bundle configured", slog.String("path", bundle.Path))
	})
}
