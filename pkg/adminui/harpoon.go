package adminui

import (
	"net/http"
	"strings"
	"time"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon"
	"go.openai.org/api/tunnel-client/pkg/proxy"
	"go.openai.org/api/tunnel-client/pkg/proxyhealth"
)

type harpoonStatusResponse struct {
	Enabled            bool                 `json:"enabled"`
	Reason             string               `json:"reason,omitempty"`
	CapturePayloads    bool                 `json:"capture_payloads"`
	AllowPlaintextHTTP bool                 `json:"allow_plaintext_http"`
	MaxResponseBytes   int                  `json:"max_response_bytes"`
	MaxRedirects       int                  `json:"max_redirects"`
	ProxyRoutes        []proxy.RouteSummary `json:"proxy_routes,omitempty"`
}

type harpoonTargetsResponse struct {
	Targets []harpoonTargetResponse `json:"targets"`
}

type harpoonTargetResponse struct {
	Label           string `json:"label"`
	URL             string `json:"url"`
	Description     string `json:"description,omitempty"`
	Source          string `json:"source,omitempty"`
	InclusionReason string `json:"inclusion_reason,omitempty"`
}

type harpoonCallsResponse struct {
	Calls []harpoonCallResponse `json:"calls"`
}

type harpoonCallResponse struct {
	Timestamp               time.Time `json:"timestamp"`
	Label                   string    `json:"label"`
	URL                     string    `json:"url"`
	Method                  string    `json:"method"`
	Status                  int       `json:"status"`
	LatencyMS               int       `json:"latency_ms"`
	ReqBytes                int       `json:"req_bytes"`
	RespBytes               int       `json:"resp_bytes"`
	Error                   string    `json:"error,omitempty"`
	RequestBody             *string   `json:"request_body,omitempty"`
	ResponseBody            *string   `json:"response_body,omitempty"`
	ResponseBodyTransformed *string   `json:"response_body_transformed,omitempty"`
	BodyIsBase64            *bool     `json:"body_is_base64,omitempty"`
}

func handleHarpoonStatus(registry *harpoon.Registry, cfg *config.HarpoonConfig, proxySnapshot proxyhealth.Snapshotter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildHarpoonStatus(registry, cfg, proxySnapshot))
	}
}

func handleHarpoonTargets(registry *harpoon.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildHarpoonTargets(registry))
	}
}

func handleHarpoonCalls(buffer *harpoon.CallBuffer, cfg *config.HarpoonConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		label := ""
		if r != nil && r.URL != nil {
			label = strings.TrimSpace(r.URL.Query().Get("label"))
		}
		limit := parseLimit(r, 100, 100)
		writeJSON(w, http.StatusOK, buildHarpoonCalls(buffer, cfg, label, limit))
	}
}

func buildHarpoonStatus(registry *harpoon.Registry, cfg *config.HarpoonConfig, proxySnapshot proxyhealth.Snapshotter) harpoonStatusResponse {
	enabled := false
	reason := ""
	if registry == nil {
		reason = "harpoon not initialized"
	} else if len(registry.Targets()) == 0 {
		reason = "no targets configured"
	} else {
		enabled = true
	}

	capture := false
	allowPlaintext := false
	maxResponse := 0
	maxRedirects := 0
	if cfg != nil {
		capture = cfg.CapturePayloads
		allowPlaintext = cfg.AllowPlaintextHTTP
		maxResponse = cfg.MaxResponseBytes
		maxRedirects = cfg.MaxRedirects
	}

	return harpoonStatusResponse{
		Enabled:            enabled,
		Reason:             reason,
		CapturePayloads:    capture,
		AllowPlaintextHTTP: allowPlaintext,
		MaxResponseBytes:   maxResponse,
		MaxRedirects:       maxRedirects,
		ProxyRoutes:        harpoonProxyRoutes(proxySnapshot),
	}
}

func buildHarpoonTargets(registry *harpoon.Registry) harpoonTargetsResponse {
	if registry == nil {
		return harpoonTargetsResponse{}
	}
	targets := registry.Targets()
	out := make([]harpoonTargetResponse, 0, len(targets))
	for _, target := range targets {
		url := ""
		if target.BaseURL != nil {
			url = target.BaseURL.String()
		}
		out = append(out, harpoonTargetResponse{
			Label:           target.Label,
			URL:             url,
			Description:     target.Description,
			Source:          target.Source,
			InclusionReason: target.InclusionReason,
		})
	}
	return harpoonTargetsResponse{Targets: out}
}

func buildHarpoonCalls(buffer *harpoon.CallBuffer, cfg *config.HarpoonConfig, label string, limit int) harpoonCallsResponse {
	if buffer == nil {
		return harpoonCallsResponse{}
	}
	capture := cfg != nil && cfg.CapturePayloads
	entries := buffer.Snapshot(limit, label)
	out := make([]harpoonCallResponse, 0, len(entries))
	for _, entry := range entries {
		call := harpoonCallResponse{
			Timestamp: entry.Timestamp,
			Label:     entry.Label,
			URL:       entry.URL,
			Method:    entry.Method,
			Status:    entry.Status,
			LatencyMS: entry.LatencyMS,
			ReqBytes:  entry.ReqBytes,
			RespBytes: entry.RespBytes,
			Error:     entry.Error,
		}
		if capture {
			req := entry.RequestBody
			resp := entry.ResponseBody
			respTransformed := entry.ResponseBodyTransformed
			base64Flag := entry.BodyIsBase64
			call.RequestBody = &req
			call.ResponseBody = &resp
			if respTransformed != "" {
				call.ResponseBodyTransformed = &respTransformed
			}
			call.BodyIsBase64 = &base64Flag
		}
		out = append(out, call)
	}
	return harpoonCallsResponse{Calls: out}
}
