package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/openai/tunnel-client/pkg/codexplugin"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/oauth"
)

type doctorStatus string

const (
	doctorStatusPass doctorStatus = "PASS"
	doctorStatusFail doctorStatus = "FAIL"
	doctorStatusSkip doctorStatus = "SKIP"
)

type doctorCheck struct {
	ID       string       `json:"id"`
	Status   doctorStatus `json:"status"`
	Summary  string       `json:"summary"`
	Why      string       `json:"why,omitempty"`
	Evidence []string     `json:"evidence,omitempty"`
	Next     []string     `json:"next,omitempty"`
}

type doctorReport struct {
	Result       string        `json:"result"`
	FailedChecks []string      `json:"failed_checks,omitempty"`
	Next         string        `json:"next,omitempty"`
	Checks       []doctorCheck `json:"checks"`
}

type doctorHealthListenerResult struct {
	Check doctorCheck
}

func newDoctorCommand(lookupEnv func(string) (string, bool), stdout io.Writer, stderr io.Writer) *cobra.Command {
	var explain bool
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate tunnel-client configuration before starting the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			report := runDoctor(cmd.Flags(), lookupEnv)
			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return err
				}
			} else {
				writeDoctorReport(cmd.OutOrStdout(), report, explain)
			}
			if report.Result == "fail" {
				return silentExitError{code: 2}
			}
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	config.RegisterFlags(cmd.PersistentFlags())
	cmd.Flags().BoolVar(&explain, "explain", false, "Explain why failed checks matter and what to do next")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON output")
	return cmd
}

func runDoctor(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) doctorReport {
	source, err := config.ResolveConfigSource(fs, lookupEnv)
	checks := make([]doctorCheck, 0, 10)
	if err != nil {
		checks = append(checks, doctorCheck{
			ID:      "config_source",
			Status:  doctorStatusFail,
			Summary: err.Error(),
			Why:     "tunnel-client needs to know which config or profile to validate before it can explain anything else.",
			Evidence: []string{
				err.Error(),
			},
			Next: []string{
				"set --profile <name>, --config /path/to/config.yaml, or the matching environment variable",
			},
		})
		checks = append(checks, doctorCanonicalWebPropertiesChecks()...)
		return finalizeDoctorReport(checks, source)
	}

	checks = append(checks, doctorCheck{
		ID:      "config_source",
		Status:  doctorStatusPass,
		Summary: doctorConfigSourceSummary(source),
	})

	if source.Path != "" {
		if _, err := os.ReadFile(source.Path); err != nil {
			checks = append(checks, doctorCheck{
				ID:      "profile_load",
				Status:  doctorStatusFail,
				Summary: err.Error(),
				Why:     "tunnel-client cannot validate a missing or unreadable profile file.",
				Evidence: []string{
					source.Path,
					err.Error(),
				},
				Next: []string{
					fmt.Sprintf("ensure %s exists and is readable", source.Path),
				},
			})
			checks = append(checks, doctorCanonicalWebPropertiesChecks()...)
			return finalizeDoctorReport(checks, source)
		}
		checks = append(checks, doctorCheck{
			ID:      "profile_load",
			Status:  doctorStatusPass,
			Summary: source.Path,
		})
	} else {
		checks = append(checks, doctorCheck{
			ID:      "profile_load",
			Status:  doctorStatusPass,
			Summary: "flags/environment only",
		})
	}

	cfg, err := config.LoadFromFlagSet(fs, lookupEnv)
	if err != nil {
		checks = append(checks, mapConfigErrorToDoctorCheck(err, source))
		checks = append(checks, doctorCanonicalWebPropertiesChecks()...)
		checks = append(checks, doctorCodexCheck(lookupEnv))
		return finalizeDoctorReport(checks, source)
	}

	checks = append(checks, doctorCheck{
		ID:      "tunnel_id",
		Status:  doctorStatusPass,
		Summary: cfg.ControlPlane.TunnelID.String(),
	})
	checks = append(checks, doctorCheck{
		ID:      "control_plane_api_key",
		Status:  doctorStatusPass,
		Summary: doctorAPIKeySummary(fs, lookupEnv),
	})
	checks = append(checks, doctorCanonicalWebPropertiesChecks()...)

	mainBinding := cfg.MCP.MainChannelBinding()
	if mainBinding != nil {
		if mainBinding.ServerURL != nil {
			checks = append(checks, doctorCheck{
				ID:      "mcp_target",
				Status:  doctorStatusPass,
				Summary: mainBinding.ServerURL.String(),
			})
			checks = append(checks, doctorReachabilityCheck(mainBinding.ServerURL))
			checks = append(checks, doctorOAuthMetadataCheck(mainBinding.ServerURL))
		} else {
			checks = append(checks, doctorCheck{
				ID:      "mcp_target",
				Status:  doctorStatusPass,
				Summary: mainBinding.Command,
			})
			checks = append(checks, doctorStdioCommandCheck(mainBinding.Command))
			checks = append(checks, doctorCheck{
				ID:      "mcp_server_reachable",
				Status:  doctorStatusSkip,
				Summary: "stdio targets are not probed over the network",
			})
			checks = append(checks, doctorCheck{
				ID:      "oauth_metadata",
				Status:  doctorStatusSkip,
				Summary: "stdio targets do not expose OAuth metadata URLs",
			})
		}
	}

	healthResult := doctorHealthListenerCheck(cfg.Health)
	checks = append(checks, healthResult.Check)
	checks = append(checks, doctorUICheck(cfg.Health, healthResult.Check.Status))
	checks = append(checks, doctorCodexCheck(lookupEnv))
	return finalizeDoctorReport(checks, source)
}

