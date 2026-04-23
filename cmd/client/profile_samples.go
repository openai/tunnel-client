package main

import (
	"bytes"
	"embed"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"go.openai.org/api/tunnel-client/pkg/config"
)

const defaultInitHealthListenAddr = "127.0.0.1:8080"

//go:embed profile_samples/*.tmpl
var embeddedProfileSamples embed.FS

type sampleProfileRequest struct {
	TunnelID         string
	BaseURL          string
	APIKeyRef        string
	HealthListenAddr string
	OpenBrowser      bool
	MCPServerURL     string
	MCPCommand       string
}

type profileSampleDefinition struct {
	Name          string
	Summary       string
	UseWhen       string
	RequiredFlags []string
	OptionalFlags []string
	Caveats       []string
	Example       sampleProfileRequest
	Generate      func(sampleProfileRequest) ([]byte, error)
}

func profileSamples() []profileSampleDefinition {
	return []profileSampleDefinition{
		{
			Name:          "sample_mcp_with_dcr",
			Summary:       "HTTP or stdio MCP target with DCR-friendly tunnel-client defaults",
			UseWhen:       "Use this when your MCP server exposes OAuth/PRMD metadata or when you want the full HTTP/OAuth startup contract exercised.",
			RequiredFlags: []string{"--tunnel-id", "exactly one of --mcp-server-url or --mcp-command"},
			OptionalFlags: []string{"--control-plane-base-url", "--control-plane-api-key-ref", "--health-listen-addr", "--open-web-ui"},
			Caveats: []string{
				"Runtime profiles store secret references such as env:CONTROL_PLANE_API_KEY, not literal keys.",
				"The main MCP channel is always bound as channel=main.",
			},
			Example: sampleProfileRequest{
				TunnelID:         "tunnel_0123456789abcdef0123456789abcdef",
				BaseURL:          "https://api.openai.com",
				APIKeyRef:        "env:CONTROL_PLANE_API_KEY",
				HealthListenAddr: defaultInitHealthListenAddr,
				MCPServerURL:     "http://127.0.0.1:3001/mcp",
			},
			Generate: generateSampleMCPWithDCRProfile,
		},
		{
			Name:          "sample_mcp_stdio_local",
			Summary:       "Local stdio MCP server with the shortest first-use tunnel-client path",
			UseWhen:       "Use this when you already have a local MCP command and want the fastest path to a healthy tunnel daemon without any HTTP or OAuth discovery setup.",
			RequiredFlags: []string{"--tunnel-id", "--mcp-command"},
			OptionalFlags: []string{"--control-plane-base-url", "--control-plane-api-key-ref", "--health-listen-addr", "--open-web-ui"},
			Caveats: []string{
				"Stdio transports skip HTTP OAuth discovery because there is no PRMD endpoint to fetch.",
				"The command is always bound to channel=main.",
			},
			Example: sampleProfileRequest{
				TunnelID:         "tunnel_0123456789abcdef0123456789abcdef",
				BaseURL:          "https://api.openai.com",
				APIKeyRef:        "env:CONTROL_PLANE_API_KEY",
				HealthListenAddr: defaultInitHealthListenAddr,
				MCPCommand:       "python /path/to/server.py",
			},
			Generate: generateSampleMCPStdioLocalProfile,
		},
		{
			Name:          "sample_mcp_remote_no_auth",
			Summary:       "Remote HTTP MCP server that does not advertise OAuth/PRMD metadata",
			UseWhen:       "Use this when your MCP server is already reachable over HTTP(S) and intentionally does not use OAuth/DCR metadata.",
			RequiredFlags: []string{"--tunnel-id", "--mcp-server-url"},
			OptionalFlags: []string{"--control-plane-base-url", "--control-plane-api-key-ref", "--health-listen-addr", "--open-web-ui"},
			Caveats: []string{
				"Plain MCP servers can still reach ready when protected-resource metadata returns 404 on all candidates.",
				"Unexpected 5xx or malformed metadata responses still keep readiness degraded.",
			},
			Example: sampleProfileRequest{
				TunnelID:         "tunnel_0123456789abcdef0123456789abcdef",
				BaseURL:          "https://api.openai.com",
				APIKeyRef:        "env:CONTROL_PLANE_API_KEY",
				HealthListenAddr: defaultInitHealthListenAddr,
				MCPServerURL:     "https://mcp.example.com/mcp",
			},
			Generate: generateSampleMCPRemoteNoAuthProfile,
		},
		{
			Name:          "sample_mcp_enterprise_proxy",
			Summary:       "HTTP or stdio MCP target for outbound proxies or private PKI environments",
			UseWhen:       "Use this when tunnel-client must egress through a corporate HTTP(S) proxy, trust a private CA bundle, or keep runtime and admin credentials clearly separated for operators.",
			RequiredFlags: []string{"--tunnel-id", "exactly one of --mcp-server-url or --mcp-command"},
			OptionalFlags: []string{"--control-plane-base-url", "--control-plane-api-key-ref", "--health-listen-addr", "--open-web-ui"},
			Caveats: []string{
				"The profile pins a global explicit proxy via env:HTTPS_PROXY so control-plane, MCP HTTP, and Harpoon traffic use the same outbound route.",
				"The CA bundle is loaded from env:ENTERPRISE_CA_BUNDLE; unset or remove that line if your environment uses public trust only.",
				"OPENAI_ADMIN_KEY is documented in the sample comments for admin flows, but tunnel-client run only consumes the runtime key reference.",
			},
			Example: sampleProfileRequest{
				TunnelID:         "tunnel_0123456789abcdef0123456789abcdef",
				BaseURL:          "https://api.openai.com",
				APIKeyRef:        "env:CONTROL_PLANE_API_KEY",
				HealthListenAddr: defaultInitHealthListenAddr,
				MCPServerURL:     "https://mcp.internal.example.com/mcp",
			},
			Generate: generateSampleMCPEntProxyProfile,
		},
	}
}

