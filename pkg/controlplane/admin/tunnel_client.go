package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.openai.org/api/tunnel-client/pkg/config"
	tctransport "go.openai.org/api/tunnel-client/pkg/transport"
)

const (
	tunnelsPath       = "/v1/tunnels"
	tunnelByIDPathFmt = "/v1/tunnels/%s"
	defaultTimeout    = 30 * time.Second
)

// AdminTunnelClient is a lightweight HTTP client for the tunnel management API.
type AdminTunnelClient struct {
	httpClient *http.Client
	baseURL    *url.URL
	adminKey   string
}

// NewAdminTunnelClient builds an AdminTunnelClient from the provided config.
func NewAdminTunnelClient(cfg *config.AdminConfig) (*AdminTunnelClient, error) {
	if cfg == nil {
		return nil, errors.New("admin client: config is required")
	}
	if cfg.BaseURL == nil {
		return nil, errors.New("admin client: base URL is required")
	}
	if cfg.AdminKey == "" {
		return nil, errors.New("admin client: admin key is required")
	}

	transport, err := tctransport.CloneDefaultWithBundle(cfg.TLS)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: defaultTimeout, Transport: transport}

	return &AdminTunnelClient{
		httpClient: client,
		baseURL:    cfg.BaseURL,
		adminKey:   cfg.AdminKey,
	}, nil
}

// CreateTunnel creates a new tunnel.
func (c *AdminTunnelClient) CreateTunnel(ctx context.Context, req TunnelCreateRequest) (*Tunnel, error) {
	var out Tunnel
	if err := c.do(ctx, http.MethodPost, tunnelsPath, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTunnel fetches a tunnel by id.
func (c *AdminTunnelClient) GetTunnel(ctx context.Context, tunnelID string) (*Tunnel, error) {
	if tunnelID == "" {
		return nil, errors.New("tunnel id is required")
	}
	path := fmt.Sprintf(tunnelByIDPathFmt, url.PathEscape(tunnelID))
	var out Tunnel
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTunnels returns tunnels filtered by organization, workspace, or tenant.
func (c *AdminTunnelClient) ListTunnels(ctx context.Context, organizationID, workspaceID, tenantID string) (*TunnelListResponse, error) {
	q := url.Values{}
	if organizationID != "" {
		q.Set("organization_id", organizationID)
	}
	if workspaceID != "" {
		q.Set("workspace_id", workspaceID)
	}
	if tenantID != "" {
		q.Set("tenant_id", tenantID)
	}
	var out TunnelListResponse
	if err := c.do(ctx, http.MethodGet, tunnelsPath, q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateTunnel mutates a tunnel.
func (c *AdminTunnelClient) UpdateTunnel(ctx context.Context, tunnelID string, req TunnelUpdateRequest) (*Tunnel, error) {
	if tunnelID == "" {
		return nil, errors.New("tunnel id is required")
	}
	path := fmt.Sprintf(tunnelByIDPathFmt, url.PathEscape(tunnelID))
	var out Tunnel
	if err := c.do(ctx, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTunnel removes a tunnel.
func (c *AdminTunnelClient) DeleteTunnel(ctx context.Context, tunnelID string) (*Tunnel, error) {
	if tunnelID == "" {
		return nil, errors.New("tunnel id is required")
	}
	path := fmt.Sprintf(tunnelByIDPathFmt, url.PathEscape(tunnelID))
	var out Tunnel
	if err := c.do(ctx, http.MethodDelete, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *AdminTunnelClient) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	target := c.baseURL.ResolveReference(&url.URL{Path: path})
	if query != nil {
		target.RawQuery = query.Encode()
	}

	var buf io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		buf = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), buf)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.adminKey))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	requestID := resp.Header.Get("x-request-id")

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		errMsg := fmt.Sprintf("request %s %s failed: %d %s", method, target.Path, resp.StatusCode, strings.TrimSpace(string(msg)))
		if requestID != "" {
			errMsg = fmt.Sprintf("%s (x-request-id: %s)", errMsg, requestID)
		}
		return errors.New(errMsg)
	}

	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