func finalizeDoctorReport(checks []doctorCheck, source config.ConfigSource) doctorReport {
	report := doctorReport{
		Result: "ok",
		Checks: checks,
		Next:   doctorNextCommand(source),
	}
	for _, check := range checks {
		if check.Status == doctorStatusFail {
			report.Result = "fail"
			report.FailedChecks = append(report.FailedChecks, check.ID)
		}
	}
	if report.Result == "fail" {
		report.Next = ""
	}
	return report
}

func writeDoctorReport(w io.Writer, report doctorReport, explain bool) {
	for _, check := range report.Checks {
		_, _ = fmt.Fprintf(w, "CHECK %-24s %-4s %s\n", check.ID, check.Status, check.Summary)
	}
	if report.Result == "ok" {
		_, _ = fmt.Fprintln(w, "\nRESULT ok")
		if report.Next != "" {
			_, _ = fmt.Fprintf(w, "NEXT   %s\n", report.Next)
		}
	} else {
		_, _ = fmt.Fprintln(w, "\nRESULT fail")
		if len(report.FailedChecks) > 0 {
			_, _ = fmt.Fprintf(w, "FAILED_CHECKS %s\n", strings.Join(report.FailedChecks, ","))
		}
		_, _ = fmt.Fprintln(w, "EXIT_CODE 2")
	}
	if !explain {
		return
	}
	for _, check := range report.Checks {
		if check.Status == doctorStatusPass {
			continue
		}
		_, _ = fmt.Fprintf(w, "\nCHECK %s   %s\n", check.ID, check.Status)
		if check.Why != "" {
			_, _ = fmt.Fprintf(w, "Why this matters:\n  %s\n", check.Why)
		}
		if len(check.Evidence) > 0 {
			_, _ = fmt.Fprintln(w, "\nEvidence:")
			for _, line := range check.Evidence {
				_, _ = fmt.Fprintf(w, "  - %s\n", line)
			}
		}
		if len(check.Next) > 0 {
			_, _ = fmt.Fprintln(w, "\nWhat to do next:")
			for i, line := range check.Next {
				_, _ = fmt.Fprintf(w, "  %d. %s\n", i+1, line)
			}
		}
	}
}

func doctorConfigSourceSummary(source config.ConfigSource) string {
	switch {
	case source.ProfileFile && source.ProfilePath != "":
		return "profile file: " + source.ProfilePath
	case source.ProfileName != "":
		return "profile: " + source.ProfileName
	case source.Path != "":
		return source.Path
	default:
		return "flags/environment only"
	}
}

func doctorNextCommand(source config.ConfigSource) string {
	switch {
	case source.ProfileFile && source.ProfilePath != "":
		return fmt.Sprintf("tunnel-client run --profile-file %s", source.ProfilePath)
	case source.ProfileName != "":
		return fmt.Sprintf("tunnel-client run --profile %s", source.ProfileName)
	case source.Path != "":
		return fmt.Sprintf("tunnel-client run --config %s", source.Path)
	default:
		return "tunnel-client run"
	}
}

