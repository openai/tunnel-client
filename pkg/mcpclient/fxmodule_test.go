package mcpclient

import (
	"bytes"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func TestNewMcpClient_DefaultTransport(t *testing.T) {
	params := clientParams{
		Config: &config.MCPConfig{
			ServerURL:             mustParseURL(t, "https://example.invalid"),
			MaxConcurrentRequests: 10,
		},
		Logging: &config.LoggingConfig{
			HTTPRawUnsafe: false,
		},
		Logger:        slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
		TransportProviders: []TransportProvider{
			newStreamableTransportProvider(),
		},
	}
	outputs, err := newMcpClient(params)
	if err != nil {
		t.Fatalf("newMcpClient returned error: %v", err)
	}

	if outputs.Client == nil {
		t.Fatalf("expected client to be non-nil")
	}

	if _, ok := outputs.Transport.(*mcp.StreamableClientTransport); !ok {
		t.Fatalf("expected raw transport to be *mcp.StreamableClientTransport; got %T", outputs.Transport)
	}
}

func TestNewMcpClient_LoggingTransport(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	params := clientParams{
		Config: &config.MCPConfig{
			ServerURL:             mustParseURL(t, "https://example.invalid"),
			MaxConcurrentRequests: 10,
		},
		Logging: &config.LoggingConfig{
			HTTPRawUnsafe: true,
			Level:         slog.LevelDebug,
		},
		Logger:        logger,
		MeterProvider: sdkmetric.NewMeterProvider(),
		TransportProviders: []TransportProvider{
			newStreamableTransportProvider(),
		},
	}
	outputs, err := newMcpClient(params)
	if err != nil {
		t.Fatalf("newMcpClient returned error: %v", err)
	}

	loggingTransport, ok := outputs.Transport.(*mcp.LoggingTransport)
	if !ok {
		t.Fatalf("expected raw transport to be logging transport; got %T", outputs.Transport)
	}

	if _, ok := loggingTransport.Transport.(*mcp.StreamableClientTransport); !ok {
		t.Fatalf("expected underlying transport to be *mcp.StreamableClientTransport; got %T", loggingTransport.Transport)
	}

	writer, ok := loggingTransport.Writer.(slogWriter)
	if !ok {
		t.Fatalf("expected writer to be slogWriter; got %T", loggingTransport.Writer)
	}

	if writer.logger == nil {
		t.Fatalf("expected writer logger to be configured")
	}

	if _, err := loggingTransport.Writer.Write([]byte("read: {}")); err != nil {
		t.Fatalf("unexpected error writing log: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "read: {}") {
		t.Fatalf("expected log output to contain message; got %q", output)
	}
	if !strings.Contains(output, tclog.FieldComponent+"="+tclog.ComponentMcpClient) {
		t.Fatalf("expected log output to contain component field; got %q", output)
	}
	if !strings.Contains(output, "transport=raw_http") {
		t.Fatalf("expected log output to include transport marker; got %q", output)
	}
}

func TestNewMcpClient_LoggingTransportRequiresDebugLevel(t *testing.T) {
	params := clientParams{
		Config: &config.MCPConfig{
			ServerURL:             mustParseURL(t, "https://example.invalid"),
			MaxConcurrentRequests: 10,
		},
		Logging: &config.LoggingConfig{
			HTTPRawUnsafe: true,
			Level:         slog.LevelInfo,
		},
		Logger:        slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
		TransportProviders: []TransportProvider{
			newStreamableTransportProvider(),
		},
	}
	outputs, err := newMcpClient(params)
	if err != nil {
		t.Fatalf("newMcpClient returned error: %v", err)
	}

	if _, ok := outputs.Transport.(*mcp.StreamableClientTransport); !ok {
		t.Fatalf("expected raw transport to be streamable; got %T", outputs.Transport)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return parsed
}