func sortedSampleNames() []string {
	names := make([]string, 0, len(profileSamples()))
	for _, sample := range profileSamples() {
		names = append(names, sample.Name)
	}
	slices.Sort(names)
	return names
}

func findProfileSample(name string) (profileSampleDefinition, bool) {
	for _, sample := range profileSamples() {
		if sample.Name == strings.TrimSpace(name) {
			return sample, true
		}
	}
	return profileSampleDefinition{}, false
}

func generateProfileSample(name string, req sampleProfileRequest) ([]byte, error) {
	sample, ok := findProfileSample(name)
	if !ok {
		return nil, fmt.Errorf("unknown sample %q; run `tunnel-client profiles samples list`", name)
	}
	return sample.Generate(req)
}

func generateSampleMCPWithDCRProfile(req sampleProfileRequest) ([]byte, error) {
	req, err := normalizeSampleRequest(req)
	if err != nil {
		return nil, err
	}
	req.MCPServerURL = strings.TrimSpace(req.MCPServerURL)
	req.MCPCommand = strings.TrimSpace(req.MCPCommand)
	if (req.MCPServerURL == "") == (req.MCPCommand == "") {
		return nil, fmt.Errorf("sample_mcp_with_dcr requires exactly one of --mcp-server-url or --mcp-command")
	}
	if req.MCPServerURL != "" {
		if _, err := url.Parse(req.MCPServerURL); err != nil {
			return nil, fmt.Errorf("invalid MCP server URL %q: %w", req.MCPServerURL, err)
		}
	}
	return renderEmbeddedSampleTemplate("profile_samples/sample_mcp_with_dcr.yaml.tmpl", req)
}

func generateSampleMCPStdioLocalProfile(req sampleProfileRequest) ([]byte, error) {
	req, err := normalizeSampleRequest(req)
	if err != nil {
		return nil, err
	}
	req.MCPCommand = strings.TrimSpace(req.MCPCommand)
	if req.MCPCommand == "" {
		return nil, fmt.Errorf("sample_mcp_stdio_local requires --mcp-command")
	}
	req.MCPServerURL = ""
	return renderEmbeddedSampleTemplate("profile_samples/sample_mcp_stdio_local.yaml.tmpl", req)
}

