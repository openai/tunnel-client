package runtimeconfig

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/pflag"

	"github.com/openai/tunnel-client/pkg/tlsconfig"
)

// Residency selects a public control-plane endpoint. It does not change a
// tunnel's service-owned residency assignment or authorize regional service use.
type Residency string

const (
	ResidencyGlobal Residency = "global"
	ResidencyUS     Residency = "us"
	ResidencyEU     Residency = "eu"
	ResidencyAU     Residency = "au"
	ResidencyCA     Residency = "ca"
	ResidencyGB     Residency = "gb"
	ResidencyIN     Residency = "in"
	ResidencyJP     Residency = "jp"
	ResidencyKR     Residency = "kr"
	ResidencySG     Residency = "sg"
	ResidencyAE     Residency = "ae"
)

type residencyConfig struct {
	BaseURL     string
	MTLSBaseURL string
}

// Keep this catalog independent of service rollout. Customers can install a
// client before regional service support arrives. An absent mTLS endpoint must
// cause an error, never a fallback to another endpoint or ordinary TLS.
var residencyConfigs = map[Residency]residencyConfig{
	ResidencyGlobal: {BaseURL: "https://api.openai.com", MTLSBaseURL: "https://mtls.api.openai.com"},
	ResidencyUS:     {BaseURL: "https://us.api.openai.com", MTLSBaseURL: "https://mtls-us.api.openai.com"},
	ResidencyEU:     {BaseURL: "https://eu.api.openai.com", MTLSBaseURL: "https://mtls-eu.api.openai.com"},
	ResidencyAU:     {BaseURL: "https://au.api.openai.com"},
	ResidencyCA:     {BaseURL: "https://ca.api.openai.com"},
	ResidencyGB:     {BaseURL: "https://gb.api.openai.com"},
	ResidencyIN:     {BaseURL: "https://in.api.openai.com"},
	ResidencyJP:     {BaseURL: "https://jp.api.openai.com"},
	ResidencyKR:     {BaseURL: "https://kr.api.openai.com"},
	ResidencySG:     {BaseURL: "https://sg.api.openai.com"},
	ResidencyAE:     {BaseURL: "https://ae.api.openai.com"},
}

const residencyValues = "global, us, eu, au, ca, gb, in, jp, kr, sg, ae"

// ParseResidency validates and normalizes an explicitly configured residency.
// An omitted setting is represented separately by the empty Residency value.
func ParseResidency(raw string) (Residency, error) {
	residency := Residency(strings.ToLower(strings.TrimSpace(raw)))
	if _, ok := residencyConfigs[residency]; !ok {
		return "", fmt.Errorf("invalid control-plane.residency %q: expected one of %s", raw, residencyValues)
	}
	return residency, nil
}

// ResolveControlPlaneBaseURL shares endpoint selection across the runtime,
// administrative commands, and SDK. Empty residency preserves legacy behavior.
// A base URL within the selected residency can select its ordinary TLS origin;
// providing a certificate then selects that residency's mTLS origin.
func ResolveControlPlaneBaseURL(rawBaseURL string, residency Residency, mtls bool) (*url.URL, error) {
	if residency == "" {
		if rawBaseURL == "" {
			rawBaseURL = defaultControlPlaneBaseURL
		}
		if mtls && strings.TrimRight(strings.TrimSpace(rawBaseURL), "/") == defaultControlPlaneBaseURL {
			rawBaseURL = defaultControlPlaneMTLSBaseURL
		}
		return parseControlPlaneBaseURL(rawBaseURL)
	}
	selected, ok := residencyConfigs[residency]
	if !ok {
		return nil, fmt.Errorf("invalid control-plane.residency %q: expected one of %s", residency, residencyValues)
	}
	target := selected.BaseURL
	if mtls {
		if selected.MTLSBaseURL == "" {
			return nil, fmt.Errorf("control-plane.residency %q has no configured mTLS endpoint", residency)
		}
		target = selected.MTLSBaseURL
	}
	if rawBaseURL != "" {
		base, err := parseControlPlaneBaseURL(rawBaseURL)
		if err != nil {
			return nil, err
		}
		// Compare complete, normalized origins. Lookalike hosts, paths, ports,
		// credentials, queries, and fragments cannot override a residency.
		base.Scheme = strings.ToLower(base.Scheme)
		base.Host = strings.ToLower(base.Host)
		if base.Path == "/" {
			base.Path = ""
		}
		if base.String() != target && (!mtls || base.String() != selected.BaseURL) {
			return nil, fmt.Errorf("control-plane.base-url conflicts with control-plane.residency %q; remove the base URL or use the selected residency's endpoint", residency)
		}
	}
	return parseControlPlaneBaseURL(target)
}

func parseControlPlaneBaseURL(raw string) (*url.URL, error) {
	u, err := parseURL(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid control-plane.base-url: %w", err)
	}
	return u, nil
}

// RegisterControlPlaneEndpointFlags installs the common endpoint and client
// certificate options without requiring runtime tunnel or MCP settings.
func RegisterControlPlaneEndpointFlags(fs *pflag.FlagSet) {
	fs.String("control-plane.base-url", defaultControlPlaneBaseURL, "Tunnel control-plane base URL (env.CONTROL_PLANE_BASE_URL)")
	fs.String("control-plane.residency", "", "Select a control-plane residency endpoint: "+residencyValues+" (env.CONTROL_PLANE_RESIDENCY); service availability is independent")
	fs.String("control-plane.client-cert", "", "Path to PEM client certificate for control-plane mTLS (format <path|env:VAR|file:/path>) (env.CONTROL_PLANE_CLIENT_CERT)")
	fs.String("control-plane.client-key", "", "Path to PEM client private key for control-plane mTLS (format <path|env:VAR|file:/path>) (env.CONTROL_PLANE_CLIENT_KEY)")
}

// ControlPlaneEndpoint is the resolved endpoint and its independent TLS identity.
type ControlPlaneEndpoint struct {
	Residency         Residency
	BaseURL           *url.URL
	ClientCertificate *tlsconfig.ClientCertificate
}

// BuildControlPlaneEndpoint applies flag/environment precedence. Runtime callers
// pass the existing environment lookup overlaid on YAML/profile values.
func BuildControlPlaneEndpoint(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) (ControlPlaneEndpoint, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	var endpoint ControlPlaneEndpoint
	raw, configured := lookupEnv("CONTROL_PLANE_RESIDENCY")
	if flag := fs.Lookup("control-plane.residency"); flag != nil && flag.Changed {
		raw, configured = flag.Value.String(), true
	}
	if configured {
		residency, err := ParseResidency(raw)
		if err != nil {
			return endpoint, err
		}
		endpoint.Residency = residency
	}
	certificate, err := buildControlPlaneClientCertificate(fs, lookupEnv)
	if err != nil {
		return endpoint, err
	}
	endpoint.ClientCertificate = certificate
	rawBaseURL := firstSet(getValue(fs, "control-plane.base-url"), envOrDefault(lookupEnv, "CONTROL_PLANE_BASE_URL", ""))
	endpoint.BaseURL, err = ResolveControlPlaneBaseURL(rawBaseURL, endpoint.Residency, certificate != nil)
	return endpoint, err
}
