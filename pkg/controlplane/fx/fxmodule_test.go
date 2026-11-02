package fx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/controlplane/internal"
	"github.com/openai/tunnel-client/pkg/harpoon"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/mcpserverinfo"
	"github.com/openai/tunnel-client/pkg/metrics"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
	"github.com/openai/tunnel-client/pkg/runtimehealth"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/tunnelctx"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestRegisteredHeaderRulesSuppressControlPlaneRawLogging(t *testing.T) {
	t.Parallel()
	const callerSecret = "synthetic-registered-caller-secret"
	const resultSecret = "synthetic-registered-result-secret"
	for _, flavor := range []string{"full", "runtime"} {
		for _, registration := range []string{"with_target", "registrar"} {
			for _, rich := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/rich=%t", flavor, registration, rich), func(t *testing.T) {
					t.Parallel()
					posted := make(chan string, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch req.URL.Path {
						case "/v1/tunnels/test-tunnel/poll":
							_, _ = io.WriteString(w, `{"commands":[{"command_type":"jsonrpc","request_id":"test-request","channel":"harpoon","shard_token":"test-shard","created_at":"2026-01-01T00:00:00Z","meta":{},"jsonrpc":{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"call_target","arguments":{"headers":{"Authorization":"Bearer `+callerSecret+`"}}}}}]}`)
						case "/v1/tunnels/test-tunnel/response":
							body, err := io.ReadAll(req.Body)
							if err != nil {
								t.Errorf("read posted response: %v", err)
							}
							posted <- string(body)
							w.WriteHeader(http.StatusOK)
						default:
							t.Errorf("unexpected control-plane request: %s", req.URL.Path)
							w.WriteHeader(http.StatusNotFound)
						}
					}))
					t.Cleanup(server.Close)
					baseURL, err := url.Parse(server.URL)
					if err != nil {
						t.Fatal(err)
					}
					controlPlaneConfig := &runtimeconfig.ControlPlaneConfig{
						BaseURL: baseURL, TunnelID: "test-tunnel", APIKey: "runtime-key",
					}
					policy := &runtimeconfig.HarpoonTargetTemplate{
						Version: 1, Origin: "https://inventory.example", Method: http.MethodGet, PathTemplate: "/items/{id}",
						Parameters: map[string]runtimeconfig.HarpoonTemplateParameter{
							"id": {Type: "string", Required: true, Pattern: "^[a-z]+$", MaxLength: 32},
						},
					}
					if rich {
						minLength, maxLength := 8, 128
						policy.HeaderRules = []runtimeconfig.HarpoonHeaderRule{{
							Name: "Authorization", Description: "Caller bearer token", Credential: true,
							Validation: &runtimeconfig.HarpoonHeaderValidation{
								Pattern: "^Bearer [A-Za-z0-9_-]+$", MinLength: &minLength, MaxLength: &maxLength,
							},
						}}
					} else {
						policy.AllowedHeaders = []string{"Accept"}
					}
					target := runtimeharpoon.Target{Label: "inventory", Template: policy}
					registrar := runtimeharpoon.WithTarget(target)
					if registration == "registrar" {
						registrar = func(registry *runtimeharpoon.Registry) error { return registry.RegisterTarget(target) }
					} else if flavor == "full" {
						registrar = harpoon.WithTarget(target)
					}
					var logs bytes.Buffer
					logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
					meterProvider := sdkmetric.NewMeterProvider()
					t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })
					var client *internal.TunnelServiceClient
					options := []fx.Option{
						fx.Supply(logger, meterProvider, controlPlaneConfig,
							&runtimeconfig.MCPConfig{TransportKind: runtimeconfig.MCPTransportHTTPStreamable},
							&runtimeconfig.LoggingConfig{HTTPRawUnsafe: true}, &runtimeconfig.HealthConfig{}),
						fx.Provide(newTunnelServiceClient,
							func() *tlsconfig.Bundle { return nil },
							func() runtimehealth.Service { return registeredHeaderRulesHealth{} },
							fx.Annotate(http.NewServeMux, fx.ResultTags(`name:"admin_mux"`))),
						fx.Supply(fx.Annotate(registrar, fx.ResultTags(`group:"harpoon_target_registrars"`))),
						// Request the client first so the test proves its registry
						// dependency completes registration before transport setup.
						fx.Populate(&client),
					}
					if flavor == "full" {
						options = append(options, fx.Supply(&config.HarpoonConfig{}), harpoon.Module)
					} else {
						options = append(options, fx.Supply(&runtimeconfig.HarpoonConfig{}), runtimeharpoon.Module)
					}
					app := fxtest.New(t, options...)
					app.RequireStart()
					t.Cleanup(func() { app.RequireStop() })
					if controlPlaneConfig.SuppressRawHTTPLogging != rich {
						t.Fatalf("suppression = %t, want %t", controlPlaneConfig.SuppressRawHTTPLogging, rich)
					}
					commands, _, err := client.Poll(t.Context(), 1)
					if err != nil || len(commands) != 1 {
						t.Fatalf("poll = %d commands, %v", len(commands), err)
					}
					// Every poll can carry routing or command tokens, so polling
					// suppresses unsafe raw dumps independently of header policy.
					for _, marker := range []string{callerSecret, "test-shard", "raw http request", "raw http response"} {
						if strings.Contains(logs.String(), marker) {
							t.Fatalf("poll raw logging exposed %q (rich=%t)", marker, rich)
						}
					}
					response := types.NewTunnelResponse(types.ChannelHarpoon, json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"value":"`+resultSecret+`"}}`), http.StatusOK, nil)
					ctx := tunnelctx.ContextWithShardToken(t.Context(), "test-shard")
					if _, err := client.PostResponse(ctx, "test-request", response); err != nil {
						t.Fatal(err)
					}
					if body := <-posted; !strings.Contains(body, resultSecret) {
						t.Fatal("control-plane transport did not deliver the response payload")
					}
					// Response delivery retains its existing rich-header policy:
					// it must suppress payload dumps only for registered secrets.
					for _, marker := range []string{resultSecret, "raw http request", "raw http response"} {
						if strings.Contains(logs.String(), marker) == rich {
							t.Fatalf("log marker %q visibility did not match header policy (rich=%t)", marker, rich)
						}
					}
				})
			}
		}
	}
}

type registeredHeaderRulesHealth struct{}

func (registeredHeaderRulesHealth) Addr(time.Duration) (string, error) { return "", nil }

func TestBuildMCPServerInfoHeaderAdvertisesEffectiveBindings(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		cfg            config.MCPConfig
		harpoonEnabled bool
		want           string
	}{
		{
			name:           "stdio main with enabled Harpoon",
			harpoonEnabled: true,
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportStdio,
					Command:       "/private/stdio-command",
				}},
			},
			want: `{"version":2,"channels":[{"name":"main","proc_affinity":true},{"name":"harpoon","stateless":true,"proc_affinity":true}]}`,
		},
		{
			name: "remote streamable HTTP main without Harpoon",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     &url.URL{Scheme: "https", Host: "private.example"},
				}},
			},
			want: `{"version":1,"channels":[{"name":"main"}]}`,
		},
		{
			name: "stateless HTTP main without Harpoon",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					Stateless:     true,
				}},
			},
			want: `{"version":2,"channels":[{"name":"main","stateless":true}]}`,
		},
		{
			name: "stateless capability stays on its configured binding",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{
					{
						Channel:       types.DefaultChannel,
						TransportKind: config.MCPTransportHTTPStreamable,
						Stateless:     true,
					},
					{
						Channel:       types.Channel("secondary"),
						TransportKind: config.MCPTransportStdio,
					},
				},
			},
			want: `{"version":2,"channels":[{"name":"main","stateless":true},{"name":"secondary","proc_affinity":true}]}`,
		},
		{
			name:           "remote streamable HTTP main with enabled Harpoon",
			harpoonEnabled: true,
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
				}},
			},
			want: `{"version":2,"channels":[{"name":"main"},{"name":"harpoon","stateless":true,"proc_affinity":true}]}`,
		},
		{
			name: "legacy in-memory main without Harpoon",
			cfg: config.MCPConfig{
				TransportKind: config.MCPTransportInMemory,
			},
			want: `{"version":1,"channels":[{"name":"main","proc_affinity":true}]}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildMCPServerInfoHeader(&testCase.cfg, testCase.harpoonEnabled, true)
			if err != nil {
				t.Fatalf("buildMCPServerInfoHeader returned error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("header = %s, want %s", got, testCase.want)
			}
			if strings.Contains(got, "private") {
				t.Fatalf("header leaked transport details: %s", got)
			}
		})
	}
}