func generateSampleMCPRemoteNoAuthProfile(req sampleProfileRequest) ([]byte, error) {
	req, err := normalizeSampleRequest(req)
	if err != nil {
		return nil, err
	}
	req.MCPServerURL = strings.TrimSpace(req.MCPServerURL)
	if req.MCPServerURL == "" {
		return nil, fmt.Errorf("sample_mcp_remote_no_auth requires --mcp-server-url")
	}
	if _, err := url.Parse(req.MCPServerURL); err != nil {
		return nil, fmt.Errorf("invalid MCP server URL %q: %w", req.MCPServerURL, err)
	}
	req.MCPCommand = ""
	return renderEmbeddedSampleTemplate("profile_samples/sample_mcp_remote_no_auth.yaml.tmpl", req)
}

func generateSampleMCPEntProxyProfile(req sampleProfileRequest) ([]byte, error) {
	req, err := normalizeSampleRequest(req)
	if err != nil {
		return nil, err
	}
	req.MCPServerURL = strings.TrimSpace(req.MCPServerURL)
	req.MCPCommand = strings.TrimSpace(req.MCPCommand)
	if (req.MCPServerURL == "") == (req.MCPCommand == "") {
		return nil, fmt.Errorf("sample_mcp_enterprise_proxy requires exactly one of --mcp-server-url or --mcp-command")
	}
	if req.MCPServerURL != "" {
		if _, err := url.Parse(req.MCPServerURL); err != nil {
			return nil, fmt.Errorf("invalid MCP server URL %q: %w", req.MCPServerURL, err)
		}
	}
	return renderEmbeddedSampleTemplate("profile_samples/sample_mcp_enterprise_proxy.yaml.tmpl", req)
}

func normalizeSampleRequest(req sampleProfileRequest) (sampleProfileRequest, error) {
	req.TunnelID = strings.TrimSpace(req.TunnelID)
	if err := config.ValidateTunnelID(req.TunnelID); err != nil {
		return req, err
	}
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.BaseURL == "" {
		req.BaseURL = "https://api.openai.com"
	}
	if _, err := url.Parse(req.BaseURL); err != nil {
		return req, fmt.Errorf("invalid control plane base URL %q: %w", req.BaseURL, err)
	}
	req.APIKeyRef = strings.TrimSpace(req.APIKeyRef)
	if req.APIKeyRef == "" {
		req.APIKeyRef = "env:CONTROL_PLANE_API_KEY"
	}
	if err := validateSecretRef(req.APIKeyRef); err != nil {
		return req, fmt.Errorf("invalid control plane API key reference: %w", err)
	}
	req.HealthListenAddr = strings.TrimSpace(req.HealthListenAddr)
	if req.HealthListenAddr == "" {
		req.HealthListenAddr = defaultInitHealthListenAddr
	}
	if _, _, err := net.SplitHostPort(req.HealthListenAddr); err != nil {
		return req, fmt.Errorf("invalid health listen address %q: %w", req.HealthListenAddr, err)
	}
	return req, nil
}

func renderEmbeddedSampleTemplate(path string, req sampleProfileRequest) ([]byte, error) {
	tmpl, err := template.ParseFS(embeddedProfileSamples, path)
	if err != nil {
		return nil, fmt.Errorf("parse embedded sample template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, req); err != nil {
		return nil, fmt.Errorf("render embedded sample template: %w", err)
	}
	return buf.Bytes(), nil
}

func validateSecretRef(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("secret reference is required")
	}
	if strings.HasPrefix(strings.ToLower(raw), "env:") {
		name := strings.TrimSpace(strings.TrimPrefix(raw, "env:"))
		if name == "" {
			return fmt.Errorf("environment variable name is required")
		}
		return nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "file:") {
		path := strings.TrimSpace(strings.TrimPrefix(raw, "file:"))
		if path == "" {
			return fmt.Errorf("file path is required")
		}
		return nil
	}
	return fmt.Errorf("use env:VARNAME or file:/path/to/key")
}

func writeProfileFile(path string, dir string, data []byte, force bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create profile directory %s: %w", dir, err)
	}
	flag := os.O_WRONLY | os.O_CREATE
	if force {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("profile %q already exists; pass --force to replace it", strings.TrimSuffix(filepath.Base(path), ".yaml"))
		}
		return fmt.Errorf("write profile %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write profile %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close profile %s: %w", path, err)
	}
	return nil
}
