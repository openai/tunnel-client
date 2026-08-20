package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/openai/tunnel-client/pkg/codexplugin/session"
	"github.com/openai/tunnel-client/pkg/healthurl"
)

type healthLocatorReport struct {
	Kind            string `json:"kind,omitempty"`
	URL             string `json:"url,omitempty"`
	URLFile         string `json:"url_file,omitempty"`
	Port            int    `json:"port,omitempty"`
	ResolvedBaseURL string `json:"resolved_base_url,omitempty"`
	Error           string `json:"error,omitempty"`
}

type healthProcessReport struct {
	PID     int    `json:"pid,omitempty"`
	PIDFile string `json:"pid_file,omitempty"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

type healthReport struct {
	Locator          healthLocatorReport   `json:"locator"`
	Process          *healthProcessReport  `json:"process,omitempty"`
	BaseURL          string                `json:"base_url,omitempty"`
	UIURL            string                `json:"ui_url,omitempty"`
	Healthz          session.EndpointProbe `json:"healthz"`
	Readyz           session.EndpointProbe `json:"readyz"`
	ControlPlanePoll *healthMetricProbe    `json:"control_plane_poll,omitempty"`
	Result           string                `json:"result"`
}

type healthMetricProbe struct {
	URL   string  `json:"url,omitempty"`
	Value float64 `json:"value,omitempty"`
	OK    bool    `json:"ok"`
	Error string  `json:"error,omitempty"`
}

func newHealthCommand(stdout io.Writer, stderr io.Writer) *cobra.Command {
	var jsonOutput bool
	var urlValue string
	var urlFile string
	var port int
	var pid int
	var pidFile string
	var requireControlPlanePoll bool

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Probe tunnel-client /healthz and /readyz for a live daemon",
		Long: strings.TrimSpace(`
Probe a live tunnel-client daemon using either its admin base URL, a health URL file,
or a loopback port. Optional PID cross-checks let scripts verify that the expected
process is still running while the health endpoints are being probed.
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := inspectHealth(
				urlValue,
				urlFile,
				port,
				pid,
				pidFile,
				requireControlPlanePoll,
			)
			if err != nil {
				return err
			}
			if jsonOutput {
				if err := writeJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			} else {
				printHealthReport(cmd.OutOrStdout(), report)
			}
			if report.Result != "ok" {
				return silentExitError{code: 2}
			}
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit machine-readable JSON output")
	cmd.Flags().StringVar(&urlValue, "url", "", "Tunnel-client admin base URL or /healthz|/readyz endpoint URL")
	cmd.Flags().StringVar(&urlFile, "url-file", "", "File containing the tunnel-client admin base URL")
	cmd.Flags().IntVar(&port, "port", 0, "Loopback port for the tunnel-client admin server")
	cmd.Flags().IntVar(&pid, "pid", 0, "Optional PID cross-check for the expected tunnel-client process")
	cmd.Flags().StringVar(&pidFile, "pid-file", "", "Optional file containing the expected tunnel-client PID")
	cmd.Flags().BoolVar(
		&requireControlPlanePoll,
		"require-control-plane-poll",
		false,
		"Require one successful control-plane poll before reporting ready",
	)
	return cmd
}

func inspectHealth(
	urlValue string,
	urlFile string,
	port int,
	pid int,
	pidFile string,
	requireControlPlanePoll bool,
) (healthReport, error) {
	if err := validateHealthInputs(urlValue, urlFile, port, pid, pidFile); err != nil {
		return healthReport{}, err
	}

	report := healthReport{
		Locator: resolveHealthLocator(urlValue, urlFile, port),
		Result:  "fail",
	}

	if report.Locator.ResolvedBaseURL != "" {
		probe := session.ProbeHealthEndpoints(report.Locator.ResolvedBaseURL)
		report.BaseURL = probe.BaseURL
		if probe.BaseURL != "" {
			report.UIURL = strings.TrimRight(probe.BaseURL, "/") + "/ui"
		}
		report.Healthz = probe.Healthz
		report.Readyz = probe.Readyz
		if requireControlPlanePoll {
			controlPlanePoll := probeControlPlanePoll(report.Locator.ResolvedBaseURL)
			report.ControlPlanePoll = &controlPlanePoll
		}
	}

	if pid > 0 || pidFile != "" {
		report.Process = resolveHealthProcess(pid, pidFile)
	}

	if healthReportOK(report) {
		report.Result = "ok"
	}
	return report, nil
}