func mapConfigErrorToDoctorCheck(err error, source config.ConfigSource) doctorCheck {
	message := err.Error()
	check := doctorCheck{
		ID:       "config_validation",
		Status:   doctorStatusFail,
		Summary:  message,
		Evidence: []string{message},
	}
	switch {
	case strings.Contains(message, "tunnel ID"):
		check.ID = "tunnel_id"
		check.Why = "tunnel-client cannot register or poll the control plane without a valid tunnel id."
		check.Next = []string{
			fmt.Sprintf("create or inspect the tunnel in %s", canonicalTunnelsManagementURL),
			"run `tunnel-client admin tunnels get <tunnel_id>` if you already know the tunnel id; this read-only lookup works with the runtime key",
			fmt.Sprintf("if you need admin CRUD or discovery, create or inspect an admin key in %s and then run `tunnel-client admin tunnels create --help` or `tunnel-client admin tunnels list --help`", canonicalAdminAPIKeysURL),
			"once you have a tunnel id, create a first profile with `tunnel-client init --sample sample_mcp_with_dcr --profile sample_mcp_with_dcr --tunnel-id tunnel_... --mcp-server-url http://127.0.0.1:3001/mcp`",
			"or set --control-plane.tunnel-id or CONTROL_PLANE_TUNNEL_ID to a tunnel_<32 lowercase hex> value",
			connectorSettingsRuntimeNote(doctorNextCommand(source)),
			"for the full first-use flow run `tunnel-client help quickstart`",
		}
	case strings.Contains(message, "control plane API key") || strings.Contains(message, "CONTROL_PLANE_API_KEY") || strings.Contains(message, "OPENAI_API_KEY"):
		check.ID = "control_plane_api_key"
		check.Why = "tunnel-client cannot poll the control plane or complete tunnel registration without this key."
		check.Next = []string{
			fmt.Sprintf("create or inspect the runtime key in %s", canonicalRuntimeAPIKeysURL),
			"export CONTROL_PLANE_API_KEY=...",
			"if your tunnel key already lives in another environment variable, map it with `export CONTROL_PLANE_API_KEY=$YOUR_EXISTING_TUNNEL_KEY_ENV`",
			fmt.Sprintf("if you also need admin CRUD, create a separate admin key in %s", canonicalAdminAPIKeysURL),
			"rerun: tunnel-client doctor",
			"if it passes, run: " + doctorNextCommand(source),
		}
	case strings.Contains(message, "control-plane.base-url"):
		check.ID = "control_plane_base_url"
		check.Why = "tunnel-client needs a valid control-plane base URL before it can talk to the tunnel service."
		check.Next = []string{"set --control-plane.base-url or CONTROL_PLANE_BASE_URL to a valid https:// URL"}
	case strings.Contains(message, "main channel is required") || strings.Contains(message, "mcp.server-url") || strings.Contains(message, "mcp.command"):
		check.ID = "mcp_target"
		check.Why = "tunnel-client needs one main MCP binding before the daemon can forward requests."
		check.Next = []string{"set --mcp.server-url or --mcp.command, or update the profile to include channel=main"}
	case source.Path != "" && strings.Contains(message, source.Path):
		check.ID = "profile_load"
		check.Why = "the selected profile file must parse cleanly before tunnel-client can validate the rest of the config."
		check.Next = []string{fmt.Sprintf("fix %s and rerun tunnel-client doctor", source.Path)}
	default:
		check.Why = "tunnel-client found a configuration problem that prevents the daemon from starting cleanly."
		check.Next = []string{"rerun with --explain or inspect the selected profile/config file"}
	}
	return check
}

func doctorCanonicalWebPropertiesChecks() []doctorCheck {
	checks := make([]doctorCheck, 0, len(canonicalWebProperties))
	for _, property := range canonicalWebProperties {
		checks = append(checks, doctorCheck{
			ID:      property.CheckID,
			Status:  doctorStatusPass,
			Summary: property.URL,
		})
	}
	return checks
}

func doctorAPIKeySummary(fs *pflag.FlagSet, lookupEnv func(string) (string, bool)) string {
	if flag := fs.Lookup("control-plane.api-key"); flag != nil && flag.Changed {
		return flag.Value.String()
	}
	if value, ok := lookupEnv("CONTROL_PLANE_API_KEY"); ok && strings.TrimSpace(value) != "" {
		return "env:CONTROL_PLANE_API_KEY"
	}
	if value, ok := lookupEnv("OPENAI_API_KEY"); ok && strings.TrimSpace(value) != "" {
		return "env:OPENAI_API_KEY"
	}
	return "configured"
}

