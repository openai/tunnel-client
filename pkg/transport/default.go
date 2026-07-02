package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"

	"github.com/openai/tunnel-client/pkg/tlsconfig"
)

// CloneDefault returns an isolated copy of the default HTTP transport so callers
// can customize behavior without mutating shared state.
func CloneDefault() http.RoundTripper {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return http.DefaultTransport
}

// CloneDefaultWithBundle returns a default HTTP transport with the custom CA
// bundle applied, when provided.
func CloneDefaultWithBundle(bundle *tlsconfig.Bundle) (http.RoundTripper, error) {
	base := CloneDefault()
	return ApplyBundle(base, bundle)
}

// ApplyBundle applies a custom CA bundle to the provided RoundTripper.
func ApplyBundle(base http.RoundTripper, bundle *tlsconfig.Bundle) (http.RoundTripper, error) {
	if bundle == nil || bundle.RootCAs == nil {
		return base, nil
	}
	if base == nil {
		return nil, fmt.Errorf("base transport is nil")
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unsupported transport type %T", base)
	}
	cloned := transport.Clone()
	tlsConfig := cloned.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	tlsConfig.RootCAs = bundle.RootCAs
	cloned.TLSClientConfig = tlsConfig
	return cloned, nil
}

// ApplyClientCertificate applies an mTLS client certificate to the provided
// RoundTripper when one is configured.
func ApplyClientCertificate(base http.RoundTripper, clientCertificate *tlsconfig.ClientCertificate) (http.RoundTripper, error) {
	if clientCertificate == nil {
		return base, nil
	}
	if base == nil {
		return nil, fmt.Errorf("base transport is nil")
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unsupported transport type %T", base)
	}
	cloned := transport.Clone()
	tlsConfig := cloned.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	certificate := clientCertificate.Certificate
	tlsConfig.Certificates = []tls.Certificate{certificate}
	tlsConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		return &certificate, nil
	}
	cloned.TLSClientConfig = tlsConfig
	return cloned, nil
}

// ApplyUnixSocketPath makes HTTP requests dial the provided Unix-domain socket.
func ApplyUnixSocketPath(base http.RoundTripper, socketPath string) (http.RoundTripper, error) {
	if socketPath == "" {
		return base, nil
	}
	if base == nil {
		return nil, fmt.Errorf("base transport is nil")
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unsupported transport type %T", base)
	}

	cloned := transport.Clone()
	dialer := &net.Dialer{}
	cloned.Proxy = nil
	cloned.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}
	return cloned, nil
}
