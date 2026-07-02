package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"go.openai.org/api/tunnel-client/pkg/localproxy"
	"go.openai.org/api/tunnel-client/pkg/types"
)

func newDevCommand(stdout io.Writer, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "dev",
		Short:         "Developer helpers for local tunnel-client validation",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.AddCommand(newDevMCPStubCommand(stdout, stderr))
	cmd.AddCommand(newDevProxyCommand(stdout, stderr))
	return cmd
}

func newDevProxyCommand(stdout io.Writer, stderr io.Writer) *cobra.Command {
	var (
		listenAddr            string
		listenUnixSocket      string
		tunnelID              types.TunnelID
		mcpServerURLs         []string
		mcpCommands           []string
		profile               string
		profileFile           string
		profileDir            string
		healthListenAddr      string
		healthURLFile         string
		urlFile               string
		backend               localproxy.BackendName
		engineQueueBackend    localproxy.QueueBackendName
		engineRedisURL        string
		printJSON             bool
		duration              time.Duration
		readinessTimeout      time.Duration
		responseTimeout       time.Duration
		clientLastSeenTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run a local in-memory tunnel control plane plus tunnel-client",
		Long: `Run a local in-memory tunnel control plane and an in-process tunnel-client runtime.

This mode is for local MCP integration tests that need an MCP URL without a
hosted tunnel control plane or a separate control-plane process.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("unexpected arguments: %s", strings.Join(args, " "))
			}
			if listenUnixSocket != "" {
				if cmd.Flags().Changed("listen") {
					return errors.New("--listen and --listen-unix-socket are mutually exclusive")
				}
				listenAddr = ""
			}
			if engineRedisURL == "" {
				engineRedisURL = os.Getenv("TUNNEL_ENGINE_REDIS_URL")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			proxy, err := localproxy.Start(ctx, localproxy.Options{
				ListenAddr:            listenAddr,
				ListenUnixSocket:      listenUnixSocket,
				TunnelID:              tunnelID,
				MCPServerURLs:         mcpServerURLs,
				MCPCommands:           mcpCommands,
				Profile:               profile,
				ProfileFile:           profileFile,
				ProfileDir:            profileDir,
				HealthListenAddr:      healthListenAddr,
				HealthURLFile:         healthURLFile,
				URLFile:               urlFile,
				Backend:               backend,
				EngineQueueBackend:    engineQueueBackend,
				EngineRedisURL:        engineRedisURL,
				ResponseTimeout:       responseTimeout,
				ClientLastSeenTimeout: clientLastSeenTimeout,
				ReadinessTimeout:      readinessTimeout,
				Stdout:                cmd.OutOrStdout(),
				Stderr:                cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			defer func() {
				_ = proxy.Stop(context.Background())
			}()

			if printJSON {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(proxy.Info()); err != nil {
					return err
				}
			} else {
				if proxy.Info().MCPTransport == "unix" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "MCP Unix socket: %s\n", proxy.Info().MCPUnixSocket)
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "MCP URL path: %s\n", proxy.Info().MCPURLPath)
				} else {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "MCP URL: %s\n", proxy.Info().MCPURL)
				}
			}

			waitCtx := ctx
			cancel := func() {}
			if duration > 0 {
				waitCtx, cancel = context.WithTimeout(ctx, duration)
			}
			defer cancel()
			if err := proxy.Wait(waitCtx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&listenAddr, "listen", localproxy.DefaultListenAddr, "Loopback address for local MCP ingress")
	cmd.Flags().StringVar(&listenUnixSocket, "listen-unix-socket", "", "Unix socket path for local MCP ingress")
	cmd.Flags().StringVar((*string)(&tunnelID), "tunnel-id", localproxy.DefaultTunnelID, "Tunnel id exposed by the local proxy")
	cmd.Flags().StringArrayVar(&mcpServerURLs, "mcp-server-url", nil, "Target MCP server URL; repeat for channel bindings using url=...,channel=...")
	cmd.Flags().StringArrayVar(&mcpCommands, "mcp-command", nil, "Command to launch a stdio MCP server; repeat for channel bindings using command=...,channel=...")
	cmd.Flags().StringVar(&profile, "profile", "", "Tunnel-client profile name to load")
	cmd.Flags().StringVar(&profileFile, "profile-file", "", "Path to a tunnel-client profile YAML file")
	cmd.Flags().StringVar(&profileDir, "profile-dir", "", "Directory containing tunnel-client profiles")
	cmd.Flags().StringVar(&healthListenAddr, "health-listen-addr", localproxy.DefaultHealthListenAddr, "Optional tunnel-client health/admin listen address; omit to run without a health/admin listener")
	cmd.Flags().StringVar(&healthURLFile, "health-url-file", "", "Write the tunnel-client health base URL to this file; enables an ephemeral health/admin listener when --health-listen-addr is omitted")
	cmd.Flags().StringVar(&urlFile, "url-file", "", "Write the local proxy connection JSON to this file")
	cmd.Flags().StringVar((*string)(&backend), "backend", string(localproxy.DefaultBackend), "Local proxy backend: auto, go, or rust")
	cmd.Flags().StringVar((*string)(&engineQueueBackend), "engine-queue-backend", string(localproxy.DefaultQueueBackend), "Local proxy queue backend: inmem or redis")
	cmd.Flags().StringVar(&engineRedisURL, "engine-redis-url", "", "Redis URL for --engine-queue-backend redis; defaults to TUNNEL_ENGINE_REDIS_URL")
	cmd.Flags().BoolVar(&printJSON, "print-json", false, "Print local proxy connection JSON after readiness")
	cmd.Flags().DurationVar(&duration, "duration", 0, "Run for a bounded duration, then exit")
	cmd.Flags().DurationVar(&readinessTimeout, "readiness-timeout", localproxy.DefaultReadinessTimeout, "Maximum time to wait for tunnel-client and MCP readiness")
	cmd.Flags().DurationVar(&responseTimeout, "response-timeout", localproxy.DefaultResponseTimeout, "Maximum time local MCP ingress waits for a tunnel-client response")
	cmd.Flags().DurationVar(&clientLastSeenTimeout, "client-last-seen-timeout", localproxy.DefaultClientLastSeenTimeout, "How recently tunnel-client must poll to be considered connected")
	return cmd
}

func newDevMCPStubCommand(stdout io.Writer, stderr io.Writer) *cobra.Command {
	var (
		listenAddr    string
		serverName    string
		serverVersion string
	)

	cmd := &cobra.Command{
		Use:   "mcp-stub",
		Short: "Run a local MCP + OAuth stub for first-use tunnel-client validation",
		Long: `Run a local Streamable HTTP MCP stub with the OAuth Protected Resource Metadata
and OAuth authorization-server metadata endpoints that tunnel-client expects
during doctor/run validation.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			stub, err := startDevMCPStub(devMCPStubOptions{
				ListenAddr:    listenAddr,
				ServerName:    serverName,
				ServerVersion: serverVersion,
			})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "MCP stub listening on %s\n", stub.BaseURL.String())
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "MCP URL: %s\n", stub.MCPURL())
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Protected resource metadata: %s\n", stub.ProtectedResourceMetadataURL())
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Authorization server metadata: %s\n", stub.AuthorizationServerMetadataURL())
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "These are the demo MCP/OAuth endpoints only. tunnel-client health/ui URLs come from the daemon health listener after `run` starts.\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Next:\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  tunnel-client init --sample sample_mcp_with_dcr --profile sample_mcp_with_dcr --tunnel-id tunnel_... --mcp-server-url %s\n", stub.MCPURL())
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  tunnel-client doctor --profile sample_mcp_with_dcr --explain\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  tunnel-client run --profile sample_mcp_with_dcr\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Try in ChatGPT after the tunnel is connected:\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - Use the server_info tool and summarize the demo MCP server.\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - Use the echo tool with input \"hello from tunnel-client\".\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - Use the uppercase tool on \"openai tunnel\".\n")

			select {
			case <-cmd.Context().Done():
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return stub.Shutdown(shutdownCtx)
			case err := <-stub.errCh:
				stub.errCh = nil
				return err
			}
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().StringVar(&listenAddr, "listen-addr", defaultDevMCPStubListenAddr, "Listen address for the local MCP stub")
	cmd.Flags().StringVar(&serverName, "server-name", defaultDevMCPStubName, "Server name advertised during MCP initialize")
	cmd.Flags().StringVar(&serverVersion, "server-version", defaultDevMCPStubVersion, "Server version advertised during MCP initialize")
	return cmd
}
