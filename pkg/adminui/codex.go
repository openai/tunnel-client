package adminui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	assistantkb "github.com/openai/tunnel-client/docs"
	"github.com/openai/tunnel-client/pkg/codexappserver"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/httpguard"
	pluginsbundle "github.com/openai/tunnel-client/plugins"
)

const (
	defaultCodexApprovalPolicy = "never"
	defaultCodexSandboxType    = "workspace-write"
)

type codexEventsResponse struct {
	Events []codexappserver.Event `json:"events"`
}

type codexThreadStartRequest struct {
	CWD                   string `json:"cwd"`
	Model                 string `json:"model"`
	ModelProvider         string `json:"model_provider"`
	ApprovalPolicy        string `json:"approval_policy"`
	SandboxType           string `json:"sandbox_type"`
	DeveloperInstructions string `json:"developer_instructions"`
	InjectContext         bool   `json:"inject_context"`
}

type codexTurnStartRequest struct {
	ThreadID       string `json:"thread_id"`
	Prompt         string `json:"prompt"`
	CWD            string `json:"cwd"`
	ApprovalPolicy string `json:"approval_policy"`
	SandboxType    string `json:"sandbox_type"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	Summary        string `json:"summary"`
	InjectContext  bool   `json:"inject_context"`
}

type codexLoginCancelRequest struct {
	LoginID string `json:"login_id"`
}

type codexStatusResponse struct {
	codexappserver.Snapshot
	State string `json:"state"`
}

func handleCodexStatus(p routeParams) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if p.CodexBridge == nil {
			http.Error(w, "codex bridge unavailable", http.StatusServiceUnavailable)
			return
		}
		p.CodexBridge.Warmup()
		snapshot := p.CodexBridge.Snapshot()
		writeJSON(w, http.StatusOK, codexStatusResponse{
			Snapshot: snapshot,
			State:    codexState(snapshot),
		})
	}
}

func handleCodexEvents(p routeParams) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if p.CodexBridge == nil {
			http.Error(w, "codex bridge unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, codexEventsResponse{
			Events: p.CodexBridge.RecentEvents(parseLimit(r, 200, 1000)),
		})
	}
}

func handleCodexEventsStream(p routeParams, shutdownCtx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if p.CodexBridge == nil {
			http.Error(w, "codex bridge unavailable", http.StatusServiceUnavailable)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		streamCtx := httpguard.MergeContexts(r.Context(), shutdownCtx)
		notify := p.CodexBridge.Subscribe(streamCtx)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-streamCtx.Done():
				return
			case event, ok := <-notify:
				if !ok {
					return
				}
				payload, err := json.Marshal(event)
				if err != nil {
					continue
				}
				_, _ = fmt.Fprintf(w, "event: codex\nid: %d\ndata: %s\n\n", event.Seq, payload)
				flusher.Flush()
			case <-ticker.C:
				_, _ = fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}

func handleCodexLoginDevice(p routeParams) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if p.CodexBridge == nil {
			http.Error(w, "codex bridge unavailable", http.StatusServiceUnavailable)
			return
		}
		result, err := p.CodexBridge.StartDeviceCodeLogin(r.Context())
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func handleCodexLoginCancel(p routeParams) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if p.CodexBridge == nil {
			http.Error(w, "codex bridge unavailable", http.StatusServiceUnavailable)
			return
		}
		var request codexLoginCancelRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil && err.Error() != "EOF" {
			writeAPIError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
			return
		}
		if err := p.CodexBridge.CancelLogin(r.Context(), request.LoginID); err != nil {
			writeAPIError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "cancel_requested"})
	}
}

func handleCodexThreadStart(p routeParams) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if p.CodexBridge == nil {
			http.Error(w, "codex bridge unavailable", http.StatusServiceUnavailable)
			return
		}

		var request codexThreadStartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeAPIError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
			return
		}

		result, err := p.CodexBridge.StartThread(r.Context(), codexappserver.ThreadStartParams{
			CWD:                   strings.TrimSpace(request.CWD),
			Model:                 strings.TrimSpace(request.Model),
			ModelProvider:         strings.TrimSpace(request.ModelProvider),
			ApprovalPolicy:        codexApprovalPolicy(request.ApprovalPolicy),
			SandboxType:           codexSandboxType(request.SandboxType),
			DeveloperInstructions: buildCodexDeveloperInstructions(strings.TrimSpace(request.DeveloperInstructions)),
		})
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, err)
			return
		}
		if request.InjectContext {
			if injectErr := p.CodexBridge.InjectThreadItems(
				r.Context(),
				result.ThreadID,
				[]map[string]any{buildCodexContextItem(p)},
			); injectErr != nil {
				writeAPIError(w, http.StatusBadGateway, injectErr)
				return
			}
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func handleCodexTurnStart(p routeParams) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if p.CodexBridge == nil {
			http.Error(w, "codex bridge unavailable", http.StatusServiceUnavailable)
			return
		}

		var request codexTurnStartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeAPIError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
			return
		}
		prompt := strings.TrimSpace(request.Prompt)
		if prompt == "" {
			writeAPIError(w, http.StatusBadRequest, fmt.Errorf("prompt is required"))
			return
		}

		threadID := strings.TrimSpace(request.ThreadID)
		if threadID == "" {
			if snapshotThread := p.CodexBridge.Snapshot().Thread; snapshotThread != nil {
				threadID = snapshotThread.ID
			}
		}
		if threadID == "" {
			writeAPIError(w, http.StatusBadRequest, fmt.Errorf("thread_id is required"))
			return
		}

		if request.InjectContext {
			if injectErr := p.CodexBridge.InjectThreadItems(
				r.Context(),
				threadID,
				buildCodexTurnContextItems(p, prompt),
			); injectErr != nil {
				writeAPIError(w, http.StatusBadGateway, injectErr)
				return
			}
		}

		result, err := p.CodexBridge.StartTurn(r.Context(), codexappserver.TurnStartParams{
			ThreadID:       threadID,
			Input:          []map[string]any{buildTextInputItem(prompt)},
			CWD:            strings.TrimSpace(request.CWD),
			ApprovalPolicy: codexApprovalPolicy(request.ApprovalPolicy),
			SandboxType:    codexSandboxType(request.SandboxType),
			Model:          strings.TrimSpace(request.Model),
			Effort:         strings.TrimSpace(request.Effort),
			Summary:        strings.TrimSpace(request.Summary),
		})
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func buildCodexDeveloperInstructions(extra string) string {
	base := strings.TrimSpace(`
You are running inside tunnel-client's Codex bridge MVP.
The browser talks only to tunnel-client; tunnel-client supervises codex app-server over stdio.
Do not tell the user to connect the browser directly to codex app-server.
Treat any injected tunnel context as authoritative runtime state for tunnel-client, the tunnel, and proxy routing.
Keep streamed progress updates concise and concrete because the admin UI renders incremental deltas directly.
`)
	if extra == "" {
		return base
	}
	if strings.Contains(extra, base) {
		return extra
	}
	return base + "\n\n" + extra
}

func buildCodexContextItem(p routeParams) map[string]any {
	status := buildStatus(p)
	system := buildSystem(p)
	codex := p.CodexBridge.Snapshot()

	lines := []string{
		"Tunnel context injected by tunnel-client.",
		"Use this as current runtime context for this bridge session.",
		fmt.Sprintf("tunnel_client.version=%s", valueOrDash(status.Version)),
		fmt.Sprintf("tunnel.health_addr=%s", valueOrDash(status.HealthListenAddr)),
		fmt.Sprintf("tunnel.control_plane_base_url=%s", valueOrDash(status.ControlPlaneBaseURL)),
		fmt.Sprintf("tunnel.control_plane_tunnel_id=%s", valueOrDash(status.ControlPlaneTunnelID)),
		fmt.Sprintf("tunnel.name=%s", valueOrDash(tunnelMetadataName(status.TunnelMetadata))),
		fmt.Sprintf("tunnel.description=%s", valueOrDash(tunnelMetadataDescription(status.TunnelMetadata))),
		fmt.Sprintf("tunnel.mcp_server_url=%s", valueOrDash(status.MCPServerURL)),
		fmt.Sprintf("tunnel.raw_http_logging_enabled=%t", status.RawHTTPLoggingEnabled),
		fmt.Sprintf("bridge.auth_method=%s", valueOrDash(codex.AuthMethod)),
	}
	if codex.Account != nil {
		lines = append(lines,
			fmt.Sprintf("bridge.account_type=%s", valueOrDash(codex.Account.Type)),
			fmt.Sprintf("bridge.account_email=%s", valueOrDash(codex.Account.Email)),
			fmt.Sprintf("bridge.account_plan_type=%s", valueOrDash(codex.Account.PlanType)),
		)
	}
	if system.MainChannelProbeStatus != "" {
		lines = append(lines, fmt.Sprintf("mcp.main_probe_status=%s", system.MainChannelProbeStatus))
	}
	if system.MainChannelProbeError != "" {
		lines = append(lines, fmt.Sprintf("mcp.main_probe_error=%s", system.MainChannelProbeError))
	}
	if status.ControlPlaneRoute != nil {
		lines = append(lines,
			fmt.Sprintf("control_plane.route_mode=%s", valueOrDash(status.ControlPlaneRoute.RouteMode)),
			fmt.Sprintf("control_plane.proxy_id=%s", valueOrDash(status.ControlPlaneRoute.ProxyID)),
			fmt.Sprintf("control_plane.proxy_url=%s", valueOrDash(status.ControlPlaneRoute.ProxyURL)),
			fmt.Sprintf("control_plane.proxy_source=%s", valueOrDash(status.ControlPlaneRoute.ProxySource)),
		)
	}
	if len(status.Warnings) > 0 {
		lines = append(lines, "warnings="+strings.Join(status.Warnings, " | "))
	}
	return buildCodexDeveloperItem(strings.Join(lines, "\n"))
}

func buildCodexTurnContextItems(p routeParams, prompt string) []map[string]any {
	items := []map[string]any{buildCodexContextItem(p)}
	if item := buildCodexKnowledgeItem(prompt); item != nil {
		items = append(items, item)
	}
	return items
}

func buildCodexKnowledgeItem(prompt string) map[string]any {
	parts := []string{
		strings.TrimSpace(assistantkb.BuildPromptContext(prompt)),
		strings.TrimSpace(pluginsbundle.BuildTunnelMCPPromptContext(prompt)),
	}
	text := strings.Join(compactCodexKnowledgeParts(parts), "\n\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return buildCodexDeveloperItem(text)
}

func compactCodexKnowledgeParts(parts []string) []string {
	compacted := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		compacted = append(compacted, part)
	}
	return compacted
}

func buildCodexDeveloperItem(text string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": "developer",
		"content": []map[string]any{
			{
				"type": "input_text",
				"text": text,
			},
		},
	}
}

func buildTextInputItem(prompt string) map[string]any {
	return map[string]any{
		"type": "text",
		"text": prompt,
	}
}

func codexState(snapshot codexappserver.Snapshot) string {
	switch {
	case snapshot.Account != nil && strings.TrimSpace(snapshot.Account.Type) != "":
		return "ready"
	case snapshot.RequiresOpenAIAuth != nil && *snapshot.RequiresOpenAIAuth:
		return "logged_out"
	case snapshot.Starting:
		return "starting"
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return "codex_missing"
	}
	if _, err := execCommandOutput("codex", "app-server", "--help"); err != nil {
		return "app_server_unsupported"
	}
	if strings.TrimSpace(snapshot.LastError) != "" {
		return "error"
	}
	if snapshot.Ready {
		return "ready"
	}
	return "not_ready"
}

func execCommandOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}

func codexApprovalPolicy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultCodexApprovalPolicy
	}
	return raw
}

func codexSandboxType(raw string) string {
	switch strings.TrimSpace(raw) {
	case "":
		return defaultCodexSandboxType
	case "dangerFullAccess":
		return "danger-full-access"
	case "workspaceWrite":
		return "workspace-write"
	case "readOnly":
		return "read-only"
	default:
		return strings.TrimSpace(raw)
	}
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	if err == nil {
		http.Error(w, http.StatusText(status), status)
		return
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func tunnelMetadataName(m *controlplane.TunnelMetadata) string {
	if m == nil {
		return ""
	}
	return m.Name
}

func tunnelMetadataDescription(m *controlplane.TunnelMetadata) string {
	if m == nil {
		return ""
	}
	return m.Description
}