func doctorReachabilityCheck(serverURL *url.URL) doctorCheck {
	if serverURL == nil {
		return doctorCheck{ID: "mcp_server_reachable", Status: doctorStatusSkip, Summary: "no HTTP MCP target configured"}
	}
	client := http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, serverURL.String(), nil)
	if err == nil {
		resp, err := client.Do(req)
		if err == nil {
			defer func() {
				_ = resp.Body.Close()
			}()
			return doctorCheck{
				ID:      "mcp_server_reachable",
				Status:  doctorStatusPass,
				Summary: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, serverURL.String()),
			}
		}
	}

	hostPort := serverURL.Host
	if !strings.Contains(hostPort, ":") {
		if strings.EqualFold(serverURL.Scheme, "https") {
			hostPort += ":443"
		} else {
			hostPort += ":80"
		}
	}
	conn, dialErr := net.DialTimeout("tcp", hostPort, 2*time.Second)
	if dialErr == nil {
		_ = conn.Close()
		return doctorCheck{
			ID:      "mcp_server_reachable",
			Status:  doctorStatusPass,
			Summary: fmt.Sprintf("TCP connect succeeded to %s", hostPort),
		}
	}
	return doctorCheck{
		ID:      "mcp_server_reachable",
		Status:  doctorStatusFail,
		Summary: dialErr.Error(),
		Why:     "tunnel-client should be able to reach the main MCP target before the daemon starts polling.",
		Evidence: []string{
			serverURL.String(),
			dialErr.Error(),
		},
		Next: []string{
			"start the local MCP server or fix the URL/host/port",
			"rerun: tunnel-client doctor",
		},
	}
}

func doctorOAuthMetadataCheck(serverURL *url.URL) doctorCheck {
	if serverURL == nil {
		return doctorCheck{ID: "oauth_metadata", Status: doctorStatusSkip, Summary: "no HTTP MCP target configured"}
	}
	urls := oauth.BuildResourceMetadataURLs(serverURL)
	if len(urls) == 0 || urls[0] == nil {
		return doctorCheck{ID: "oauth_metadata", Status: doctorStatusSkip, Summary: "no OAuth metadata URLs derived"}
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(urls[0].String())
	if err != nil {
		return doctorCheck{
			ID:      "oauth_metadata",
			Status:  doctorStatusFail,
			Summary: err.Error(),
			Why:     "HTTP MCP servers that rely on DCR/PRMD should expose the protected-resource metadata and authorization-server metadata contract or readiness can stay degraded.",
			Evidence: []string{
				urls[0].String(),
				err.Error(),
			},
			Next: []string{
				"verify the MCP server exposes GET /.well-known/oauth-protected-resource/mcp",
				"verify authorization_servers[0] resolves to GET /.well-known/oauth-authorization-server",
				"inspect /readyz and the logged oauth_discovery_urls after startup",
			},
		}
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return doctorCheck{
			ID:      "oauth_metadata",
			Status:  doctorStatusPass,
			Summary: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, urls[0].String()),
		}
	}
	return doctorCheck{
		ID:      "oauth_metadata",
		Status:  doctorStatusFail,
		Summary: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, urls[0].String()),
		Why:     "HTTP MCP servers that rely on DCR/PRMD should expose the protected-resource metadata and authorization-server metadata contract or readiness can stay degraded.",
		Evidence: []string{
			urls[0].String(),
			fmt.Sprintf("HTTP %d", resp.StatusCode),
		},
		Next: []string{
			"verify the MCP server exposes GET /.well-known/oauth-protected-resource/mcp",
			"verify authorization_servers[0] resolves to GET /.well-known/oauth-authorization-server",
			"inspect /readyz and the embedded UI after startup",
		},
	}
}

