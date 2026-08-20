package tlsconfig

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// SystemTrustSummary describes system trust root usage.
type SystemTrustSummary struct {
	Enabled      bool     `json:"enabled"`
	Source       string   `json:"source"`
	SourcePaths  []string `json:"source_paths,omitempty"`
	FallbackNote string   `json:"fallback_note,omitempty"`
}

// CertMetadata captures metadata for a certificate.
type CertMetadata struct {
	CertID      string `json:"cert_id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	SubjectCN   string `json:"subject_cn,omitempty"`
	IssuerCN    string `json:"issuer_cn,omitempty"`
	NotBefore   string `json:"not_before,omitempty"`
	NotAfter    string `json:"not_after,omitempty"`
	Source      string `json:"source,omitempty"`
	ParseStatus string `json:"parse_status"`
}

// ExtraBundleSummary describes the extra CA bundle.
type ExtraBundleSummary struct {
	Path         string         `json:"path,omitempty"`
	CertCount    int            `json:"cert_count"`
	ParseErrors  int            `json:"parse_errors"`
	Certificates []CertMetadata `json:"certificates,omitempty"`
}

// TrustReport describes outbound TLS trust configuration.
type TrustReport struct {
	SystemTrust SystemTrustSummary  `json:"system_trust"`
	ExtraBundle *ExtraBundleSummary `json:"extra_bundle,omitempty"`
}

var trustLogOnce sync.Once

// BuildTrustReport returns trust metadata for logging and APIs.
func BuildTrustReport(bundle *Bundle) TrustReport {
	if bundle == nil {
		return TrustReport{SystemTrust: systemTrustSummary()}
	}
	report := TrustReport{SystemTrust: bundle.SystemTrust}
	if bundle.ExtraBundle != nil {
		report.ExtraBundle = bundle.ExtraBundle
	}
	return report
}

// LogTrustReport logs a structured TLS trust report once.
func LogTrustReport(logger *slog.Logger, bundle *Bundle) {
	if logger == nil {
		return
	}
	trustLogOnce.Do(func() {
		report := BuildTrustReport(bundle)
		logger.Info("tls trust summary", slog.Any("system_trust", report.SystemTrust), slog.Any("extra_bundle", report.ExtraBundle))
	})
}

func systemTrustSummary() SystemTrustSummary {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		return SystemTrustSummary{
			Enabled:      false,
			Source:       "system cert pool",
			FallbackNote: fmt.Sprintf("system cert pool unavailable: %v", err),
		}
	}
	return SystemTrustSummary{
		Enabled: true,
		Source:  "system cert pool",
	}
}

func parseExtraBundle(path string, contents []byte) *ExtraBundleSummary {
	if path == "" || len(contents) == 0 {
		return nil
	}
	summary := &ExtraBundleSummary{Path: path}
	rest := contents
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		meta := CertMetadata{Source: path}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			meta.ParseStatus = err.Error()
			summary.Certificates = append(summary.Certificates, meta)
			summary.ParseErrors++
			continue
		}
		meta.ParseStatus = "ok"
		meta.SubjectCN = cert.Subject.CommonName
		meta.IssuerCN = cert.Issuer.CommonName
		meta.Name = firstNonEmpty(meta.SubjectCN, cert.Subject.String())
		meta.Description = "extra CA bundle certificate"
		meta.NotBefore = cert.NotBefore.UTC().Format(time.RFC3339)
		meta.NotAfter = cert.NotAfter.UTC().Format(time.RFC3339)
		meta.CertID = hashCertificate(cert)
		summary.Certificates = append(summary.Certificates, meta)
		summary.CertCount++
	}
	return summary
}

func hashCertificate(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	value := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(value[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
