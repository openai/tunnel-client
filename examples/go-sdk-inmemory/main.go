package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	tunnelclient "github.com/openai/tunnel-client"
)

const readyFileEnv = "TUNNEL_CLIENT_SDK_READY_FILE"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "tunnel-client-sdk-example",
		Version: "1.0.0",
	}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Return the caller's message through the OpenAI Tunnel control plane.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, map[string]string, error) {
		message := fmt.Sprintf("Echo: %s", args["message"])
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: message}},
		}, map[string]string{"message": message}, nil
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Run(serverCtx, serverTransport)
	}()

	cfg, err := configFromEnvironment()
	if err != nil {
		return err
	}
	client, err := tunnelclient.New(cfg, clientTransport)
	if err != nil {
		return err
	}
	if err := client.Start(ctx); err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Stop(stopCtx)
	}()

	if err := client.WaitUntilReady(ctx); err != nil {
		return fmt.Errorf("wait for tunnel control plane: %w", err)
	}
	if err := writeReadyFile(os.Getenv(readyFileEnv)); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "tunnel-client SDK example connected")

	select {
	case <-ctx.Done():
		return nil
	case <-client.Done():
		return errors.New("tunnel-client SDK runtime stopped")
	case err := <-serverDone:
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("MCP server stopped: %w", err)
	}
}

func configFromEnvironment() (tunnelclient.Config, error) {
	tunnelID := strings.TrimSpace(os.Getenv("CONTROL_PLANE_TUNNEL_ID"))
	if tunnelID == "" {
		return tunnelclient.Config{}, errors.New("CONTROL_PLANE_TUNNEL_ID is required")
	}
	apiKey := strings.TrimSpace(os.Getenv("CONTROL_PLANE_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if apiKey == "" {
		return tunnelclient.Config{}, errors.New("CONTROL_PLANE_API_KEY or OPENAI_API_KEY is required")
	}

	pollTimeout, err := durationFromEnvironment("CONTROL_PLANE_POLL_TIMEOUT")
	if err != nil {
		return tunnelclient.Config{}, err
	}
	extraHeaders, err := headersFromEnvironment("CONTROL_PLANE_EXTRA_HEADERS")
	if err != nil {
		return tunnelclient.Config{}, err
	}
	return tunnelclient.Config{
		TunnelID:                 tunnelID,
		APIKey:                   apiKey,
		ControlPlaneBaseURL:      os.Getenv("CONTROL_PLANE_BASE_URL"),
		OrganizationID:           os.Getenv("CONTROL_PLANE_ORGANIZATION_ID"),
		ControlPlaneExtraHeaders: extraHeaders,
		PollTimeout:              pollTimeout,
	}, nil
}

func durationFromEnvironment(name string) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return duration, nil
}

func writeReadyFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte("ready\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", readyFileEnv, err)
	}
	return nil
}

func headersFromEnvironment(name string) (map[string]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	headers := make(map[string]string)
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';'
	}) {
		key, value, ok := strings.Cut(entry, ":")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("invalid %s entry %q; expected Key: Value", name, strings.TrimSpace(entry))
		}
		headers[key] = value
	}
	return headers, nil
}