func validateHealthInputs(urlValue string, urlFile string, port int, pid int, pidFile string) error {
	locators := 0
	if strings.TrimSpace(urlValue) != "" {
		locators++
	}
	if strings.TrimSpace(urlFile) != "" {
		locators++
	}
	if port != 0 {
		locators++
	}
	if locators != 1 {
		return fmt.Errorf("choose exactly one of --url, --url-file, or --port")
	}
	if port < 0 {
		return fmt.Errorf("--port must be positive")
	}
	if pid < 0 {
		return fmt.Errorf("--pid must be positive")
	}
	if pid > 0 && strings.TrimSpace(pidFile) != "" {
		return fmt.Errorf("choose at most one of --pid or --pid-file")
	}
	return nil
}

func resolveHealthLocator(urlValue string, urlFile string, port int) healthLocatorReport {
	switch {
	case strings.TrimSpace(urlValue) != "":
		baseURL := session.NormalizeHealthBaseURL(urlValue)
		report := healthLocatorReport{
			Kind:            "url",
			URL:             strings.TrimSpace(urlValue),
			ResolvedBaseURL: baseURL,
		}
		if baseURL == "" {
			report.Error = "health URL is empty after normalization"
		}
		return report
	case strings.TrimSpace(urlFile) != "":
		report := healthLocatorReport{
			Kind:    "url_file",
			URLFile: strings.TrimSpace(urlFile),
		}
		data, err := os.ReadFile(report.URLFile)
		if err != nil {
			report.Error = fmt.Sprintf("read %s: %v", report.URLFile, err)
			return report
		}
		baseURL := session.NormalizeHealthBaseURL(string(data))
		report.ResolvedBaseURL = baseURL
		if baseURL == "" {
			report.Error = fmt.Sprintf("%s did not contain a health URL", report.URLFile)
		}
		return report
	default:
		baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
		return healthLocatorReport{
			Kind:            "port",
			Port:            port,
			ResolvedBaseURL: baseURL,
		}
	}
}

func resolveHealthProcess(pid int, pidFile string) *healthProcessReport {
	report := &healthProcessReport{}
	if strings.TrimSpace(pidFile) != "" {
		report.PIDFile = strings.TrimSpace(pidFile)
		data, err := os.ReadFile(report.PIDFile)
		if err != nil {
			report.Error = fmt.Sprintf("read %s: %v", report.PIDFile, err)
			return report
		}
		value := strings.TrimSpace(string(data))
		parsedPID, err := strconv.Atoi(value)
		if err != nil || parsedPID <= 0 {
			report.Error = fmt.Sprintf("parse %s as pid: %q", report.PIDFile, value)
			return report
		}
		report.PID = parsedPID
	} else {
		report.PID = pid
	}
	report.Running = session.PIDIsRunning(report.PID)
	return report
}

func healthReportOK(report healthReport) bool {
	if report.Locator.Error != "" {
		return false
	}
	if !report.Healthz.OK || !report.Readyz.OK {
		return false
	}
	if report.Process != nil && (report.Process.Error != "" || !report.Process.Running) {
		return false
	}
	if report.ControlPlanePoll != nil && !report.ControlPlanePoll.OK {
		return false
	}
	return true
}