func TestMCPServerInfoHeaderProviderTracksHarpoonEnablement(t *testing.T) {
	t.Parallel()

	enabled := false
	provider, err := newMCPServerInfoHeaderProviderForPollChannels(
		&config.MCPConfig{TransportKind: config.MCPTransportHTTPStreamable},
		nil,
		func() bool { return enabled },
		true,
	)
	if err != nil {
		t.Fatalf("newMCPServerInfoHeaderProvider returned error: %v", err)
	}

	got, err := provider()
	if err != nil {
		t.Fatalf("provider without Harpoon returned error: %v", err)
	}
	if want := `{"version":1,"channels":[{"name":"main"}]}`; got != want {
		t.Fatalf("provider without Harpoon = %s, want %s", got, want)
	}

	enabled = true
	got, err = provider()
	if err != nil {
		t.Fatalf("provider with Harpoon returned error: %v", err)
	}
	if want := `{"version":2,"channels":[{"name":"main"},{"name":"harpoon","stateless":true,"proc_affinity":true}]}`; got != want {
		t.Fatalf("provider with Harpoon = %s, want %s", got, want)
	}
}

func TestBuildMCPServerInfoHeaderHarpoonOnly(t *testing.T) {
	t.Parallel()
	got, err := buildMCPServerInfoHeader(&config.MCPConfig{AllowNoMain: true}, true, true)
	if err != nil {
		t.Fatalf("buildMCPServerInfoHeader returned error: %v", err)
	}
	if want := `{"version":2,"channels":[{"name":"harpoon","stateless":true,"proc_affinity":true}]}`; got != want {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestBuildMCPServerInfoHeaderLegacyHarpoonFixtureOmitsStateless(t *testing.T) {
	t.Parallel()

	got, err := buildMCPServerInfoHeader(
		&config.MCPConfig{AllowNoMain: true},
		true,
		false,
	)
	if err != nil {
		t.Fatalf("buildMCPServerInfoHeader returned error: %v", err)
	}
	if want := `{"version":1,"channels":[{"name":"harpoon","proc_affinity":true}]}`; got != want {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestMCPServerInfoHeaderProviderFiltersDisabledHarpoon(t *testing.T) {
	t.Parallel()
	provider, err := newMCPServerInfoHeaderProviderForPollChannels(
		&config.MCPConfig{ChannelBindings: []config.MCPChannelBinding{{
			Channel:       types.DefaultChannel,
			TransportKind: config.MCPTransportHTTPStreamable,
		}}},
		&config.ControlPlaneConfig{PollChannelsConfigured: true, PollChannels: []types.Channel{types.DefaultChannel}},
		func() bool { return true },
		true,
	)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	got, err := provider()
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if want := `{"version":1,"channels":[{"name":"main"}]}`; got != want {
		t.Fatalf("header = %s, want %s", got, want)
	}
}

func TestMCPServerInfoHeaderProviderReservesFutureHarpoonChannel(t *testing.T) {
	t.Parallel()

	bindings := make([]config.MCPChannelBinding, 0, mcpserverinfo.MaxChannels)
	bindings = append(bindings, config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
	})
	for index := 1; index < mcpserverinfo.MaxChannels; index++ {
		bindings = append(bindings, config.MCPChannelBinding{
			Channel:       types.Channel(fmt.Sprintf("channel_%02d", index)),
			TransportKind: config.MCPTransportHTTPStreamable,
		})
	}
	cfg := &config.MCPConfig{ChannelBindings: bindings}

	if _, err := newMCPServerInfoHeaderProviderForPollChannels(cfg, nil, func() bool { return false }, true); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("provider with possible Harpoon error = %v, want channel bound error", err)
	}
	if _, err := newMCPServerInfoHeaderProviderForPollChannels(cfg, nil, nil, true); err != nil {
		t.Fatalf("provider without Harpoon registry returned error: %v", err)
	}
}

func TestBuildMCPServerInfoHeaderRejectsInvalidBindings(t *testing.T) {
	t.Parallel()

	tooMany := make([]config.MCPChannelBinding, 0, mcpserverinfo.MaxChannels)
	tooMany = append(tooMany, config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
	})
	for index := 1; index < mcpserverinfo.MaxChannels; index++ {
		tooMany = append(tooMany, config.MCPChannelBinding{
			Channel:       types.Channel(fmt.Sprintf("channel_%02d", index)),
			TransportKind: config.MCPTransportHTTPStreamable,
		})
	}

	testCases := []struct {
		name           string
		cfg            config.MCPConfig
		harpoonEnabled bool
		wantErr        string
	}{
		{
			name: "duplicate canonical channel",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{
					{Channel: types.DefaultChannel, TransportKind: config.MCPTransportHTTPStreamable},
					{Channel: types.Channel(" MAIN "), TransportKind: config.MCPTransportStdio},
				},
			},
			wantErr: "duplicate channel declaration",
		},
		{
			name: "invalid channel",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.Channel("bad/channel"),
					TransportKind: config.MCPTransportHTTPStreamable,
				}},
			},
			wantErr: "invalid channel",
		},
		{
			name: "unsupported transport",
			cfg: config.MCPConfig{
				ChannelBindings: []config.MCPChannelBinding{{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportKind("unknown"),
				}},
			},
			wantErr: "unsupported MCP transport",
		},
		{
			name:           "more than 32 channels with enabled harpoon",
			cfg:            config.MCPConfig{ChannelBindings: tooMany},
			harpoonEnabled: true,
			wantErr:        "exceeds maximum",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildMCPServerInfoHeader(&testCase.cfg, testCase.harpoonEnabled, true)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, testCase.wantErr)
			}
		})
	}
}