func doctorBaseURL(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "http://127.0.0.1:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func doctorHealthListenerCheck(cfg config.HealthConfig) doctorHealthListenerResult {
	if cfg.UnixSocket != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.UnixSocket), 0o755); err != nil {
			return doctorHealthListenerResult{
				Check: doctorCheck{
					ID:      "health_listener",
					Status:  doctorStatusFail,
					Summary: err.Error(),
					Why:     "tunnel-client must bind the local health/admin listener before it can serve /healthz, /readyz, or /ui.",
					Evidence: []string{
						cfg.UnixSocket,
						err.Error(),
					},
					Next: []string{
						"choose a writable --health.unix-socket path",
						"rerun: tunnel-client doctor",
					},
				},
			}
		}
		ln, err := net.Listen("unix", cfg.UnixSocket)
		if err != nil {
			return doctorHealthListenerResult{
				Check: doctorCheck{
					ID:      "health_listener",
					Status:  doctorStatusFail,
					Summary: err.Error(),
					Why:     "tunnel-client must bind the local health/admin listener before it can serve /healthz, /readyz, or /ui.",
					Evidence: []string{
						cfg.UnixSocket,
						err.Error(),
					},
					Next: []string{
						"choose a different --health.unix-socket or remove the stale socket",
						"rerun: tunnel-client doctor",
					},
				},
			}
		}
		_ = ln.Close()
		_ = os.Remove(cfg.UnixSocket)
		return doctorHealthListenerResult{
			Check: doctorCheck{
				ID:      "health_listener",
				Status:  doctorStatusPass,
				Summary: "will bind unix socket " + cfg.UnixSocket,
			},
		}
	}

	listenAddr := cfg.ListenAddr
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return doctorHealthListenerResult{
			Check: doctorCheck{
				ID:      "health_listener",
				Status:  doctorStatusFail,
				Summary: err.Error(),
				Why:     "tunnel-client must bind the local health/admin listener before it can serve /healthz, /readyz, or /ui.",
				Evidence: []string{
					listenAddr,
					err.Error(),
				},
				Next: []string{
					"choose a different --health.listen-addr or stop the conflicting process",
					"rerun: tunnel-client doctor",
				},
			},
		}
	}
	actualAddr := ln.Addr().String()
	_ = ln.Close()
	if _, port, err := net.SplitHostPort(listenAddr); err == nil && port == "0" {
		return doctorHealthListenerResult{
			Check: doctorCheck{
				ID:      "health_listener",
				Status:  doctorStatusPass,
				Summary: "ephemeral bind ok on " + doctorBaseURL(actualAddr),
				Next: []string{
					"inspect startup summary or HEALTH_URL_FILE for the final /ui URL",
				},
			},
		}
	}
	return doctorHealthListenerResult{
		Check: doctorCheck{
			ID:      "health_listener",
			Status:  doctorStatusPass,
			Summary: "will bind " + doctorBaseURL(listenAddr),
		},
	}
}

func doctorUICheck(cfg config.HealthConfig, healthStatus doctorStatus) doctorCheck {
	if healthStatus != doctorStatusPass {
		return doctorCheck{
			ID:      "ui",
			Status:  doctorStatusSkip,
			Summary: "blocked by health listener check",
		}
	}
	if cfg.UnixSocket != "" {
		return doctorCheck{
			ID:      "ui",
			Status:  doctorStatusPass,
			Summary: "inspect startup summary or HEALTH_URL_FILE for the Unix-socket admin URL",
		}
	}
	listenAddr := cfg.ListenAddr
	if _, port, err := net.SplitHostPort(listenAddr); err == nil && port == "0" {
		return doctorCheck{
			ID:      "ui",
			Status:  doctorStatusPass,
			Summary: "ephemeral port; inspect startup summary or HEALTH_URL_FILE for the final /ui URL",
		}
	}
	return doctorCheck{
		ID:      "ui",
		Status:  doctorStatusPass,
		Summary: doctorBaseURL(listenAddr) + "/ui",
	}
}

func doctorCodexCheck(lookupEnv func(string) (string, bool)) doctorCheck {
	detection := codexplugin.Detect(lookupEnv)
	if !detection.Detected {
		return doctorCheck{
			ID:      "codex_plugin",
			Status:  doctorStatusSkip,
			Summary: "Codex not detected locally",
		}
	}
	if detection.PluginInstalled {
		return doctorCheck{
			ID:      "codex_plugin",
			Status:  doctorStatusPass,
			Summary: detection.PluginDir,
		}
	}
	return doctorCheck{
		ID:      "codex_plugin",
		Status:  doctorStatusSkip,
		Summary: fmt.Sprintf("Codex detected; Tunnel MCP plugin not installed (run `%s`)", detection.InstallHint),
		Why:     "the optional Codex plugin gives tunnel-client a more discoverable Codex-native control surface.",
		Evidence: []string{
			"CODEX_HOME: " + detection.CodexHome,
			"expected plugin dir: " + detection.PluginDir,
		},
		Next: []string{
			detection.InstallHint,
		},
	}
}

func doctorStdioCommandCheck(command string) doctorCheck {
	resolved, err := preflightStdioCommand(command)
	if err != nil {
		return doctorCheck{
			ID:      "mcp_command_executable",
			Status:  doctorStatusFail,
			Summary: err.Error(),
			Why:     "stdio MCP targets are spawned as local child processes during `tunnel-client run`; if the executable is missing or not executable, the daemon stays up but requests through that MCP target fail immediately.",
			Evidence: []string{
				command,
				err.Error(),
			},
			Next: []string{
				"install the command or fix the first executable token in mcp.command",
				"if the command is a script, run chmod +x on the script and ensure its shebang points to an installed interpreter",
				"for wrapper commands, verify the shell or interpreter exists and that the wrapped script path is readable or executable as invoked",
				"rerun: tunnel-client doctor",
			},
		}
	}
	return doctorCheck{
		ID:      "mcp_command_executable",
		Status:  doctorStatusPass,
		Summary: resolved,
	}
}