func printHealthReport(w io.Writer, report healthReport) {
	_, _ = fmt.Fprintf(w, "Locator: %s\n", locatorSummary(report.Locator))
	if report.Locator.Error != "" {
		_, _ = fmt.Fprintf(w, "Locator error: %s\n", report.Locator.Error)
	}
	if report.BaseURL != "" {
		_, _ = fmt.Fprintf(w, "Base URL: %s\n", report.BaseURL)
	}
	if report.UIURL != "" {
		_, _ = fmt.Fprintf(w, "UI URL: %s\n", report.UIURL)
	}
	printEndpointReport(w, "Healthz", report.Healthz)
	printEndpointReport(w, "Readyz", report.Readyz)
	if report.ControlPlanePoll != nil {
		printMetricProbe(w, "Control-plane poll", *report.ControlPlanePoll)
	}
	if report.Process != nil {
		_, _ = fmt.Fprintf(w, "Process: %s\n", processSummary(*report.Process))
	}
	_, _ = fmt.Fprintf(w, "Result: %s\n", strings.ToUpper(report.Result))
}

func locatorSummary(locator healthLocatorReport) string {
	switch locator.Kind {
	case "url":
		return "url=" + locator.URL
	case "url_file":
		return "url_file=" + locator.URLFile
	case "port":
		return fmt.Sprintf("port=%d", locator.Port)
	default:
		return "unknown"
	}
}

func processSummary(process healthProcessReport) string {
	if process.Error != "" {
		if process.PIDFile != "" {
			return "FAIL | pid_file=" + process.PIDFile + " | " + process.Error
		}
		return "FAIL | " + process.Error
	}
	status := "FAIL"
	if process.Running {
		status = "PASS"
	}
	if process.PIDFile != "" {
		return fmt.Sprintf("%s | pid=%d | pid_file=%s", status, process.PID, process.PIDFile)
	}
	return fmt.Sprintf("%s | pid=%d", status, process.PID)
}

func printEndpointReport(w io.Writer, label string, endpoint session.EndpointProbe) {
	parts := []string{}
	status := "FAIL"
	if endpoint.OK {
		status = "PASS"
	}
	parts = append(parts, status)
	if endpoint.URL != "" {
		parts = append(parts, endpoint.URL)
	}
	if endpoint.Status != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", endpoint.Status))
	}
	if endpoint.Error != "" {
		parts = append(parts, endpoint.Error)
	} else if endpoint.Body != "" {
		parts = append(parts, endpoint.Body)
	}
	_, _ = fmt.Fprintf(w, "%s: %s\n", label, strings.Join(parts, " | "))
}

func probeControlPlanePoll(baseURL string) healthMetricProbe {
	target, err := healthurl.Parse(baseURL)
	if err != nil {
		return healthMetricProbe{Error: err.Error()}
	}
	metricsURL := target.URL("/metrics")
	probe := healthMetricProbe{URL: metricsURL}

	client, err := target.HTTPClient(2 * time.Second)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	response, err := client.Get(target.RequestURL("/metrics"))
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		probe.Error = response.Status
		return probe
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}

	value, ok := parseMetricValue(string(body), "commands_poll_last_successful_timestamp_seconds")
	if !ok {
		probe.Error = "missing commands_poll_last_successful_timestamp_seconds metric"
		return probe
	}

	probe.Value = value
	probe.OK = value > 0
	if !probe.OK {
		probe.Error = "no successful control-plane poll observed"
	}
	return probe
}

func parseMetricValue(metrics string, metricName string) (float64, bool) {
	for _, line := range strings.Split(metrics, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line != metricName &&
			!strings.HasPrefix(line, metricName+" ") &&
			!strings.HasPrefix(line, metricName+"{") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		return value, true
	}
	return 0, false
}

func printMetricProbe(w io.Writer, label string, probe healthMetricProbe) {
	parts := []string{}
	status := "FAIL"
	if probe.OK {
		status = "PASS"
	}
	parts = append(parts, status)
	if probe.URL != "" {
		parts = append(parts, probe.URL)
	}
	if probe.OK {
		parts = append(parts, fmt.Sprintf("last_success_unix_seconds=%.0f", probe.Value))
	} else if probe.Error != "" {
		parts = append(parts, probe.Error)
	}
	_, _ = fmt.Fprintf(w, "%s: %s\n", label, strings.Join(parts, " | "))
}