func TestRunPollerStartsEvenWhenFetcherBlocks(t *testing.T) {
	t.Parallel()

	queue := make(controlplane.PolledCommandQueue, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	fetcher := &blockingFetcher{started: make(chan struct{}, 1)}
	queueAdapter := &queueAdapter{
		queue:  queue,
		logger: logger,
	}
	meterProvider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
	})

	poller, err := internal.NewPoller(queueAdapter, fetcher, logger, meterProvider.Meter("test"), time.Millisecond*50, 0, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	app := fxtest.New(
		t,
		fx.Supply(logger),
		fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
		fx.Invoke(runPoller),
	)

	app.RequireStart()
	select {
	case <-fetcher.started:
	case <-time.After(time.Second):
		t.Fatal("poller did not start poll loop")
	}
	app.RequireStop()
}

func TestRunPollerWaitsForMCPStartupProbeBeforeFirstPoll(t *testing.T) {
	t.Parallel()

	waitLog := newSignalWriter("waiting for MCP startup probe before first control-plane poll")
	logger := slog.New(slog.NewTextHandler(waitLog, nil))
	probeState := mcpclient.NewProbeState()
	poller := &recordingPoller{started: make(chan struct{})}
	mcpConfig := &config.MCPConfig{StartupWaitTimeout: time.Second}

	app := fxtest.New(
		t,
		fx.Supply(logger, mcpConfig, probeState),
		fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
		fx.Invoke(runPoller),
	)
	app.RequireStart()

	select {
	case <-waitLog.Seen():
	case <-time.After(time.Second):
		t.Fatal("poller did not enter MCP startup wait")
	}
	select {
	case <-poller.started:
		t.Fatal("poller started before MCP startup probe completed")
	default:
	}

	probeState.Set(nil)
	select {
	case <-poller.started:
	case <-time.After(time.Second):
		t.Fatal("poller did not start after MCP startup probe completed")
	}
	app.RequireStop()
}

