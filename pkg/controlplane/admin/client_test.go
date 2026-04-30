package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/version"
)

func TestAdminTunnelClientCreateAndGet(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tunnels":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-key" {
				t.Fatalf("unexpected Authorization header %q", got)
			}
			w.Header().Set("X-Request-Id", "req_create_123")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"tunnel_1","name":"n","description":"d","creator":"u","organization_ids":["org1"],"workspace_ids":["ws1"]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tunnels/tunnel_1":
			w.Header().Set("X-Request-Id", "req_get_123")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"tunnel_1","name":"n"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	cfg := &config.AdminConfig{
		BaseURL:  mustParseURL(t, server.URL),
		AdminKey: "admin-key",
	}
	client, err := NewAdminTunnelClient(cfg)
	if err != nil {
		t.Fatalf("NewAdminTunnelClient: %v", err)
	}

	ctx := context.Background()

	created, err := client.CreateTunnel(ctx, TunnelCreateRequest{Name: "n", Description: "d"})
	if err != nil {
		t.Fatalf("CreateTunnel: %v", err)
	}
	if created.ID != "tunnel_1" || created.Creator != "u" || created.RequestID != "req_create_123" {
		t.Fatalf("unexpected create response %+v", created)
	}

	fetched, err := client.GetTunnel(ctx, "tunnel_1")
	if err != nil {
		t.Fatalf("GetTunnel: %v", err)
	}
	if fetched.Name != "n" || fetched.RequestID != "req_get_123" {
		t.Fatalf("unexpected get response %+v", fetched)
	}
}

func TestAdminTunnelClientUpdateEncodesEmptySlices(t *testing.T) {
	t.Parallel()

	var captured struct {
		Method string
		Path   string
		Body   map[string]any
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&captured.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"tunnel_2","name":"x"}`))
	}))
	t.Cleanup(server.Close)

	cfg := &config.AdminConfig{
		BaseURL:  mustParseURL(t, server.URL),
		AdminKey: "key",
	}
	client, err := NewAdminTunnelClient(cfg)
	if err != nil {
		t.Fatalf("NewAdminTunnelClient: %v", err)
	}

	empty := []string{}
	name := "new"
	req := TunnelUpdateRequest{
		Name:            &name,
		OrganizationIDs: &empty,
	}

	if _, err := client.UpdateTunnel(context.Background(), "tunnel_2", req); err != nil {
		t.Fatalf("UpdateTunnel: %v", err)
	}

	if captured.Method != http.MethodPost || captured.Path != "/v1/tunnels/tunnel_2" {
		t.Fatalf("unexpected request %s %s", captured.Method, captured.Path)
	}
	if got := captured.Body["organization_ids"]; got == nil {
		t.Fatalf("expected organization_ids to be present, got %#v", captured.Body)
	}
}

func TestAdminTunnelClientSendsIdentityHeadersForAllMethods(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertIdentityHeaders(t, r)
		seen[r.Method+" "+r.URL.Path] = true
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tunnels":
			_, _ = w.Write([]byte(`{"tunnels":[]}`))
		default:
			_, _ = w.Write([]byte(`{"id":"tunnel_1","name":"n"}`))
		}
	}))
	t.Cleanup(server.Close)

	cfg := &config.AdminConfig{
		BaseURL:  mustParseURL(t, server.URL),
		AdminKey: "key",
	}
	client, err := NewAdminTunnelClient(cfg)
	if err != nil {
		t.Fatalf("NewAdminTunnelClient: %v", err)
	}

	ctx := context.Background()
	if _, err := client.CreateTunnel(ctx, TunnelCreateRequest{Name: "n"}); err != nil {
		t.Fatalf("CreateTunnel: %v", err)
	}
	if _, err := client.GetTunnel(ctx, "tunnel_1"); err != nil {
		t.Fatalf("GetTunnel: %v", err)
	}
	if _, err := client.ListTunnels(ctx, "org_1", "", ""); err != nil {
		t.Fatalf("ListTunnels: %v", err)
	}
	if _, err := client.UpdateTunnel(ctx, "tunnel_1", TunnelUpdateRequest{}); err != nil {
		t.Fatalf("UpdateTunnel: %v", err)
	}
	if _, err := client.DeleteTunnel(ctx, "tunnel_1"); err != nil {
		t.Fatalf("DeleteTunnel: %v", err)
	}

	for _, want := range []string{
		"POST /v1/tunnels",
		"GET /v1/tunnels/tunnel_1",
		"GET /v1/tunnels",
		"POST /v1/tunnels/tunnel_1",
		"DELETE /v1/tunnels/tunnel_1",
	} {
		if !seen[want] {
			t.Fatalf("missing request %s; saw %#v", want, seen)
		}
	}
}

func TestAdminTunnelClientErrorIncludesRequestID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_test_123")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(server.Close)

	cfg := &config.AdminConfig{
		BaseURL:  mustParseURL(t, server.URL),
		AdminKey: "key",
	}
	client, err := NewAdminTunnelClient(cfg)
	if err != nil {
		t.Fatalf("NewAdminTunnelClient: %v", err)
	}

	_, err = client.GetTunnel(context.Background(), "id")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var requestErr *RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("expected RequestError, got %T", err)
	}
	if requestErr.RequestID != "req_test_123" || requestErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("unexpected request error details: %+v", requestErr)
	}
	if got := err.Error(); !containsAll(got, "500", "boom", "req_test_123") {
		t.Fatalf("error missing request id or details: %s", got)
	}
}

func TestAdminTunnelClientDeleteUnsupportedHostExplainsProblem(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid URL (DELETE /v1/tunnels/tunnel_123)"}}`))
	}))
	t.Cleanup(server.Close)

	cfg := &config.AdminConfig{
		BaseURL:  mustParseURL(t, server.URL),
		AdminKey: "key",
	}
	client, err := NewAdminTunnelClient(cfg)
	if err != nil {
		t.Fatalf("NewAdminTunnelClient: %v", err)
	}

	_, err = client.DeleteTunnel(context.Background(), "tunnel_123")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got := err.Error(); !containsAll(got, "delete is not exposed on this control-plane base URL yet", "DELETE /v1/tunnels/tunnel_123") {
		t.Fatalf("unexpected delete error: %s", got)
	}
}

func assertIdentityHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("User-Agent"); got != version.UserAgent {
		t.Fatalf("unexpected User-Agent header %q", got)
	}
	if got := r.Header.Get("X-Tunnel-Client-Name"); got != version.ClientName {
		t.Fatalf("unexpected X-Tunnel-Client-Name header %q", got)
	}
	if got := r.Header.Get("X-Tunnel-Client-Version"); got != version.Version {
		t.Fatalf("unexpected X-Tunnel-Client-Version header %q", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return parsed
}