func TestRunPollerStartsAfterHTTPStartupProbeFailure(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer
			state := mcpclient.NewProbeState()
			probeErr := mcpclient.NewProbeHTTPStatusError(status, errors.New("probe rejected"))
			state.Set(probeErr)
			checkedAt, _, _ := state.Wait(0)
			poller := &recordingPoller{started: make(chan struct{})}
			app := fxtest.New(t,
				fx.Supply(slog.New(slog.NewJSONHandler(&logs, nil)), &config.MCPConfig{StartupWaitTimeout: time.Second}, state),
				fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
				fx.Invoke(runPoller),
			)
			app.RequireStart()
			t.Cleanup(app.RequireStop)
			select {
			case <-poller.started:
			case <-time.After(5 * time.Second):
				t.Fatal("completed HTTP startup probe failure prevented polling")
			}
			app.RequireStop()

			gotCheckedAt, gotErr, done := state.Wait(0)
			if !done || gotCheckedAt != checkedAt || gotErr != probeErr {
				t.Fatalf("poller changed probe result: checkedAt=%v err=%v done=%t", gotCheckedAt, gotErr, done)
			}

			wantLevel, wantMessage, wantError := "WARN", "MCP startup probe completed with failure; starting control-plane poller for compatibility", probeErr.Error()
			if status == http.StatusUnauthorized {
				wantLevel, wantMessage, wantError = "INFO", "MCP server requires authentication; starting control-plane poller", ""
			}
			matched := 0
			for line := range bytes.SplitSeq(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
				var entry struct {
					Level   string `json:"level"`
					Message string `json:"msg"`
					Error   string `json:"error"`
				}
				if err := json.Unmarshal(line, &entry); err != nil {
					t.Fatalf("decode poller log: %v", err)
				}
				if status == http.StatusUnauthorized && (entry.Level == "WARN" || entry.Level == "ERROR") {
					t.Errorf("authentication-required probe logged an unexpected failure: %+v", entry)
				}
				if entry.Message == wantMessage {
					matched++
					if entry.Level != wantLevel || entry.Error != wantError {
						t.Errorf("poller log = %+v, want level=%q error=%q", entry, wantLevel, wantError)
					}
				}
			}
			if matched != 1 {
				t.Fatalf("got %d logs for %q, want one; logs:\n%s", matched, wantMessage, &logs)
			}
		})
	}
}

func TestRunPollerWaitsForStartupCatalog(t *testing.T) {
	t.Parallel()

	waitLog := newSignalWriter("waiting for startup catalog before first control-plane poll")
	logger := slog.New(slog.NewTextHandler(waitLog, nil))
	catalog := hostbus.NewStartupCatalogState()
	poller := &recordingPoller{started: make(chan struct{})}
	app := fxtest.New(t,
		fx.Supply(logger, catalog),
		fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
		fx.Invoke(runPoller),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	select {
	case <-poller.started:
		t.Fatal("poller consumed work before startup target registration completed")
	case <-waitLog.Seen():
	case <-time.After(time.Second):
		t.Fatal("poller did not reach startup catalog wait")
	}
	select {
	case <-poller.started:
		t.Fatal("poller started with a pending startup catalog")
	default:
	}

	// Successful finalization also permits an empty catalog for non-OAuth MCP.
	catalog.Complete(nil)
	select {
	case <-poller.started:
	case <-time.After(time.Second):
		t.Fatal("poller did not start after target registration completed")
	}
}

func TestRunPollerRejectsFailedStartupCatalog(t *testing.T) {
	t.Parallel()

	failureLog := newSignalWriter("startup catalog failed; control-plane polling disabled")
	logger := slog.New(slog.NewTextHandler(failureLog, nil))
	catalog := hostbus.NewStartupCatalogState()
	catalog.Complete(errors.New("invalid OAuth metadata"))
	poller := &recordingPoller{started: make(chan struct{})}
	app := fxtest.New(t,
		fx.Supply(logger, catalog),
		fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
		fx.Invoke(runPoller),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	select {
	case <-poller.started:
		t.Fatal("client with a failed startup catalog consumed work")
	case <-failureLog.Seen():
	case <-time.After(time.Second):
		t.Fatal("failed catalog was not observed")
	}
}

func TestRunPollerCancelsStartupCatalogWait(t *testing.T) {
	t.Parallel()

	waitLog := newSignalWriter("waiting for startup catalog before first control-plane poll")
	logger := slog.New(slog.NewTextHandler(waitLog, nil))
	catalog := hostbus.NewStartupCatalogState()
	poller := &recordingPoller{started: make(chan struct{})}
	app := fxtest.New(t,
		fx.Supply(logger, catalog),
		fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
		fx.Invoke(runPoller),
	)
	app.RequireStart()
	select {
	case <-poller.started:
		app.RequireStop()
		t.Fatal("poller started before cancellation of pending catalog")
	case <-waitLog.Seen():
	case <-time.After(time.Second):
		app.RequireStop()
		t.Fatal("poller did not reach startup catalog wait")
	}
	app.RequireStop()
	select {
	case <-poller.started:
		t.Fatal("poller started during shutdown")
	default:
	}
}

func TestRunPollerExplicitNonHarpoonScopeDoesNotWaitForCatalog(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog := hostbus.NewStartupCatalogState()
	controlPlane := &config.ControlPlaneConfig{PollChannelsConfigured: true, PollChannels: []types.Channel{types.DefaultChannel}}
	poller := &recordingPoller{started: make(chan struct{})}
	app := fxtest.New(t,
		fx.Supply(logger, catalog, controlPlane),
		fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
		fx.Invoke(runPoller),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	select {
	case <-poller.started:
	case <-time.After(time.Second):
		t.Fatal("explicit main-only poller was blocked by unrelated Harpoon discovery")
	}
}

func TestRunPollerPreservesLegacyHostBus(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog := hostbus.NewStartupCatalogState()
	catalog.Complete(errors.New("startup acknowledgement unavailable"))
	poller := &recordingPoller{started: make(chan struct{})}
	app := fxtest.New(t,
		fx.Supply(logger, catalog),
		fx.Supply(fx.Annotate(legacyHostBus{}, fx.As(new(hostbus.HostRegistrationBus)))),
		fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
		fx.Invoke(runPoller),
	)
	app.RequireStart()
	t.Cleanup(app.RequireStop)
	select {
	case <-poller.started:
	case <-time.After(time.Second):
		t.Fatal("legacy host-bus implementation was blocked by unsupported acknowledgement")
	}
}

type legacyHostBus struct{}

func (legacyHostBus) Publish(context.Context, hostbus.URLBundle) error { return nil }
func (legacyHostBus) Close() error                                     { return nil }

func TestStartupCatalogReadinessMatchesPollingEligibility(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		noCatalog  bool
		mainOnly   bool
		legacyBus  bool
		completion error
	}{
		{name: "registration succeeds"},
		{name: "registration fails", completion: errors.New("private registration failure")},
		{name: "catalog absent", noCatalog: true},
		{name: "explicit main only", mainOnly: true},
		{name: "legacy bus", legacyBus: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			catalog := hostbus.NewStartupCatalogState()
			if testCase.noCatalog {
				catalog = nil
			}
			controlPlane := &runtimeconfig.ControlPlaneConfig{}
			if testCase.mainOnly {
				controlPlane.PollChannelsConfigured = true
				controlPlane.PollChannels = []types.Channel{types.DefaultChannel}
			}
			meter := sdkmetric.NewMeterProvider()
			t.Cleanup(func() { _ = meter.Shutdown(context.Background()) })
			var mux *http.ServeMux
			var service runtimehealth.Service
			options := []fx.Option{
				fx.NopLogger,
				runtimehealth.Module,
				fx.Supply(controlPlane, meter, &runtimeconfig.HealthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil))),
				fx.Provide(
					func() metrics.MetricsExporter { return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}) },
					fx.Annotate(newStartupCatalogReadinessGate, fx.ResultTags(`group:"runtime_readiness_gates"`)),
				),
				fx.Populate(&service),
				fx.Invoke(func(p struct {
					fx.In
					Mux *http.ServeMux `name:"admin_mux"`
				}) {
					mux = p.Mux
				}),
			}
			if catalog != nil {
				options = append(options, fx.Supply(catalog))
			}
			if testCase.legacyBus {
				options = append(options, fx.Supply(fx.Annotate(legacyHostBus{}, fx.As(new(hostbus.HostRegistrationBus)))))
			}
			app := fx.New(options...)
			if err := app.Err(); err != nil {
				t.Fatal(err)
			}
			checkReady := func(wantStatus int, wantBody string) {
				t.Helper()
				recorder := httptest.NewRecorder()
				mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
				if recorder.Code != wantStatus || recorder.Body.String() != wantBody {
					t.Fatalf("readyz = %d %q, want %d %q", recorder.Code, recorder.Body.String(), wantStatus, wantBody)
				}
			}
			bypass := testCase.noCatalog || testCase.mainOnly || testCase.legacyBus
			if bypass {
				checkReady(http.StatusOK, "ready")
			} else {
				checkReady(http.StatusServiceUnavailable, "startup catalog pending")
			}
			catalog.Complete(testCase.completion)
			if testCase.completion != nil && !bypass {
				checkReady(http.StatusServiceUnavailable, "startup catalog failed")
			} else {
				checkReady(http.StatusOK, "ready")
			}
		})
	}
}

func TestWaitForMCPStartupBeforePollingFailsOpenAfterProbeFailure(t *testing.T) {
	t.Parallel()

	state := mcpclient.NewProbeState()
	state.Set(errors.New("mcp startup wait timed out"))
	if !waitForMCPStartupBeforePolling(
		context.Background(),
		&config.MCPConfig{StartupWaitTimeout: time.Second},
		state,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	) {
		t.Fatal("completed startup wait failure must fail open for polling")
	}
}

func TestWaitForMCPStartupBeforePollingStopsOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitForMCPStartupBeforePolling(
		ctx,
		&config.MCPConfig{StartupWaitTimeout: time.Second},
		mcpclient.NewProbeState(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	) {
		t.Fatal("canceled startup wait must not start polling")
	}
}

type blockingFetcher struct {
	started chan struct{}
}

type recordingPoller struct {
	started chan struct{}
	once    sync.Once
}

func (p *recordingPoller) Run(ctx context.Context) {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
}

type signalWriter struct {
	needle string
	seen   chan struct{}
	once   sync.Once
}

func newSignalWriter(needle string) *signalWriter {
	return &signalWriter{needle: needle, seen: make(chan struct{})}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.needle) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

func (w *signalWriter) Seen() <-chan struct{} {
	return w.seen
}

func (f *blockingFetcher) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	select {
	case f.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, "", ctx.Err()
}
